package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync/atomic"

	"github.com/oriraz/grafana-snow-integration/backend/internal/apierror"
	"github.com/oriraz/grafana-snow-integration/backend/internal/model"
	"github.com/oriraz/grafana-snow-integration/backend/internal/service"
)

// WebhooksHandler handles Alertmanager and ServiceNow inbound webhooks.
type WebhooksHandler struct {
	amSvc        *service.AlertmanagerService
	incSvc       *service.IncidentService
	maxAMBytes   int64
	shuttingDown *atomic.Bool
}

// NewWebhooksHandler constructs a WebhooksHandler.
func NewWebhooksHandler(
	amSvc *service.AlertmanagerService,
	incSvc *service.IncidentService,
	maxAMBytes int64,
	shuttingDown *atomic.Bool,
) *WebhooksHandler {
	return &WebhooksHandler{
		amSvc:        amSvc,
		incSvc:       incSvc,
		maxAMBytes:   maxAMBytes,
		shuttingDown: shuttingDown,
	}
}

// Alertmanager handles POST /api/v1/webhooks/alertmanager.
func (h *WebhooksHandler) Alertmanager(w http.ResponseWriter, r *http.Request) {
	if h.shuttingDown != nil && h.shuttingDown.Load() {
		apierror.Write(w, r, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Server is shutting down; please retry."))
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, h.maxAMBytes)
	rawBody, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			apierror.Write(w, r, apierror.New(http.StatusBadRequest, apierror.CodeInvalidRequest, "Alertmanager webhook payload exceeds ALERTMANAGER_MAX_PAYLOAD_BYTES."))
			return
		}
		apierror.Write(w, r, apierror.New(http.StatusBadRequest, apierror.CodeInvalidRequest, "Failed to read webhook body.").WithCause(err))
		return
	}

	resp, err := h.amSvc.Ingest(r.Context(), rawBody)
	if err != nil {
		apierror.Write(w, r, err)
		return
	}
	apierror.WriteJSON(w, http.StatusOK, resp)
}

// ServiceNow handles POST /api/v1/webhooks/servicenow.
func (h *WebhooksHandler) ServiceNow(w http.ResponseWriter, r *http.Request) {
	rawBody, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	if err != nil {
		apierror.Write(w, r, apierror.New(http.StatusBadRequest, apierror.CodeInvalidRequest, "Failed to read webhook body.").WithCause(err))
		return
	}

	var payload model.ServiceNowWebhookPayload
	if err := json.Unmarshal(rawBody, &payload); err != nil {
		apierror.Write(w, r, apierror.New(http.StatusBadRequest, apierror.CodeInvalidRequest, "Malformed ServiceNow webhook JSON payload.").WithCause(err))
		return
	}

	updated, err := h.incSvc.ProcessInboundWebhook(r.Context(), rawBody, payload)
	if err != nil {
		apierror.Write(w, r, err)
		return
	}
	apierror.WriteJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"updated": updated,
	})
}
