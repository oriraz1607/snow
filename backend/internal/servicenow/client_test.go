package servicenow

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oriraz/grafana-snow-integration/backend/internal/auth"
	"github.com/oriraz/grafana-snow-integration/backend/internal/mocksnow"
	"github.com/oriraz/grafana-snow-integration/backend/internal/model"
	"github.com/oriraz/grafana-snow-integration/backend/internal/observability"
)

type memUserCache struct {
	mu      sync.Mutex
	entries map[string]model.SnowUserCacheEntry
}

func newMemUserCache() *memUserCache {
	return &memUserCache{entries: make(map[string]model.SnowUserCacheEntry)}
}

func (m *memUserCache) LookupUserCache(_ context.Context, key string) (*model.SnowUserCacheEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[key]
	if !ok || time.Now().After(e.ExpiresAt) {
		return nil, nil
	}
	cp := e
	return &cp, nil
}

func (m *memUserCache) UpsertUserCache(_ context.Context, entry model.SnowUserCacheEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[entry.Email] = entry
	return nil
}

func TestOAuthRefreshAnd401RetryOnPost(t *testing.T) {
	mockSrv := mocksnow.NewServer(mocksnow.Config{
		ClientID:              "cid",
		ClientSecret:          "csec",
		DefaultCallerSysID:    "noc_001",
		DefaultCallerUsername: "noc",
	})
	ts := httptest.NewServer(mockSrv.Handler())
	defer ts.Close()

	// Trigger one forced 401 on the first Table API call to prove OAuth invalidation + single retry on POST.
	respFault, err := http.Post(ts.URL+"/_mock/faults", "application/json", strings.NewReader(`{"force_401_once":true}`))
	if err != nil {
		t.Fatalf("set fault: %v", err)
	}
	respFault.Body.Close()

	metrics := observability.NewMetrics()
	oauth := NewOAuthTokenProvider(ts.URL+"/oauth_token.do", "cid", "csec", "", nil)
	breaker := NewCircuitBreaker(nil)
	client := NewClient(ts.URL, 5*time.Second, oauth, breaker, metrics)

	rec, err := client.CreateIncident(context.Background(), CreateIncidentPayload{
		ShortDescription: "Test 401 refresh",
		Impact:           "1",
		Urgency:          "1",
		CallerID:         "u_alice_001",
		CorrelationID:    "alert-401-test",
	})
	if err != nil {
		t.Fatalf("expected POST CreateIncident to recover after 401 token refresh, got error: %v", err)
	}
	if rec.Number == "" || rec.SysID == "" {
		t.Fatalf("expected valid incident record, got %+v", rec)
	}
}

func TestCircuitBreakerTransitions(t *testing.T) {
	now := time.Unix(1000, 0)
	var transitions []BreakerState
	cb := NewCircuitBreaker(func(s BreakerState) {
		transitions = append(transitions, s)
	})
	cb.now = func() time.Time { return now }

	for i := 0; i < 4; i++ {
		if !cb.Allow() {
			t.Fatalf("expected Allow() true before threshold at i=%d", i)
		}
		cb.RecordFailure()
		if cb.State() != BreakerClosed {
			t.Fatalf("expected closed before 5 failures, got %d", cb.State())
		}
	}

	// 5th consecutive failure opens the circuit
	cb.RecordFailure()
	if cb.State() != BreakerOpen || cb.Allow() {
		t.Fatalf("expected open circuit rejecting requests")
	}

	// Advance 31 seconds -> half-open probe allowed once
	now = now.Add(31 * time.Second)
	if !cb.Allow() {
		t.Fatalf("expected half-open probe allowed after cooldown")
	}
	if cb.Allow() {
		t.Fatalf("expected concurrent second half-open probe rejected")
	}

	cb.RecordSuccess()
	if cb.State() != BreakerClosed {
		t.Fatalf("expected closed circuit after probe success")
	}
}

func TestCallerResolverCacheAndFallback(t *testing.T) {
	mockSrv := mocksnow.NewServer(mocksnow.Config{
		Username:              "svc",
		Password:              "pass",
		DefaultCallerSysID:    "noc_001",
		DefaultCallerUsername: "noc",
	})
	ts := httptest.NewServer(mockSrv.Handler())
	defer ts.Close()

	metrics := observability.NewMetrics()
	client := NewClient(ts.URL, 5*time.Second, NewBasicAuthProvider("svc", "pass"), NewCircuitBreaker(nil), metrics)
	cache := newMemUserCache()
	resolver := NewCallerResolver(cache, client, "noc_001", metrics, nil)

	// 1. Explicit generic_noc
	res := resolver.Resolve(context.Background(), model.CallerModeGenericNOC, auth.OperatorContext{Login: "alice.smith", Email: "alice.smith@corp.example"})
	if res.ResolutionStatus != model.CallerResGenericNOCSelected || res.SysID != "noc_001" {
		t.Fatalf("unexpected generic_noc resolution: %+v", res)
	}

	// 2. SSO email match + populates positive cache
	res = resolver.Resolve(context.Background(), model.CallerModeSSOUser, auth.OperatorContext{Login: "alice.smith", Email: "alice.smith@corp.example"})
	if res.ResolutionStatus != model.CallerResSSOMatched || res.SysID != "u_alice_001" {
		t.Fatalf("expected sso_matched u_alice_001, got %+v", res)
	}
	if _, ok := cache.entries["alice.smith@corp.example"]; !ok {
		t.Fatalf("expected positive lookup cached")
	}

	// 3. Missing SSO user -> fallback_noc + negative cache
	res = resolver.Resolve(context.Background(), model.CallerModeSSOUser, auth.OperatorContext{Login: "nobody", Email: "nobody@corp.example"})
	if res.ResolutionStatus != model.CallerResFallbackNOC || res.SysID != "noc_001" || res.FallbackReason == nil {
		t.Fatalf("expected fallback_noc for missing user, got %+v", res)
	}
	neg, ok := cache.entries["nobody@corp.example"]
	if !ok || neg.SnowSysID != nil || neg.IsActive {
		t.Fatalf("expected negative cache entry for nobody@corp.example, got %+v", neg)
	}

	// 4. HTTP error during lookup -> fallback_noc WITHOUT caching error
	resp, err := http.Post(ts.URL+"/_mock/faults", "application/json", strings.NewReader(`{"sys_user_status_code":500}`))
	if err != nil {
		t.Fatalf("faults post: %v", err)
	}
	resp.Body.Close()

	res = resolver.Resolve(context.Background(), model.CallerModeSSOUser, auth.OperatorContext{Login: "tempfail", Email: "tempfail@corp.example"})
	if res.ResolutionStatus != model.CallerResFallbackNOC || res.SysID != "noc_001" {
		t.Fatalf("expected fallback_noc on lookup error, got %+v", res)
	}
	if _, cached := cache.entries["tempfail@corp.example"]; cached {
		t.Fatalf("transient error must NOT be cached")
	}

	// Clear faults before testing legacy username lookup and correlation_id query
	resp2, _ := http.Post(ts.URL+"/_mock/faults", "application/json", strings.NewReader(`{}`))
	resp2.Body.Close()

	// 5. Email miss falls back to LookupUserByUsername(op.Login) and caches under user:<login>
	res = resolver.Resolve(context.Background(), model.CallerModeSSOUser, auth.OperatorContext{
		Login: "alice.smith",
		Email: "alice.alias@other.example",
	})
	if res.ResolutionStatus != model.CallerResSSOMatched || res.SysID != "u_alice_001" {
		t.Fatalf("expected username fallback match after email miss, got %+v", res)
	}
	if _, ok := cache.entries["user:alice.smith"]; !ok {
		t.Fatalf("expected username fallback cached under user:alice.smith")
	}

	// 6. Empty email falls back to LookupUserByUsername even when LookupByUsername=false
	res = resolver.Resolve(context.Background(), model.CallerModeSSOUser, auth.OperatorContext{Login: "alice.smith"})
	if res.ResolutionStatus != model.CallerResSSOMatched || res.SysID != "u_alice_001" {
		t.Fatalf("expected username lookup match for alice.smith with empty email, got %+v", res)
	}

	createdRec, err := client.CreateIncident(context.Background(), CreateIncidentPayload{
		ShortDescription: "Corr check",
		Impact:           "2",
		Urgency:          "2",
		CallerID:         "u_alice_001",
		CorrelationID:    "corr-abc",
	})
	if err != nil {
		t.Fatalf("create incident failed: %v", err)
	}
	found, err := client.FindIncidentByCorrelationID(context.Background(), "corr-abc")
	if err != nil || found == nil {
		t.Fatalf("expected correlation_id lookup success, got rec=%v err=%v", found, err)
	}

	reopened, err := client.ReopenIncident(context.Background(), createdRec.SysID, "[Grafana NOC] Auto-reopened: alert still firing")
	if err != nil || reopened == nil || reopened.State != 2 {
		t.Fatalf("expected ReopenIncident to move state back to 2 (In Progress), got rec=%+v err=%v", reopened, err)
	}
}

func TestIsCallerIDRejection(t *testing.T) {
	se := &SnowError{UpstreamStatus: 400, UpstreamBody: `{"error":{"detail":"Invalid reference on caller_id"}}`}
	if !se.IsCallerIDRejection() {
		t.Fatalf("expected IsCallerIDRejection true")
	}
	if errors.Is(se, context.DeadlineExceeded) {
		t.Fatalf("unexpected error match")
	}
}

func TestSysparmQueryValidation(t *testing.T) {
	httpHits := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		httpHits++
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	metrics := observability.NewMetrics()
	client := NewClient(ts.URL, 2*time.Second, NewBasicAuthProvider("svc", "pass"), NewCircuitBreaker(nil), metrics)

	if _, err := client.FindIncidentByNumber(context.Background(), "INC001^ORpriority=1"); err == nil {
		t.Fatalf("expected FindIncidentByNumber to reject ^ operator")
	}
	if _, err := client.FindIncidentByNumber(context.Background(), "IN"); err == nil {
		t.Fatalf("expected FindIncidentByNumber to reject too-short incident number")
	}
	if _, err := client.LookupUserByEmail(context.Background(), "a@b.com^active=false"); err == nil {
		t.Fatalf("expected LookupUserByEmail to reject ^ operator")
	}
	if _, err := client.LookupUserByUsername(context.Background(), "alice\n^admin=true"); err == nil {
		t.Fatalf("expected LookupUserByUsername to reject newline/^ operator")
	}
	if _, err := client.FindIncidentByCorrelationID(context.Background(), "abc^OR1=1"); err == nil {
		t.Fatalf("expected FindIncidentByCorrelationID to reject ^ operator")
	}
	if _, err := client.GetIncidentsBySysIDs(context.Background(), "get", []string{"sys1,sys2"}); err == nil {
		t.Fatalf("expected GetIncidentsBySysIDs to reject comma inside sys_id")
	}
	if httpHits != 0 {
		t.Fatalf("expected 0 outbound HTTP calls for rejected sysparm_query inputs, got %d", httpHits)
	}

	// CallerResolver safely falls back to default NOC caller when poisoned operator email is rejected.
	resolver := NewCallerResolver(nil, client, "noc_001", metrics, nil)
	res := resolver.Resolve(context.Background(), model.CallerModeSSOUser, auth.OperatorContext{
		Login: "attacker",
		Email: "attacker@corp.example^ORactive=true",
	})
	if res.ResolutionStatus != model.CallerResFallbackNOC || res.SysID != "noc_001" {
		t.Fatalf("expected fallback_noc on rejected sys_user query input, got %+v", res)
	}
}
