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

	// 5. Legacy username lookup mode
	res = resolver.Resolve(context.Background(), model.CallerModeSSOUser, auth.OperatorContext{Login: "alice.smith", LookupByUsername: true})
	if res.ResolutionStatus != model.CallerResSSOMatched || res.SysID != "u_alice_001" {
		t.Fatalf("expected username lookup match for alice.smith, got %+v", res)
	}
	_, err = client.CreateIncident(context.Background(), CreateIncidentPayload{
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
