package httpapi

import (
	"context"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/oriraz/grafana-snow-integration/backend/internal/apierror"
	"github.com/oriraz/grafana-snow-integration/backend/internal/auth"
	"github.com/oriraz/grafana-snow-integration/backend/internal/model"
)

var idempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// IncidentService defines incident handler dependencies (satisfied by *service.IncidentService).
type IncidentService interface {
	CreateIncident(ctx context.Context, alertID string, op auth.OperatorContext, req model.CreateIncidentRequest, idempotencyKey string) (*model.CreateIncidentResponse, error)
	CreateBulkIncident(ctx context.Context, op auth.OperatorContext, req model.CreateBulkIncidentRequest, idempotencyKey string) (*model.CreateBulkIncidentResponse, error)
	LinkIncident(ctx context.Context, alertID string, op auth.OperatorContext, req model.LinkIncidentRequest) (*model.IncidentSyncResponse, error)
	SyncIncident(ctx context.Context, alertID string, op auth.OperatorContext) (*model.IncidentSyncResponse, error)
	PostWorkNote(ctx context.Context, alertID string, op auth.OperatorContext, req model.WorkNoteRequest) (*model.AuditEvent, error)
	GetMetadata(ctx context.Context) model.ServiceNowMetadataResponse
	GetIncidentActiveStatus(ctx context.Context, snowSysID string) (*model.IncidentActiveStatusResponse, error)
}

// ServiceNowHandler handles operator-facing ServiceNow incident endpoints.
type ServiceNowHandler struct {
	svc IncidentService
}

// NewServiceNowHandler constructs a ServiceNowHandler.
func NewServiceNowHandler(svc IncidentService) *ServiceNowHandler {
	return &ServiceNowHandler{svc: svc}
}

// CreateIncident handles POST /api/v1/alerts/{alert_id}/servicenow/incident.
func (h *ServiceNowHandler) CreateIncident(w http.ResponseWriter, r *http.Request) {
	alertID := chi.URLParam(r, "alert_id")
	if !model.IsValidUUID(alertID) {
		apierror.Write(w, r, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "alert_id must be a valid UUID."))
		return
	}
	idemKey, err := extractIdempotencyKey(r)
	if err != nil {
		apierror.Write(w, r, err)
		return
	}
	var req model.CreateIncidentRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		apierror.Write(w, r, err)
		return
	}
	op, _ := auth.OperatorFromContext(r.Context())
	resp, err := h.svc.CreateIncident(r.Context(), alertID, op, req, idemKey)
	if err != nil {
		apierror.Write(w, r, err)
		return
	}
	if resp.Replayed {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	apierror.WriteJSON(w, http.StatusCreated, resp)
}

// CreateBulkIncident handles POST /api/v1/servicenow/incidents/bulk.
func (h *ServiceNowHandler) CreateBulkIncident(w http.ResponseWriter, r *http.Request) {
	idemKey, err := extractIdempotencyKey(r)
	if err != nil {
		apierror.Write(w, r, err)
		return
	}
	var req model.CreateBulkIncidentRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		apierror.Write(w, r, err)
		return
	}
	op, _ := auth.OperatorFromContext(r.Context())
	resp, err := h.svc.CreateBulkIncident(r.Context(), op, req, idemKey)
	if err != nil {
		apierror.Write(w, r, err)
		return
	}
	if resp.Replayed {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	apierror.WriteJSON(w, http.StatusCreated, resp)
}

func extractIdempotencyKey(r *http.Request) (string, error) {
	rawVals := r.Header.Values("Idempotency-Key")
	if len(rawVals) == 0 {
		return "", nil
	}
	key := strings.TrimSpace(rawVals[0])
	if key == "" || !idempotencyKeyPattern.MatchString(key) {
		return "", apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "Idempotency-Key must be 1-128 characters matching [A-Za-z0-9._:-].")
	}
	return key, nil
}

// LinkIncident handles POST /api/v1/alerts/{alert_id}/servicenow/link.
func (h *ServiceNowHandler) LinkIncident(w http.ResponseWriter, r *http.Request) {
	alertID := chi.URLParam(r, "alert_id")
	if !model.IsValidUUID(alertID) {
		apierror.Write(w, r, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "alert_id must be a valid UUID."))
		return
	}
	var req model.LinkIncidentRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		apierror.Write(w, r, err)
		return
	}
	op, _ := auth.OperatorFromContext(r.Context())
	resp, err := h.svc.LinkIncident(r.Context(), alertID, op, req)
	if err != nil {
		apierror.Write(w, r, err)
		return
	}
	apierror.WriteJSON(w, http.StatusOK, resp)
}

// SyncIncident handles POST /api/v1/alerts/{alert_id}/servicenow/sync.
func (h *ServiceNowHandler) SyncIncident(w http.ResponseWriter, r *http.Request) {
	alertID := chi.URLParam(r, "alert_id")
	if !model.IsValidUUID(alertID) {
		apierror.Write(w, r, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "alert_id must be a valid UUID."))
		return
	}
	op, _ := auth.OperatorFromContext(r.Context())
	resp, err := h.svc.SyncIncident(r.Context(), alertID, op)
	if err != nil {
		apierror.Write(w, r, err)
		return
	}
	apierror.WriteJSON(w, http.StatusOK, resp)
}

// PostWorkNote handles POST /api/v1/alerts/{alert_id}/servicenow/work-notes.
func (h *ServiceNowHandler) PostWorkNote(w http.ResponseWriter, r *http.Request) {
	alertID := chi.URLParam(r, "alert_id")
	if !model.IsValidUUID(alertID) {
		apierror.Write(w, r, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "alert_id must be a valid UUID."))
		return
	}
	var req model.WorkNoteRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		apierror.Write(w, r, err)
		return
	}
	op, _ := auth.OperatorFromContext(r.Context())
	ev, err := h.svc.PostWorkNote(r.Context(), alertID, op, req)
	if err != nil {
		apierror.Write(w, r, err)
		return
	}
	apierror.WriteJSON(w, http.StatusOK, map[string]any{
		"alert_id":    alertID,
		"audit_event": ev,
	})
}

// Metadata handles GET /api/v1/servicenow/metadata.
func (h *ServiceNowHandler) Metadata(w http.ResponseWriter, r *http.Request) {
	resp := h.svc.GetMetadata(r.Context())
	apierror.WriteJSON(w, http.StatusOK, resp)
}

// IncidentActiveStatus handles GET /api/v1/integrations/servicenow/incidents/{snow_sys_id}/active-status.
func (h *ServiceNowHandler) IncidentActiveStatus(w http.ResponseWriter, r *http.Request) {
	snowSysID := chi.URLParam(r, "snow_sys_id")
	resp, err := h.svc.GetIncidentActiveStatus(r.Context(), snowSysID)
	if err != nil {
		apierror.Write(w, r, err)
		return
	}
	apierror.WriteJSON(w, http.StatusOK, resp)
}
