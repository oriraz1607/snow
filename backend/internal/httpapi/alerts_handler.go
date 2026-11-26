package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/oriraz/grafana-snow-integration/backend/internal/apierror"
	"github.com/oriraz/grafana-snow-integration/backend/internal/auth"
	"github.com/oriraz/grafana-snow-integration/backend/internal/model"
)

// AlertStore defines alert repository dependencies (satisfied by *store.AlertRepository).
type AlertStore interface {
	ListAlerts(ctx context.Context, filter model.ListAlertsFilter) (*model.ListAlertsResponse, error)
	GetAlert(ctx context.Context, alertID string) (*model.AlertDetail, error)
	GetAlertHistory(ctx context.Context, alertID string, limit, offset int) (*model.AlertHistoryResponse, error)
	AcknowledgeAlert(ctx context.Context, alertID string, op auth.OperatorContext, version int, comment string) (*model.Alert, error)
	UnacknowledgeAlert(ctx context.Context, alertID string, op auth.OperatorContext, version int, comment string) (*model.Alert, error)
	DismissAlert(ctx context.Context, alertID string, op auth.OperatorContext, version int, reason string) (*model.Alert, error)
	BulkDismissAlerts(ctx context.Context, alertIDs []string, op auth.OperatorContext, reason string) (*model.BulkDismissResponse, error)
	ReopenAlert(ctx context.Context, alertID string, op auth.OperatorContext, version int, comment string) (*model.Alert, error)
	AddOperatorNote(ctx context.Context, alertID string, op auth.OperatorContext, comment string) (*model.AuditEvent, error)
	TagAlertTeam(ctx context.Context, alertID string, op auth.OperatorContext, team string) (*model.Alert, error)
}

// ConfluenceUpdater validates and persists NOC Confluence documentation links.
type ConfluenceUpdater interface {
	UpdateAlertConfluenceURL(ctx context.Context, alertID string, op auth.OperatorContext, rawURL string) (*model.Alert, error)
}

// AlertsHandler handles HTTP query and triage mutations for alerts.
type AlertsHandler struct {
	repo       AlertStore
	confluence ConfluenceUpdater
}

// NewAlertsHandler constructs an AlertsHandler.
func NewAlertsHandler(repo AlertStore, confluence ...ConfluenceUpdater) *AlertsHandler {
	var conf ConfluenceUpdater
	if len(confluence) > 0 {
		conf = confluence[0]
	}
	return &AlertsHandler{repo: repo, confluence: conf}
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
	limit := 20
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		v, err := strconv.Atoi(rawLimit)
		if err != nil || v < 1 || v > 100 {
			apierror.Write(w, r, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "limit must be an integer between 1 and 100."))
			return
		}
		limit = v
	}
	offset := 0
	if rawOffset := strings.TrimSpace(r.URL.Query().Get("offset")); rawOffset != "" {
		v, err := strconv.Atoi(rawOffset)
		if err != nil || v < 0 {
			apierror.Write(w, r, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "offset must be a non-negative integer."))
			return
		}
		offset = v
	}
	history, err := h.repo.GetAlertHistory(r.Context(), alertID, limit, offset)
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
	apierror.Write(w, r, apierror.New(
		http.StatusConflict,
		apierror.CodeTicketRequiredForAck,
		"Alerts cannot be acknowledged without a ServiceNow incident. Create or link a ServiceNow incident to acknowledge this alert.",
	))
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

// BulkDismiss handles POST /api/v1/alerts/bulk/dismiss.
func (h *AlertsHandler) BulkDismiss(w http.ResponseWriter, r *http.Request) {
	var req model.BulkDismissRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		apierror.Write(w, r, err)
		return
	}
	if len(req.AlertIDs) == 0 {
		apierror.Write(w, r, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "alert_ids must contain at least 1 alert ID."))
		return
	}
	seen := make(map[string]struct{}, len(req.AlertIDs))
	deduped := make([]string, 0, len(req.AlertIDs))
	for _, raw := range req.AlertIDs {
		id := strings.TrimSpace(raw)
		if id == "" || !model.IsValidUUID(id) {
			apierror.Write(w, r, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "Every item in alert_ids must be a valid UUID."))
			return
		}
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			deduped = append(deduped, id)
		}
	}
	if len(deduped) > 100 {
		apierror.Write(w, r, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "Cannot bulk-dismiss more than 100 alerts per request."))
		return
	}

	reason := strings.TrimSpace(req.Reason)
	if len(reason) < 3 || len(reason) > 500 {
		apierror.Write(w, r, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "reason must be between 3 and 500 characters."))
		return
	}

	op, _ := auth.OperatorFromContext(r.Context())
	resp, err := h.repo.BulkDismissAlerts(r.Context(), deduped, op, reason)
	if err != nil {
		apierror.Write(w, r, err)
		return
	}
	apierror.WriteJSON(w, http.StatusOK, resp)
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

// TagTeam handles POST /api/v1/alerts/{alert_id}/team.
func (h *AlertsHandler) TagTeam(w http.ResponseWriter, r *http.Request) {
	alertID := chi.URLParam(r, "alert_id")
	if !model.IsValidUUID(alertID) {
		apierror.Write(w, r, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "alert_id must be a valid UUID."))
		return
	}
	var req model.TagTeamRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		apierror.Write(w, r, err)
		return
	}
	team := strings.TrimSpace(req.Team)
	if len(team) < 1 || len(team) > 128 {
		apierror.Write(w, r, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "team must be between 1 and 128 characters."))
		return
	}
	op, _ := auth.OperatorFromContext(r.Context())
	updated, err := h.repo.TagAlertTeam(r.Context(), alertID, op, team)
	if err != nil {
		apierror.Write(w, r, err)
		return
	}
	apierror.WriteJSON(w, http.StatusOK, updated)
}

// UpdateConfluenceURL handles POST /api/v1/alerts/{alert_id}/confluence.
func (h *AlertsHandler) UpdateConfluenceURL(w http.ResponseWriter, r *http.Request) {
	alertID := chi.URLParam(r, "alert_id")
	if !model.IsValidUUID(alertID) {
		apierror.Write(w, r, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "alert_id must be a valid UUID."))
		return
	}
	var req model.UpdateConfluenceURLRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		apierror.Write(w, r, err)
		return
	}
	if h.confluence == nil {
		apierror.Write(w, r, apierror.New(http.StatusInternalServerError, apierror.CodeInternalError, "Confluence documentation service is not configured."))
		return
	}
	op, _ := auth.OperatorFromContext(r.Context())
	updated, err := h.confluence.UpdateAlertConfluenceURL(r.Context(), alertID, op, req.ConfluenceURL)
	if err != nil {
		apierror.Write(w, r, err)
		return
	}
	apierror.WriteJSON(w, http.StatusOK, updated)
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
		filter.Sort = model.SortSeverityDesc
	}
	switch filter.Sort {
	case model.SortStartsAtDesc,
		model.SortStartsAtAsc,
		model.SortSeverityDesc,
		model.SortSeverityAsc,
		model.SortAlertNameAsc,
		model.SortAlertNameDesc,
		model.SortInstanceAsc,
		model.SortInstanceDesc,
		model.SortResolvedAtDesc:
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
			case model.AlertStateFilterNonActiveAcknowledged:
				filter.NonActiveAcknowledged = true
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
			case "critical", "warning":
				filter.Severities = append(filter.Severities, sev)
			default:
				return filter, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "Invalid severity filter value (only critical and warning are supported).")
			}
		}
	}

	filter.Environments = parseCSVFilterValues(q["env"], q["environment"])
	filter.Sites = parseCSVFilterValues(q["site"])
	filter.Teams = parseCSVFilterValues(q["team"])
	filter.Instances = parseCSVFilterValues(q["instance"])

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

func parseCSVFilterValues(groups ...[]string) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, vals := range groups {
		for _, raw := range vals {
			for _, part := range strings.Split(raw, ",") {
				v := strings.TrimSpace(part)
				if v == "" {
					continue
				}
				if _, ok := seen[v]; !ok {
					seen[v] = struct{}{}
					out = append(out, v)
				}
			}
		}
	}
	return out
}
