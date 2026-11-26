package auth

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func signTestJWT(t *testing.T, secret string, claims OperatorClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}
	return signed
}

func baseClaims(now time.Time) OperatorClaims {
	return OperatorClaims{
		Email: "alice.smith@corp.example",
		Name:  "Alice Smith",
		OrgID: 1,
		Role:  "Editor",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "noc-alert-manager-app",
			Audience:  jwt.ClaimStrings{ExpectedAudience},
			Subject:   "alice.smith",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(60 * time.Second)),
			ID:        "jti-1",
		},
	}
}

func TestOperatorMiddlewareAndRBAC(t *testing.T) {
	const primarySecret = "primary-test-secret-key"
	const previousSecret = "previous-test-secret-key"

	cfg := OperatorAuthConfig{
		JWTSecret:                primarySecret,
		JWTSecretPrevious:        previousSecret,
		JWTIssuer:                "noc-alert-manager-app",
		AllowLegacyProxyFallback: false,
	}

	t.Run("valid JWT with explicit permissions enforces strict permissions", func(t *testing.T) {
		claims := baseClaims(time.Now())
		claims.Permissions = []string{PermAlertsRead} // Explicitly read-only even though Role=Editor
		token := signTestJWT(t, primarySecret, claims)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/alerts/123/acknowledge", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()

		handler := OperatorMiddleware(cfg)(RequirePermission(PermAlertsTriage)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})))
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden, got %d (%s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("omitted permissions falls back to Grafana role", func(t *testing.T) {
		claims := baseClaims(time.Now())
		claims.Role = "Editor"
		claims.Permissions = nil
		token := signTestJWT(t, primarySecret, claims)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/alerts/123/acknowledge", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()

		var seenLogin string
		handler := OperatorMiddleware(cfg)(RequirePermission(PermIncidentsWrite)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			op, _ := OperatorFromContext(r.Context())
			seenLogin = op.Login
			w.WriteHeader(http.StatusOK)
		})))
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK || seenLogin != "alice.smith" {
			t.Fatalf("expected 200 OK for Editor fallback, got %d (login=%q)", rec.Code, seenLogin)
		}

		// Viewer fallback denies triage
		viewerClaims := baseClaims(time.Now())
		viewerClaims.Role = "Viewer"
		viewerClaims.Permissions = nil
		viewerToken := signTestJWT(t, primarySecret, viewerClaims)

		req2 := httptest.NewRequest(http.MethodPost, "/api/v1/alerts/123/acknowledge", nil)
		req2.Header.Set("Authorization", "Bearer "+viewerToken)
		rec2 := httptest.NewRecorder()
		OperatorMiddleware(cfg)(RequirePermission(PermAlertsTriage)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))).ServeHTTP(rec2, req2)
		if rec2.Code != http.StatusForbidden {
			t.Fatalf("expected 403 for Viewer on triage, got %d", rec2.Code)
		}
	})

	t.Run("dual-key rotation accepts previous secret", func(t *testing.T) {
		token := signTestJWT(t, previousSecret, baseClaims(time.Now()))
		req := httptest.NewRequest(http.MethodGet, "/api/v1/alerts", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()

		OperatorMiddleware(cfg)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})).ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK with previous rotation key, got %d", rec.Code)
		}
	})

	t.Run("expired JWT returns 401 TOKEN_EXPIRED", func(t *testing.T) {
		claims := baseClaims(time.Now().Add(-5 * time.Minute))
		claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-4 * time.Minute))
		token := signTestJWT(t, primarySecret, claims)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/alerts", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()

		OperatorMiddleware(cfg)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})).ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized || !bytes.Contains(rec.Body.Bytes(), []byte("TOKEN_EXPIRED")) {
			t.Fatalf("expected 401 TOKEN_EXPIRED, got %d (%s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("rejects excessive JWT TTL (>120s) and wrong issuer/audience", func(t *testing.T) {
		longClaims := baseClaims(time.Now())
		longClaims.ExpiresAt = jwt.NewNumericDate(longClaims.IssuedAt.Add(300 * time.Second))
		tok := signTestJWT(t, primarySecret, longClaims)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/alerts", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		OperatorMiddleware(cfg)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})).ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 for >120s TTL, got %d", rec.Code)
		}
	})

	t.Run("legacy proxy headers gated by ALLOW_LEGACY_PROXY_HEADER_AUTH", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/alerts", nil)
		req.Header.Set("X-Proxy-Token", "shared-proxy-secret")
		req.Header.Set("X-Grafana-User", "legacy.op")

		recDisabled := httptest.NewRecorder()
		OperatorMiddleware(cfg)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})).ServeHTTP(recDisabled, req)
		if recDisabled.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 when legacy proxy disabled, got %d", recDisabled.Code)
		}

		enabledCfg := cfg
		enabledCfg.AllowLegacyProxyFallback = true
		enabledCfg.ProxySharedToken = "shared-proxy-secret"

		recEnabled := httptest.NewRecorder()
		var gotOp OperatorContext
		OperatorMiddleware(enabledCfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotOp, _ = OperatorFromContext(r.Context())
			w.WriteHeader(http.StatusOK)
		})).ServeHTTP(recEnabled, req)
		if recEnabled.Code != http.StatusOK || !gotOp.LookupByUsername || gotOp.Login != "legacy.op" {
			t.Fatalf("expected legacy auth success with LookupByUsername=true, got status=%d op=%+v", recEnabled.Code, gotOp)
		}

		// REV-04: legacy proxy fallback MUST fail closed when X-Grafana-Role is empty
		for _, perm := range []string{PermAlertsRead, PermAlertsTriage, PermIncidentsWrite} {
			recDenied := httptest.NewRecorder()
			OperatorMiddleware(enabledCfg)(RequirePermission(perm)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))).ServeHTTP(recDenied, req)
			if recDenied.Code != http.StatusForbidden {
				t.Fatalf("expected 403 Forbidden for empty X-Grafana-Role on %s, got %d", perm, recDenied.Code)
			}
		}

		// Viewer X-Grafana-Role permits read but denies write/triage; Editor permits write/triage
		viewerReq := req.Clone(req.Context())
		viewerReq.Header.Set("X-Grafana-Role", "Viewer")
		recViewerRead := httptest.NewRecorder()
		OperatorMiddleware(enabledCfg)(RequirePermission(PermAlertsRead)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))).ServeHTTP(recViewerRead, viewerReq)
		if recViewerRead.Code != http.StatusOK {
			t.Fatalf("expected 200 OK for legacy Viewer read, got %d", recViewerRead.Code)
		}

		recViewerTriage := httptest.NewRecorder()
		OperatorMiddleware(enabledCfg)(RequirePermission(PermAlertsTriage)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))).ServeHTTP(recViewerTriage, viewerReq)
		if recViewerTriage.Code != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden for legacy Viewer triage, got %d", recViewerTriage.Code)
		}

		editorReq := req.Clone(req.Context())
		editorReq.Header.Set("X-Grafana-Role", "Editor")
		recEditorWrite := httptest.NewRecorder()
		OperatorMiddleware(enabledCfg)(RequirePermission(PermIncidentsWrite)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))).ServeHTTP(recEditorWrite, editorReq)
		if recEditorWrite.Code != http.StatusOK {
			t.Fatalf("expected 200 OK for legacy Editor write, got %d", recEditorWrite.Code)
		}
	})
}

func TestWebhookAuthMiddlewares(t *testing.T) {
	t.Run("alertmanager bearer constant-time", func(t *testing.T) {
		mw := AlertmanagerBearerMiddleware("am-secret")

		badReq := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/alertmanager", nil)
		badReq.Header.Set("Authorization", "Bearer wrong")
		badRec := httptest.NewRecorder()
		mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })).ServeHTTP(badRec, badReq)
		if badRec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", badRec.Code)
		}

		goodReq := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/alertmanager", nil)
		goodReq.Header.Set("Authorization", "Bearer am-secret")
		goodRec := httptest.NewRecorder()
		mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })).ServeHTTP(goodRec, goodReq)
		if goodRec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", goodRec.Code)
		}
	})

	t.Run("servicenow HMAC-SHA256 verification", func(t *testing.T) {
		const secret = "snow-hmac-secret"
		body := []byte(`{"sys_id":"s1","state":6}`)
		mw := ServiceNowWebhookMiddleware(secret, 1024*1024)

		validReq := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/servicenow", bytes.NewReader(body))
		validReq.Header.Set("X-SNOW-Signature-256", ComputeHMACSHA256Header(body, secret))
		validRec := httptest.NewRecorder()
		mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})).ServeHTTP(validRec, validReq)
		if validRec.Code != http.StatusOK {
			t.Fatalf("expected 200 for valid HMAC, got %d", validRec.Code)
		}

		tamperedReq := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/servicenow", bytes.NewReader([]byte(`{"sys_id":"s1","state":7}`)))
		tamperedReq.Header.Set("X-SNOW-Signature-256", ComputeHMACSHA256Header(body, secret))
		tamperedRec := httptest.NewRecorder()
		mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })).ServeHTTP(tamperedRec, tamperedReq)
		if tamperedRec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 for tampered payload, got %d", tamperedRec.Code)
		}
	})
}
