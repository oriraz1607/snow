package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/oriraz/grafana-snow-integration/backend/internal/apierror"
	"github.com/oriraz/grafana-snow-integration/backend/internal/auth"
	"github.com/oriraz/grafana-snow-integration/backend/internal/model"
	"github.com/oriraz/grafana-snow-integration/backend/internal/store"
)

// AlertsHandler handles HTTP query and triage mutations for alerts.
type AlertsHandler struct {
	repo *store.AlertRepository
}

// NewAlertsHandler constructs an AlertsHandler.
func NewAlertsHandler(repo *store.AlertRepository) *AlertsHandler {
	return &AlertsHandler{repo: repo}
}

// List handles GET /api/v1/alerts.
func (h *AlertsHandler) List(w http.ResponseWriter, r *http.Request) {
	filter, err := parseListAlertsFilter(r)
	if err != nil {
		apierror.Write(w, r, err)
		return
	}
	resp, err := h.repo.ListAlerts(r.Context(), filter)
	if err != nil {
		apierror.Write(w, r, err)
		return
	}
	apierror.WriteJSON(w, http.StatusOK, resp)
}

// Get handles GET /api/v1/alerts/{alert_id}.
func (h *AlertsHandler) Get(w http.ResponseWriter, r *http.Request) {
	alertID := chi.URLParam(r, "alert_id")
	if !model.IsValidUUID(alertID) {
		apierror.Write(w, r, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "alert_id must be a valid UUID."))
		return
	}
	detail, err := h.repo.GetAlert(r.Context(), alertID)
	if err != nil {
		apierror.Write(w, r, err)
		return
	}
	apierror.WriteJSON(w, http.StatusOK, detail)
}

// History handles GET /api/v1/alerts/{alert_id}/history.
func (h *AlertsHandler) History(w http.ResponseWriter, r *http.Request) {
	alertID := chi.URLParam(r, "alert_id")
	if !model.IsValidUUID(alertID) {
		apierror.Write(w, r, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "alert_id must be a valid UUID."))
		return
	}
	history, err := h.repo.GetAlertHistory(r.Context(), alertID)
	if err != nil {
		apierror.Write(w, r, err)
		return
	}
	apierror.WriteJSON(w, http.StatusOK, history)
}

// Acknowledge handles POST /api/v1/alerts/{alert_id}/acknowledge.
func (h *AlertsHandler) Acknowledge(w http.ResponseWriter, r *http.Request) {
	alertID := chi.URLParam(r, "alert_id")
	if !model.IsValidUUID(alertID) {
		apierror.Write(w, r, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "alert_id must be a valid UUID."))
		return
	}
	var req model.StateMutationRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		apierror.Write(w, r, err)
		return
	}
	if req.Version < 1 {
		apierror.Write(w, r, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "version must be >= 1."))
		return
	}
	op, _ := auth.OperatorFromContext(r.Context())
	updated, err := h.repo.AcknowledgeAlert(r.Context(), alertID, op, req.Version, req.Comment)
	if err != nil {
		apierror.Write(w, r, err)
		return
	}
	apierror.WriteJSON(w, http.StatusOK, updated)
}

// Unacknowledge handles POST /api/v1/alerts/{alert_id}/unacknowledge.
func (h *AlertsHandler) Unacknowledge(w http.ResponseWriter, r *http.Request) {
	alertID := chi.URLParam(r, "alert_id")
	if !model.IsValidUUID(alertID) {
		apierror.Write(w, r, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "alert_id must be a valid UUID."))
		return
	}
	var req model.StateMutationRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		apierror.Write(w, r, err)
		return
	}
	if req.Version < 1 {
		apierror.Write(w, r, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "version must be >= 1."))
		return
	}
	op, _ := auth.OperatorFromContext(r.Context())
	updated, err := h.repo.UnacknowledgeAlert(r.Context(), alertID, op, req.Version, req.Comment)
	if err != nil {
		apierror.Write(w, r, err)
		return
	}
	apierror.WriteJSON(w, http.StatusOK, updated)
}

// Dismiss handles POST /api/v1/alerts/{alert_id}/dismiss.
func (h *AlertsHandler) Dismiss(w http.ResponseWriter, r *http.Request) {
	alertID := chi.URLParam(r, "alert_id")
	if !model.IsValidUUID(alertID) {
		apierror.Write(w, r, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "alert_id must be a valid UUID."))
		return
	}
	var req model.DismissRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		apierror.Write(w, r, err)
		return
	}
	if req.Version < 1 {
		apierror.Write(w, r, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "version must be >= 1."))
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if len(reason) < 3 || len(reason) > 500 {
		apierror.Write(w, r, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "reason must be between 3 and 500 characters."))
		return
	}
	op, _ := auth.OperatorFromContext(r.Context())
	updated, err := h.repo.DismissAlert(r.Context(), alertID, op, req.Version, reason)
	if err != nil {
		apierror.Write(w, r, err)
		return
	}
	apierror.WriteJSON(w, http.StatusOK, updated)
}

// Reopen handles POST /api/v1/alerts/{alert_id}/reopen.
func (h *AlertsHandler) Reopen(w http.ResponseWriter, r *http.Request) {
	alertID := chi.URLParam(r, "alert_id")
	if !model.IsValidUUID(alertID) {
		apierror.Write(w, r, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "alert_id must be a valid UUID."))
		return
	}
	var req model.StateMutationRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		apierror.Write(w, r, err)
		return
	}
	if req.Version < 1 {
		apierror.Write(w, r, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "version must be >= 1."))
		return
	}
	op, _ := auth.OperatorFromContext(r.Context())
	updated, err := h.repo.ReopenAlert(r.Context(), alertID, op, req.Version, req.Comment)
	if err != nil {
		apierror.Write(w, r, err)
		return
	}
	apierror.WriteJSON(w, http.StatusOK, updated)
}

// AddNote handles POST /api/v1/alerts/{alert_id}/notes.
func (h *AlertsHandler) AddNote(w http.ResponseWriter, r *http.Request) {
	alertID := chi.URLParam(r, "alert_id")
	if !model.IsValidUUID(alertID) {
		apierror.Write(w, r, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "alert_id must be a valid UUID."))
		return
	}
	var req model.AddNoteRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		apierror.Write(w, r, err)
		return
	}
	comment := strings.TrimSpace(req.Comment)
	if len(comment) < 1 || len(comment) > 2000 {
		apierror.Write(w, r, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "comment must be between 1 and 2000 characters."))
		return
	}
	op, _ := auth.OperatorFromContext(r.Context())
	ev, err := h.repo.AddOperatorNote(r.Context(), alertID, op, comment)
	if err != nil {
		apierror.Write(w, r, err)
		return
	}
	apierror.WriteJSON(w, http.StatusCreated, ev)
}

func parseListAlertsFilter(r *http.Request) (model.ListAlertsFilter, error) {
	q := r.URL.Query()
	filter := model.ListAlertsFilter{
		Sort:          strings.TrimSpace(q.Get("sort")),
		Limit:         50,
		Fingerprint:   strings.TrimSpace(q.Get("fingerprint")),
		Search:        strings.TrimSpace(q.Get("search")),
		IncidentDrift: strings.TrimSpace(q.Get("incident_drift")),
	}
	if filter.Sort == "" {
		filter.Sort = model.SortStartsAtDesc
	}
	switch filter.Sort {
	case model.SortStartsAtDesc, model.SortStartsAtAsc, model.SortSeverityDesc, model.SortResolvedAtDesc:
	default:
		return filter, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "Unsupported sort parameter.")
	}

	if filter.IncidentDrift == "" {
		filter.IncidentDrift = "all"
	}
	switch filter.IncidentDrift {
	case "all", string(model.DriftResolvedOpenTicket), string(model.DriftActiveClosedTicket), string(model.DriftNone):
	default:
		return filter, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "Invalid incident_drift filter.")
	}

	for _, raw := range q["state"] {
		for _, part := range strings.Split(raw, ",") {
			st := model.AlertState(strings.ToLower(strings.TrimSpace(part)))
			if st == "" {
				continue
			}
			switch st {
			case model.AlertStateFiring, model.AlertStateAcknowledged, model.AlertStateDismissed, model.AlertStateResolved:
				filter.States = append(filter.States, st)
			default:
				return filter, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "Invalid alert state filter value.")
			}
		}
	}

	for _, raw := range q["severity"] {
		for _, part := range strings.Split(raw, ",") {
			sev := strings.ToLower(strings.TrimSpace(part))
			if sev == "" {
				continue
			}
			switch sev {
			case "critical", "warning", "info", "unknown":
				filter.Severities = append(filter.Severities, sev)
			default:
				return filter, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "Invalid severity filter value.")
			}
		}
	}

	if rawHas := strings.TrimSpace(q.Get("has_incident")); rawHas != "" {
		b, err := strconv.ParseBool(rawHas)
		if err != nil {
			return filter, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "has_incident must be true or false.")
		}
		filter.HasIncident = &b
	}

	if labelVals := q["label"]; len(labelVals) > 0 {
		filter.Labels = make(map[string]string, len(labelVals))
		for _, lv := range labelVals {
			parts := strings.SplitN(lv, "=", 2)
			if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
				return filter, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "label filter must follow key=value format.")
			}
			filter.Labels[strings.TrimSpace(parts[0])] = parts[1]
		}
	}

	if fromStr := strings.TrimSpace(q.Get("from")); fromStr != "" {
		t, err := time.Parse(time.RFC3339, fromStr)
		if err != nil {
			return filter, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "from must be a valid RFC 3339 timestamp.")
		}
		utc := t.UTC()
		filter.From = &utc
	}
	if toStr := strings.TrimSpace(q.Get("to")); toStr != "" {
		t, err := time.Parse(time.RFC3339, toStr)
		if err != nil {
			return filter, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "to must be a valid RFC 3339 timestamp.")
		}
		utc := t.UTC()
		filter.To = &utc
	}

	if limitStr := strings.TrimSpace(q.Get("limit")); limitStr != "" {
		lim, err := strconv.Atoi(limitStr)
		if err != nil || lim < 1 || lim > 200 {
			return filter, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "limit must be an integer between 1 and 200.")
		}
		filter.Limit = lim
	}

	if cursorStr := strings.TrimSpace(q.Get("cursor")); cursorStr != "" {
		cur, err := model.DecodeCursor(cursorStr)
		if err != nil {
			return filter, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "Invalid pagination cursor.")
		}
		if cur.Sort != "" && cur.Sort != filter.Sort {
			return filter, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "Cursor sort order does not match requested sort.")
		}
		filter.Cursor = cur
	}

	return filter, nil
}
