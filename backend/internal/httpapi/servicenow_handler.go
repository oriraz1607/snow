package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/oriraz/grafana-snow-integration/backend/internal/apierror"
	"github.com/oriraz/grafana-snow-integration/backend/internal/auth"
	"github.com/oriraz/grafana-snow-integration/backend/internal/model"
	"github.com/oriraz/grafana-snow-integration/backend/internal/service"
)

// ServiceNowHandler handles operator-facing ServiceNow incident endpoints.
type ServiceNowHandler struct {
	svc *service.IncidentService
}

// NewServiceNowHandler constructs a ServiceNowHandler.
func NewServiceNowHandler(svc *service.IncidentService) *ServiceNowHandler {
	return &ServiceNowHandler{svc: svc}
}

// CreateIncident handles POST /api/v1/alerts/{alert_id}/servicenow/incident.
func (h *ServiceNowHandler) CreateIncident(w http.ResponseWriter, r *http.Request) {
	alertID := chi.URLParam(r, "alert_id")
	if !model.IsValidUUID(alertID) {
		apierror.Write(w, r, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "alert_id must be a valid UUID."))
		return
	}
	var req model.CreateIncidentRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		apierror.Write(w, r, err)
		return
	}
	op, _ := auth.OperatorFromContext(r.Context())
	resp, err := h.svc.CreateIncident(r.Context(), alertID, op, req)
	if err != nil {
		apierror.Write(w, r, err)
		return
	}
	apierror.WriteJSON(w, http.StatusCreated, resp)
}

// CreateBulkIncident handles POST /api/v1/servicenow/incidents/bulk.
func (h *ServiceNowHandler) CreateBulkIncident(w http.ResponseWriter, r *http.Request) {
	var req model.CreateBulkIncidentRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		apierror.Write(w, r, err)
		return
	}
	op, _ := auth.OperatorFromContext(r.Context())
	resp, err := h.svc.CreateBulkIncident(r.Context(), op, req)
	if err != nil {
		apierror.Write(w, r, err)
		return
	}
	apierror.WriteJSON(w, http.StatusCreated, resp)
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
