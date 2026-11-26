package apierror

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
)

const (
	CodeInvalidRequest             = "INVALID_REQUEST"
	CodeValidationError            = "VALIDATION_ERROR"
	CodeUnauthorized               = "UNAUTHORIZED"
	CodeTokenExpired               = "TOKEN_EXPIRED"
	CodeForbiddenRBAC              = "FORBIDDEN_RBAC"
	CodeAlertNotFound              = "ALERT_NOT_FOUND"
	CodeIncidentNotFound           = "INCIDENT_NOT_FOUND"
	CodeConflictStaleVersion       = "CONFLICT_STALE_VERSION"
	CodeInvalidStateTransition     = "INVALID_STATE_TRANSITION"
	CodeIncidentAlreadyLinked      = "INCIDENT_ALREADY_LINKED"
	CodeIncidentCreationInProgress = "INCIDENT_CREATION_IN_PROGRESS"
	CodeIdempotencyKeyConflict     = "IDEMPOTENCY_KEY_CONFLICT"
	CodeTeamManagedByLabel         = "TEAM_MANAGED_BY_LABEL"
	CodeAlertHasOpenIncident       = "ALERT_HAS_OPEN_INCIDENT"
	CodeSnowValidationRejected     = "SNOW_VALIDATION_REJECTED"
	CodeRateLimited                = "RATE_LIMITED"
	CodeSnowUpstreamError          = "SNOW_UPSTREAM_ERROR"
	CodeDBUnavailable              = "DB_UNAVAILABLE"
	CodeSnowUnavailable            = "SNOW_UNAVAILABLE"
	CodeSnowTimeout                = "SNOW_TIMEOUT"
	CodeInternalError              = "INTERNAL_ERROR"
)

type requestIDKey struct{}

// WithRequestID attaches the request correlation ID to ctx.
func WithRequestID(ctx context.Context, reqID string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, reqID)
}

// RequestIDFromContext returns the request correlation ID from ctx, if any.
func RequestIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(requestIDKey{}).(string)
	return v
}

// APIError is an RFC 7807-inspired structured HTTP error.
type APIError struct {
	Status  int
	Code    string
	Message string
	Details map[string]any
	Cause   error
}

func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	if e.Cause != nil {
		return e.Code + ": " + e.Message + ": " + e.Cause.Error()
	}
	return e.Code + ": " + e.Message
}

func (e *APIError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// New constructs an APIError.
func New(status int, code, message string) *APIError {
	return &APIError{
		Status:  status,
		Code:    code,
		Message: message,
	}
}

// WithDetails attaches structured error context for the response envelope.
func (e *APIError) WithDetails(details map[string]any) *APIError {
	e.Details = details
	return e
}

// WithCause attaches an underlying error for internal logging.
func (e *APIError) WithCause(err error) *APIError {
	e.Cause = err
	return e
}

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	RequestID string         `json:"request_id,omitempty"`
	Details   map[string]any `json:"details,omitempty"`
}

// Write encodes err as a standard API error envelope and writes it to w.
func Write(w http.ResponseWriter, r *http.Request, err error) {
	reqID := RequestIDFromContext(r.Context())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		apiErr = New(http.StatusInternalServerError, CodeInternalError, "An internal server error occurred.")
	}

	WriteJSON(w, apiErr.Status, errorEnvelope{
		Error: errorBody{
			Code:      apiErr.Code,
			Message:   apiErr.Message,
			RequestID: reqID,
			Details:   apiErr.Details,
		},
	})
}

// WriteJSON writes a JSON payload with UTF-8 Content-Type.
func WriteJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
