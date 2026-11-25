package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
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
