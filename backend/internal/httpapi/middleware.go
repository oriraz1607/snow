package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/oriraz/grafana-snow-integration/backend/internal/apierror"
	"github.com/oriraz/grafana-snow-integration/backend/internal/auth"
	"github.com/oriraz/grafana-snow-integration/backend/internal/observability"
)

const defaultJSONBodyLimit int64 = 1 << 20 // 1 MiB

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}

func (sr *statusRecorder) Unwrap() http.ResponseWriter {
	return sr.ResponseWriter
}

// SecurityHeadersMiddleware attaches baseline HTTP security headers on API responses.
func SecurityHeadersMiddleware(tlsEnabled bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
			h.Set("Cache-Control", "no-store")
			if tlsEnabled || r.TLS != nil {
				h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
			}
			next.ServeHTTP(w, r)
		})
	}
}

// CORSMiddleware applies a strict origin allowlist and handles CORS preflight requests.
func CORSMiddleware(allowedOrigins []string) func(http.Handler) http.Handler {
	allowed := make(map[string]struct{}, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		trimmed := strings.TrimRight(strings.TrimSpace(origin), "/")
		if trimmed != "" {
			allowed[trimmed] = struct{}{}
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := strings.TrimRight(strings.TrimSpace(r.Header.Get("Origin")), "/")
			if len(allowed) > 0 && origin != "" {
				w.Header().Add("Vary", "Origin")
				_, ok := allowed[origin]
				if ok {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
					w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key, X-Request-Id")
					w.Header().Set("Access-Control-Expose-Headers", "X-Request-Id, Idempotency-Replayed, Retry-After")
					w.Header().Set("Access-Control-Max-Age", "600")
				}
				if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
					if !ok {
						apierror.Write(w, r, apierror.New(http.StatusForbidden, apierror.CodeForbiddenRBAC, "Origin is not allowed by CORS policy."))
						return
					}
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

const (
	rateLimiterMaxBuckets = 10000
	rateLimiterIdleTTL    = 5 * time.Minute
)

type tokenBucket struct {
	tokens   float64
	lastSeen time.Time
}

type ipRateLimiter struct {
	mu          sync.Mutex
	rps         float64
	burst       float64
	trustProxy  bool
	now         func() time.Time
	buckets     map[string]*tokenBucket
	lastCleanup time.Time
}

func newIPRateLimiter(rps float64, burst int, trustProxy bool, nowFn func() time.Time) *ipRateLimiter {
	if rps <= 0 {
		rps = 60
	}
	if burst < 1 {
		burst = 1
	}
	if nowFn == nil {
		nowFn = time.Now
	}
	return &ipRateLimiter{
		rps:         rps,
		burst:       float64(burst),
		trustProxy:  trustProxy,
		now:         nowFn,
		buckets:     make(map[string]*tokenBucket),
		lastCleanup: nowFn(),
	}
}

func (l *ipRateLimiter) allow(key string) (bool, int) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	if now.Sub(l.lastCleanup) >= time.Minute || len(l.buckets) >= rateLimiterMaxBuckets {
		for k, b := range l.buckets {
			if now.Sub(b.lastSeen) > rateLimiterIdleTTL {
				delete(l.buckets, k)
			}
		}
		if len(l.buckets) >= rateLimiterMaxBuckets {
			// Evict oldest seen bucket if still at cap under spoofed flood.
			var oldestKey string
			var oldestTime time.Time
			for k, b := range l.buckets {
				if oldestKey == "" || b.lastSeen.Before(oldestTime) {
					oldestKey = k
					oldestTime = b.lastSeen
				}
			}
			if oldestKey != "" {
				delete(l.buckets, oldestKey)
			}
		}
		l.lastCleanup = now
	}

	b, ok := l.buckets[key]
	if !ok {
		b = &tokenBucket{tokens: l.burst, lastSeen: now}
		l.buckets[key] = b
	} else {
		elapsed := now.Sub(b.lastSeen).Seconds()
		if elapsed > 0 {
			b.tokens = math.Min(l.burst, b.tokens+elapsed*l.rps)
		}
		b.lastSeen = now
	}

	if b.tokens >= 1.0 {
		b.tokens -= 1.0
		return true, 0
	}

	waitSeconds := int(math.Ceil((1.0 - b.tokens) / l.rps))
	if waitSeconds < 1 {
		waitSeconds = 1
	}
	return false, waitSeconds
}

// RateLimitMiddleware enforces a per-client-IP token-bucket rate limit returning 429 RATE_LIMITED.
func RateLimitMiddleware(rps float64, burst int, trustProxy bool) func(http.Handler) http.Handler {
	limiter := newIPRateLimiter(rps, burst, trustProxy, time.Now)
	return rateLimitWithLimiter(limiter)
}

func rateLimitWithLimiter(limiter *ipRateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := clientIPFromRequest(r, limiter.trustProxy)
			if allowed, retryAfter := limiter.allow(key); !allowed {
				w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
				apierror.Write(w, r, apierror.New(http.StatusTooManyRequests, apierror.CodeRateLimited, "Rate limit exceeded. Please retry later."))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func clientIPFromRequest(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" {
			first, _, _ := strings.Cut(xff, ",")
			if trimmed := strings.TrimSpace(first); trimmed != "" {
				return trimmed
			}
		}
		if xri := strings.TrimSpace(r.Header.Get("X-Real-IP")); xri != "" {
			return xri
		}
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil && host != "" {
		return host
	}
	if addr := strings.TrimSpace(r.RemoteAddr); addr != "" {
		return addr
	}
	return "unknown"
}

// RequestIDMiddleware accepts or generates X-Request-Id and echoes it in response headers.
func RequestIDMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqID := strings.TrimSpace(r.Header.Get("X-Request-Id"))
			if reqID == "" || len(reqID) > 128 {
				reqID = newRequestID()
			}
			w.Header().Set("X-Request-Id", reqID)
			ctx := apierror.WithRequestID(r.Context(), reqID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RecoverMiddleware catches panics and responds with a structured 500 JSON error.
func RecoverMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					if logger != nil {
						logger.Error("panic recovered in HTTP handler",
							slog.String("component", "http_server"),
							slog.String("request_id", apierror.RequestIDFromContext(r.Context())),
							slog.Any("panic", rec),
							slog.String("stack", string(debug.Stack())),
						)
					}
					apierror.Write(w, r, apierror.New(http.StatusInternalServerError, apierror.CodeInternalError, "An internal server error occurred."))
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// AccessLogAndMetricsMiddleware emits structured JSON access logs and Prometheus HTTP metrics.
func AccessLogAndMetricsMiddleware(logger *slog.Logger, metrics *observability.Metrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(rec, r)

			duration := time.Since(started)
			routePattern := r.URL.Path
			if rctx := chi.RouteContext(r.Context()); rctx != nil && rctx.RoutePattern() != "" {
				routePattern = rctx.RoutePattern()
			}

			if metrics != nil {
				metrics.ObserveHTTP(r.Method, routePattern, rec.status, duration.Seconds())
			}

			if logger != nil {
				attrs := []any{
					slog.String("component", "http_access"),
					slog.String("request_id", apierror.RequestIDFromContext(r.Context())),
					slog.String("method", r.Method),
					slog.String("route", routePattern),
					slog.Int("status", rec.status),
					slog.Int64("duration_ms", duration.Milliseconds()),
				}
				if op, ok := auth.OperatorFromContext(r.Context()); ok && op.Login != "" {
					attrs = append(attrs, slog.String("actor_login", op.Login))
				}
				if alertID := chi.URLParam(r, "alert_id"); alertID != "" {
					attrs = append(attrs, slog.String("alert_id", alertID))
				}
				logger.Info("http request completed", attrs...)
			}
		})
	}
}

func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, defaultJSONBodyLimit)
	defer r.Body.Close()

	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return apierror.New(http.StatusBadRequest, apierror.CodeInvalidRequest, "Request body exceeds size limit.")
		}
		return apierror.New(http.StatusBadRequest, apierror.CodeInvalidRequest, "Unable to read request body.").WithCause(err)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return apierror.New(http.StatusBadRequest, apierror.CodeInvalidRequest, "Request body cannot be empty.")
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return apierror.New(http.StatusBadRequest, apierror.CodeInvalidRequest, "Malformed JSON request body.").WithCause(err)
	}
	return nil
}

func newRequestID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "req_fallback"
	}
	return "req_" + hex.EncodeToString(b[:])
}
