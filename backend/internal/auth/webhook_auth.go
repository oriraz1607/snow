package auth

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/oriraz/grafana-snow-integration/backend/internal/apierror"
)

const defaultMaxWebhookBytes int64 = 5 * 1024 * 1024

// AlertmanagerBearerMiddleware verifies constant-time Bearer token auth for Alertmanager webhooks.
func AlertmanagerBearerMiddleware(expectedToken string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if expectedToken == "" {
				apierror.Write(w, r, apierror.New(http.StatusUnauthorized, apierror.CodeUnauthorized, "Webhook authentication is not configured."))
				return
			}

			token, ok := parseBearerToken(strings.TrimSpace(r.Header.Get("Authorization")))
			if !ok || subtle.ConstantTimeCompare([]byte(token), []byte(expectedToken)) != 1 {
				apierror.Write(w, r, apierror.New(http.StatusUnauthorized, apierror.CodeUnauthorized, "Invalid Alertmanager webhook token."))
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// ServiceNowWebhookMiddleware verifies HMAC-SHA256 (X-SNOW-Signature-256: sha256=<hex>)
// or constant-time Bearer token auth against SNOW_INBOUND_WEBHOOK_SECRET.
func ServiceNowWebhookMiddleware(secret string, maxBytes int64) func(http.Handler) http.Handler {
	if maxBytes <= 0 {
		maxBytes = defaultMaxWebhookBytes
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if secret == "" {
				apierror.Write(w, r, apierror.New(http.StatusUnauthorized, apierror.CodeUnauthorized, "ServiceNow inbound webhook secret is not configured."))
				return
			}

			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			rawBody, err := io.ReadAll(r.Body)
			if err != nil {
				var maxErr *http.MaxBytesError
				if errors.As(err, &maxErr) {
					apierror.Write(w, r, apierror.New(http.StatusBadRequest, apierror.CodeInvalidRequest, "Webhook payload exceeds maximum allowed size."))
					return
				}
				apierror.Write(w, r, apierror.New(http.StatusBadRequest, apierror.CodeInvalidRequest, "Unable to read webhook request body."))
				return
			}
			_ = r.Body.Close()

			sigHeader := strings.TrimSpace(r.Header.Get("X-SNOW-Signature-256"))
			if sigHeader != "" {
				if !VerifyHMACSHA256(rawBody, secret, sigHeader) {
					apierror.Write(w, r, apierror.New(http.StatusUnauthorized, apierror.CodeUnauthorized, "Invalid ServiceNow webhook HMAC signature."))
					return
				}
			} else {
				token, ok := parseBearerToken(strings.TrimSpace(r.Header.Get("Authorization")))
				if !ok || subtle.ConstantTimeCompare([]byte(token), []byte(secret)) != 1 {
					apierror.Write(w, r, apierror.New(http.StatusUnauthorized, apierror.CodeUnauthorized, "Missing or invalid ServiceNow webhook credentials."))
					return
				}
			}

			r.Body = io.NopCloser(bytes.NewReader(rawBody))
			next.ServeHTTP(w, r)
		})
	}
}

// VerifyHMACSHA256 validates a "sha256=<hex>" (or raw hex) HMAC-SHA256 header in constant time.
func VerifyHMACSHA256(body []byte, secret, header string) bool {
	trimmed := strings.TrimSpace(header)
	trimmed = strings.TrimPrefix(trimmed, "sha256=")
	sigBytes, err := hex.DecodeString(trimmed)
	if err != nil || len(sigBytes) != sha256.Size {
		return false
	}

	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	expected := mac.Sum(nil)
	return hmac.Equal(sigBytes, expected)
}

// ComputeHMACSHA256Header computes the canonical "sha256=<hex>" header value.
func ComputeHMACSHA256Header(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
