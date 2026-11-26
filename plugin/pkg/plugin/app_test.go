package plugin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/grafana/grafana-plugin-sdk-go/backend"
)

type captureSender struct {
	resp *backend.CallResourceResponse
}

func (s *captureSender) Send(resp *backend.CallResourceResponse) error {
	s.resp = resp
	return nil
}

func newTestApp(t *testing.T, baseURL, secret string) *App {
	t.Helper()
	jsonData, err := json.Marshal(map[string]any{
		"apiBaseUrl":             baseURL,
		"pollIntervalSeconds":    15,
		"defaultCallerMode":      "sso_user",
		"genericNocDisplayLabel": "Fallback Service Account (noc)",
	})
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}

	inst, err := NewApp(context.Background(), backend.AppInstanceSettings{
		JSONData: jsonData,
		DecryptedSecureJSONData: map[string]string{
			"apiJwtSecret": secret,
		},
	})
	if err != nil {
		t.Fatalf("NewApp failed: %v", err)
	}
	app, ok := inst.(*App)
	if !ok {
		t.Fatalf("unexpected instance type %T", inst)
	}
	return app
}

func TestMintOperatorJWTClaims(t *testing.T) {
	now := time.Date(2026, 11, 25, 18, 0, 0, 0, time.UTC)
	secret := "test-super-secret-key-at-least-32-chars"
	user := &backend.User{
		Login: "alice.smith",
		Email: "alice.smith@corp.example",
		Name:  "Alice Smith",
		Role:  "Editor",
	}
	perms := PermissionsForRole(user.Role)

	tok, err := MintOperatorJWT(secret, user, 7, perms, now)
	if err != nil {
		t.Fatalf("MintOperatorJWT failed: %v", err)
	}

	claims := &OperatorClaims{}
	parsed, err := jwt.ParseWithClaims(tok, claims, func(_ *jwt.Token) (any, error) {
		return []byte(secret), nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(JWTIssuer),
		jwt.WithAudience(JWTAudience),
		jwt.WithTimeFunc(func() time.Time { return now.Add(10 * time.Second) }),
	)
	if err != nil || !parsed.Valid {
		t.Fatalf("expected valid JWT, got err=%v", err)
	}

	if claims.Subject != "alice.smith" {
		t.Fatalf("expected sub=alice.smith, got %q", claims.Subject)
	}
	if claims.Email != "alice.smith@corp.example" {
		t.Fatalf("expected email=alice.smith@corp.example, got %q", claims.Email)
	}
	if claims.Name != "Alice Smith" {
		t.Fatalf("expected name=Alice Smith, got %q", claims.Name)
	}
	if claims.OrgID != 7 {
		t.Fatalf("expected org_id=7, got %d", claims.OrgID)
	}
	if claims.Role != "Editor" {
		t.Fatalf("expected role=Editor, got %q", claims.Role)
	}
	if claims.ID == "" {
		t.Fatal("expected non-empty jti claim")
	}
	if gotTTL := claims.ExpiresAt.Sub(claims.IssuedAt.Time); gotTTL != 60*time.Second {
		t.Fatalf("expected 60s TTL, got %s", gotTTL)
	}
	if len(claims.Permissions) != 3 {
		t.Fatalf("expected 3 permissions for Editor, got %v", claims.Permissions)
	}
}

func TestCallResourceRejectsUnauthenticatedUser(t *testing.T) {
	app := newTestApp(t, "http://127.0.0.1:9999", "secret-1234567890")
	sender := &captureSender{}

	err := app.CallResource(context.Background(), &backend.CallResourceRequest{
		Method: http.MethodGet,
		Path:   "v1/alerts",
		URL:    "/api/plugins/noc-alert-manager-app/resources/v1/alerts",
		PluginContext: backend.PluginContext{
			OrgID: 1,
			User:  nil,
		},
	}, sender)
	if err != nil {
		t.Fatalf("CallResource returned unexpected error: %v", err)
	}
	if sender.resp == nil || sender.resp.Status != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %+v", sender.resp)
	}
}

func TestCallResourceRBACEnforcementAndRouteAllowlist(t *testing.T) {
	upstreamCalled := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"summary":{},"items":[]}`))
	}))
	defer srv.Close()

	app := newTestApp(t, srv.URL, "secret-1234567890")

	cases := []struct {
		name           string
		role           string
		method         string
		path           string
		expectedStatus int
	}{
		{"viewer reads alerts", "Viewer", http.MethodGet, "v1/alerts", http.StatusOK},
		{"viewer reads metadata", "Viewer", http.MethodGet, "v1/servicenow/metadata", http.StatusOK},
		{"viewer reads history", "Viewer", http.MethodGet, "v1/alerts/11111111-1111-4111-8111-111111111111/history", http.StatusOK},
		{"viewer cannot acknowledge", "Viewer", http.MethodPost, "v1/alerts/11111111-1111-4111-8111-111111111111/acknowledge", http.StatusForbidden},
		{"viewer cannot tag team", "Viewer", http.MethodPost, "v1/alerts/11111111-1111-4111-8111-111111111111/team", http.StatusForbidden},
		{"viewer cannot update confluence url", "Viewer", http.MethodPost, "v1/alerts/11111111-1111-4111-8111-111111111111/confluence", http.StatusForbidden},
		{"viewer cannot bulk dismiss", "Viewer", http.MethodPost, "v1/alerts/bulk/dismiss", http.StatusForbidden},
		{"viewer cannot create snow incident", "Viewer", http.MethodPost, "v1/alerts/11111111-1111-4111-8111-111111111111/servicenow/incident", http.StatusForbidden},
		{"viewer cannot create bulk snow incident", "Viewer", http.MethodPost, "v1/servicenow/incidents/bulk", http.StatusForbidden},
		{"editor can acknowledge", "Editor", http.MethodPost, "v1/alerts/11111111-1111-4111-8111-111111111111/acknowledge", http.StatusOK},
		{"editor can tag team", "Editor", http.MethodPost, "v1/alerts/11111111-1111-4111-8111-111111111111/team", http.StatusOK},
		{"editor can update confluence url", "Editor", http.MethodPost, "v1/alerts/11111111-1111-4111-8111-111111111111/confluence", http.StatusOK},
		{"editor can bulk dismiss", "Editor", http.MethodPost, "v1/alerts/bulk/dismiss", http.StatusOK},
		{"editor can create bulk snow incident", "Editor", http.MethodPost, "v1/servicenow/incidents/bulk", http.StatusOK},
		{"editor can post work notes", "Editor", http.MethodPost, "v1/alerts/11111111-1111-4111-8111-111111111111/servicenow/work-notes", http.StatusOK},
		{"admin can sync snow incident", "Admin", http.MethodPost, "v1/alerts/11111111-1111-4111-8111-111111111111/servicenow/sync", http.StatusOK},
		{"webhooks blocked", "Admin", http.MethodPost, "v1/webhooks/alertmanager", http.StatusNotFound},
		{"traversal blocked", "Admin", http.MethodGet, "v1/alerts/../webhooks/alertmanager", http.StatusBadRequest},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sender := &captureSender{}
			err := app.CallResource(context.Background(), &backend.CallResourceRequest{
				Method: tc.method,
				Path:   tc.path,
				URL:    "/api/plugins/noc-alert-manager-app/resources/" + tc.path,
				PluginContext: backend.PluginContext{
					OrgID: 1,
					User: &backend.User{
						Login: "operator",
						Email: "operator@corp.example",
						Name:  "Operator User",
						Role:  tc.role,
					},
				},
			}, sender)
			if err != nil {
				t.Fatalf("CallResource error: %v", err)
			}
			if sender.resp == nil || sender.resp.Status != tc.expectedStatus {
				t.Fatalf("expected status %d, got %+v", tc.expectedStatus, sender.resp)
			}
		})
	}

	if upstreamCalled != 10 {
		t.Fatalf("expected exactly 10 authorized upstream calls, got %d", upstreamCalled)
	}
}

func TestCallResourceProxyForwardsHeadersQueryBodyAndErrors(t *testing.T) {
	const jwtSecret = "proxy-forwarding-secret-32-chars-min"
	var capturedAuth, capturedIdem, capturedReqID, capturedCookie, capturedQuery, capturedPath, capturedBody string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		capturedIdem = r.Header.Get("Idempotency-Key")
		capturedReqID = r.Header.Get("X-Request-Id")
		capturedCookie = r.Header.Get("Cookie")
		capturedPath = r.URL.Path
		capturedQuery = r.URL.RawQuery
		raw, _ := io.ReadAll(r.Body)
		capturedBody = string(raw)

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("X-Request-Id", "req-upstream-42")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"code":"CONFLICT_STALE_VERSION","message":"stale version","details":{"current_version":3}}}`))
	}))
	defer srv.Close()

	app := newTestApp(t, srv.URL, jwtSecret)
	fixedNow := time.Date(2026, 11, 25, 19, 30, 0, 0, time.UTC)
	app.now = func() time.Time { return fixedNow }

	sender := &captureSender{}
	err := app.CallResource(context.Background(), &backend.CallResourceRequest{
		Method: http.MethodPost,
		Path:   "v1/alerts/11111111-1111-4111-8111-111111111111/servicenow/incident",
		URL:    "/api/plugins/noc-alert-manager-app/resources/v1/alerts/11111111-1111-4111-8111-111111111111/servicenow/incident?dry_run=false",
		Headers: map[string][]string{
			"Content-Type":    {"application/json"},
			"Idempotency-Key": {"idem-uuid-1234"},
			"X-Request-Id":    {"req-client-1"},
			"Authorization":   {"Bearer forged-client-token"},
			"Cookie":          {"grafana_session=secret"},
		},
		Body: []byte(`{"short_description":"Disk Full","impact":1,"urgency":1}`),
		PluginContext: backend.PluginContext{
			OrgID: 3,
			User: &backend.User{
				Login: "jane.doe",
				Email: "jane.doe@corp.example",
				Name:  "Jane Doe",
				Role:  "Editor",
			},
		},
	}, sender)
	if err != nil {
		t.Fatalf("CallResource error: %v", err)
	}

	if capturedPath != "/api/v1/alerts/11111111-1111-4111-8111-111111111111/servicenow/incident" {
		t.Fatalf("unexpected upstream path %q", capturedPath)
	}
	if capturedQuery != "dry_run=false" {
		t.Fatalf("unexpected upstream query %q", capturedQuery)
	}
	if capturedIdem != "idem-uuid-1234" {
		t.Fatalf("expected Idempotency-Key forwarded, got %q", capturedIdem)
	}
	if capturedReqID != "req-client-1" {
		t.Fatalf("expected X-Request-Id forwarded, got %q", capturedReqID)
	}
	if capturedCookie != "" {
		t.Fatalf("expected Cookie header stripped, got %q", capturedCookie)
	}
	if capturedBody != `{"short_description":"Disk Full","impact":1,"urgency":1}` {
		t.Fatalf("unexpected upstream body %q", capturedBody)
	}
	if !strings.HasPrefix(capturedAuth, "Bearer ") || strings.Contains(capturedAuth, "forged-client-token") {
		t.Fatalf("expected forged Authorization to be replaced, got %q", capturedAuth)
	}

	// Verify minted JWT verifies cleanly with the shared secret.
	tokenStr := strings.TrimPrefix(capturedAuth, "Bearer ")
	claims := &OperatorClaims{}
	parsed, err := jwt.ParseWithClaims(tokenStr, claims, func(_ *jwt.Token) (any, error) {
		return []byte(jwtSecret), nil
	}, jwt.WithTimeFunc(func() time.Time { return fixedNow }))
	if err != nil || !parsed.Valid {
		t.Fatalf("minted JWT failed verification: %v", err)
	}
	if claims.Subject != "jane.doe" || claims.Email != "jane.doe@corp.example" || claims.OrgID != 3 {
		t.Fatalf("unexpected JWT claims: %+v", claims)
	}

	// Verify 409 status + body + X-Request-Id passed through verbatim.
	if sender.resp.Status != http.StatusConflict {
		t.Fatalf("expected 409 pass-through, got %d", sender.resp.Status)
	}
	if !strings.Contains(string(sender.resp.Body), "CONFLICT_STALE_VERSION") {
		t.Fatalf("expected conflict error body, got %s", string(sender.resp.Body))
	}
	if got := firstHeaderValue(sender.resp.Headers, "X-Request-Id"); got != "req-upstream-42" {
		t.Fatalf("expected X-Request-Id header passed back, got %q", got)
	}
}

func TestCheckHealth(t *testing.T) {
	t.Run("unconfigured settings", func(t *testing.T) {
		inst, err := NewApp(context.Background(), backend.AppInstanceSettings{})
		if err != nil {
			t.Fatalf("NewApp error: %v", err)
		}
		res, err := inst.(*App).CheckHealth(context.Background(), &backend.CheckHealthRequest{})
		if err != nil {
			t.Fatalf("CheckHealth error: %v", err)
		}
		if res.Status != backend.HealthStatusError {
			t.Fatalf("expected error status, got %v", res.Status)
		}
	})

	t.Run("healthy api with reachable snow", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v1/servicenow/metadata" {
				t.Fatalf("unexpected path %s", r.URL.Path)
			}
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"snow_reachable":true}`))
		}))
		defer srv.Close()

		app := newTestApp(t, srv.URL, "health-secret-32-chars-minimum-12")
		res, err := app.CheckHealth(context.Background(), &backend.CheckHealthRequest{
			PluginContext: backend.PluginContext{OrgID: 1},
		})
		if err != nil {
			t.Fatalf("CheckHealth error: %v", err)
		}
		if res.Status != backend.HealthStatusOk {
			t.Fatalf("expected HealthStatusOk, got %v (%s)", res.Status, res.Message)
		}
	})

	t.Run("secret mismatch returns error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer srv.Close()

		app := newTestApp(t, srv.URL, "wrong-secret")
		res, err := app.CheckHealth(context.Background(), &backend.CheckHealthRequest{})
		if err != nil {
			t.Fatalf("CheckHealth error: %v", err)
		}
		if res.Status != backend.HealthStatusError {
			t.Fatalf("expected HealthStatusError, got %v", res.Status)
		}
	})
}
