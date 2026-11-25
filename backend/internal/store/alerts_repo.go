package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/oriraz/grafana-snow-integration/backend/internal/apierror"
	"github.com/oriraz/grafana-snow-integration/backend/internal/auth"
	"github.com/oriraz/grafana-snow-integration/backend/internal/model"
)

// AlertRepository manages PostgreSQL persistence for alert episodes, triage actions, and audit logs.
type AlertRepository struct {
	pool *pgxpool.Pool
}

// NewAlertRepository constructs an AlertRepository.
func NewAlertRepository(pool *pgxpool.Pool) *AlertRepository {
	return &AlertRepository{pool: pool}
}

// ListAlerts queries alert episodes with filters, keyset pagination, and live queue counters.
func (r *AlertRepository) ListAlerts(ctx context.Context, filter model.ListAlertsFilter) (*model.ListAlertsResponse, error) {
	summary, err := r.getSummaryCounts(ctx)
	if err != nil {
		return nil, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Unable to query alert summary counts.").WithCause(err)
	}

	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	sortOrder := filter.Sort
	if sortOrder == "" {
		sortOrder = model.SortStartsAtDesc
	}

	where := make([]string, 0, 10)
	args := make([]any, 0, 12)
	addArg := func(val any) string {
		args = append(args, val)
		return fmt.Sprintf("$%d", len(args))
	}

	if len(filter.States) > 0 {
		stateStrings := make([]string, len(filter.States))
		for i, st := range filter.States {
			stateStrings[i] = string(st)
		}
		ph := addArg(stateStrings)
		where = append(where, fmt.Sprintf("a.state = ANY(%s::alert_state[])", ph))
	}

	if len(filter.Severities) > 0 {
		sevs := make([]string, len(filter.Severities))
		for i, s := range filter.Severities {
			sevs[i] = strings.ToLower(strings.TrimSpace(s))
		}
		ph := addArg(sevs)
		where = append(where, fmt.Sprintf("LOWER(a.severity) = ANY(%s::text[])", ph))
	}

	switch filter.IncidentDrift {
	case string(model.DriftResolvedOpenTicket):
		where = append(where, "a.state = 'resolved' AND s.sync_status IN ('created', 'linked') AND s.snow_state IN (1, 2, 3)")
	case string(model.DriftActiveClosedTicket):
		where = append(where, "a.state IN ('firing', 'acknowledged') AND s.sync_status IN ('created', 'linked') AND s.snow_state IN (6, 7, 8)")
	case string(model.DriftNone):
		where = append(where, "NOT (a.state = 'resolved' AND s.sync_status IN ('created', 'linked') AND s.snow_state IN (1, 2, 3))")
		where = append(where, "NOT (a.state IN ('firing', 'acknowledged') AND s.sync_status IN ('created', 'linked') AND s.snow_state IN (6, 7, 8))")
	}

	if filter.HasIncident != nil {
		if *filter.HasIncident {
			where = append(where, "s.sync_status IN ('created', 'linked')")
		} else {
			where = append(where, "(s.id IS NULL OR s.sync_status NOT IN ('created', 'linked'))")
		}
	}

	if filter.Fingerprint != "" {
		ph := addArg(filter.Fingerprint)
		where = append(where, fmt.Sprintf("a.fingerprint = %s", ph))
	}

	if len(filter.Labels) > 0 {
		keys := make([]string, 0, len(filter.Labels))
		for k := range filter.Labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			pairJSON, err := json.Marshal(map[string]string{k: filter.Labels[k]})
			if err != nil {
				return nil, apierror.New(http.StatusBadRequest, apierror.CodeInvalidRequest, "Invalid label filter.")
			}
			ph := addArg(string(pairJSON))
			where = append(where, fmt.Sprintf("a.labels @> %s::jsonb", ph))
		}
	}

	if filter.Search != "" {
		pattern := "%" + escapeLikePattern(filter.Search) + "%"
		ph := addArg(pattern)
		where = append(where, fmt.Sprintf("(a.alert_name ILIKE %s ESCAPE '\\' OR COALESCE(a.annotations->>'summary', '') ILIKE %s ESCAPE '\\' OR a.fingerprint ILIKE %s ESCAPE '\\' OR COALESCE(s.incident_number, '') ILIKE %s ESCAPE '\\')", ph, ph, ph, ph))
	}

	if filter.From != nil {
		ph := addArg(*filter.From)
		where = append(where, fmt.Sprintf("a.starts_at >= %s", ph))
	}
	if filter.To != nil {
		ph := addArg(*filter.To)
		where = append(where, fmt.Sprintf("a.starts_at <= %s", ph))
	}

	const severityRankExpr = `CASE LOWER(a.severity) WHEN 'critical' THEN 1 WHEN 'warning' THEN 2 WHEN 'info' THEN 3 ELSE 4 END`

	if filter.Cursor != nil {
		c := filter.Cursor
		switch sortOrder {
		case model.SortStartsAtDesc:
			if c.StartsAt == nil {
				return nil, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "Invalid pagination cursor.")
			}
			pTS := addArg(*c.StartsAt)
			pID := addArg(c.ID)
			where = append(where, fmt.Sprintf("(a.starts_at, a.id) < (%s, %s::uuid)", pTS, pID))
		case model.SortStartsAtAsc:
			if c.StartsAt == nil {
				return nil, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "Invalid pagination cursor.")
			}
			pTS := addArg(*c.StartsAt)
			pID := addArg(c.ID)
			where = append(where, fmt.Sprintf("(a.starts_at, a.id) > (%s, %s::uuid)", pTS, pID))
		case model.SortSeverityDesc:
			if c.SeverityRank == nil || c.StartsAt == nil {
				return nil, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "Invalid pagination cursor.")
			}
			pRank := addArg(*c.SeverityRank)
			pTS := addArg(*c.StartsAt)
			pID := addArg(c.ID)
			where = append(where, fmt.Sprintf("(%s > %s OR (%s = %s AND (a.starts_at < %s OR (a.starts_at = %s AND a.id < %s::uuid))))",
				severityRankExpr, pRank, severityRankExpr, pRank, pTS, pTS, pID))
		case model.SortResolvedAtDesc:
			pID := addArg(c.ID)
			if c.ResolvedNull || c.ResolvedAt == nil {
				where = append(where, fmt.Sprintf("(a.resolved_at IS NULL AND a.id < %s::uuid)", pID))
			} else {
				pRes := addArg(*c.ResolvedAt)
				where = append(where, fmt.Sprintf("(a.resolved_at IS NULL OR a.resolved_at < %s OR (a.resolved_at = %s AND a.id < %s::uuid))", pRes, pRes, pID))
			}
		}
	}

	var orderClause string
	switch sortOrder {
	case model.SortStartsAtAsc:
		orderClause = "a.starts_at ASC, a.id ASC"
	case model.SortSeverityDesc:
		orderClause = severityRankExpr + " ASC, a.starts_at DESC, a.id DESC"
	case model.SortResolvedAtDesc:
		orderClause = "a.resolved_at DESC NULLS LAST, a.id DESC"
	default:
		sortOrder = model.SortStartsAtDesc
		orderClause = "a.starts_at DESC, a.id DESC"
	}

	whereSQL := ""
	if len(where) > 0 {
		whereSQL = "WHERE " + strings.Join(where, " AND ")
	}

	limitPH := addArg(limit + 1)

	query := fmt.Sprintf(`
		SELECT
			a.id::text, a.fingerprint, a.starts_at, a.ends_at, a.state::text, a.version,
			a.alert_name, a.severity, a.labels, a.annotations, a.generator_url,
			a.acknowledged_by, a.acknowledged_by_email, a.acknowledged_at,
			a.dismissed_by, a.dismissed_at, a.dismiss_reason, a.resolved_at,
			(SELECT COUNT(*) FROM alerts prev WHERE prev.fingerprint = a.fingerprint AND prev.starts_at < a.starts_at) AS previous_episode_count,
			s.id::text, s.sync_status::text, s.snow_sys_id, s.incident_number, s.incident_url,
			s.snow_state, s.snow_state_label, s.priority, s.assignment_group,
			s.caller_mode::text, s.caller_resolution_status::text, s.caller_sys_id,
			s.caller_fallback_reason, s.snow_state_synced_at, s.resolution_note_posted_at, s.last_error
		FROM alerts a
		LEFT JOIN servicenow_incidents s ON s.alert_id = a.id
		%s
		ORDER BY %s
		LIMIT %s
	`, whereSQL, orderClause, limitPH)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to query alerts.").WithCause(err)
	}
	defer rows.Close()

	items := make([]model.Alert, 0, limit)
	for rows.Next() {
		item, err := scanAlertItemRow(rows)
		if err != nil {
			return nil, apierror.New(http.StatusInternalServerError, apierror.CodeInternalError, "Failed to scan alert row.").WithCause(err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to iterate alert rows.").WithCause(err)
	}

	var nextCursor *string
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		rank := model.SeverityRank(last.Severity)
		startsAt := last.StartsAt
		cursor := model.PageCursor{
			Sort:         sortOrder,
			StartsAt:     &startsAt,
			ResolvedAt:   last.ResolvedAt,
			ResolvedNull: last.ResolvedAt == nil,
			SeverityRank: &rank,
			ID:           last.ID,
		}
		if encoded, err := model.EncodeCursor(cursor); err == nil {
			nextCursor = &encoded
		}
	}

	return &model.ListAlertsResponse{
		Summary:    summary,
		Items:      items,
		NextCursor: nextCursor,
	}, nil
}

func (r *AlertRepository) getSummaryCounts(ctx context.Context) (model.SummaryCounts, error) {
	var s model.SummaryCounts
	err := r.pool.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE a.state = 'firing') AS firing,
			COUNT(*) FILTER (WHERE a.state = 'acknowledged') AS acknowledged,
			COUNT(*) FILTER (WHERE a.state = 'acknowledged' AND (s.id IS NULL OR s.sync_status NOT IN ('created', 'linked'))) AS acknowledged_without_incident,
			COUNT(*) FILTER (WHERE a.state = 'dismissed') AS dismissed,
			COUNT(*) FILTER (WHERE a.state = 'resolved' AND s.sync_status IN ('created', 'linked') AND s.snow_state IN (1, 2, 3)) AS resolved_open_ticket
		FROM alerts a
		LEFT JOIN servicenow_incidents s ON s.alert_id = a.id
	`).Scan(&s.Firing, &s.Acknowledged, &s.AcknowledgedWithoutIncident, &s.Dismissed, &s.ResolvedOpenTicket)
	return s, err
}

// GetAlert fetches a single alert episode with linked incident and recent 20 audit events.
func (r *AlertRepository) GetAlert(ctx context.Context, alertID string) (*model.AlertDetail, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT
			a.id::text, a.fingerprint, a.starts_at, a.ends_at, a.state::text, a.version,
			a.alert_name, a.severity, a.group_key, a.receiver, a.labels, a.annotations,
			a.generator_url, a.external_url,
			a.acknowledged_by, a.acknowledged_by_email, a.acknowledged_at,
			a.dismissed_by, a.dismissed_at, a.dismiss_reason, a.resolved_at,
			a.delivery_count, a.first_received_at, a.last_received_at,
			(SELECT COUNT(*) FROM alerts prev WHERE prev.fingerprint = a.fingerprint AND prev.starts_at < a.starts_at) AS previous_episode_count,
			s.id::text, s.sync_status::text, s.snow_sys_id, s.incident_number, s.incident_url,
			s.snow_state, s.snow_state_label, s.priority, s.assignment_group,
			s.caller_mode::text, s.caller_resolution_status::text, s.caller_sys_id,
			s.caller_fallback_reason, s.snow_state_synced_at, s.resolution_note_posted_at, s.last_error
		FROM alerts a
		LEFT JOIN servicenow_incidents s ON s.alert_id = a.id
		WHERE a.id = $1::uuid
	`, alertID)

	detail, err := scanAlertDetailRow(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apierror.New(http.StatusNotFound, apierror.CodeAlertNotFound, "Alert episode not found.")
		}
		return nil, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to load alert episode.").WithCause(err)
	}

	events, err := r.listAuditEvents(ctx, alertID, 20, true)
	if err != nil {
		return nil, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to load alert audit events.").WithCause(err)
	}
	detail.RecentAuditEvents = events
	return detail, nil
}

// GetAlertHistory returns the full audit timeline and previous episodes for alertID.
func (r *AlertRepository) GetAlertHistory(ctx context.Context, alertID string) (*model.AlertHistoryResponse, error) {
	var fingerprint string
	err := r.pool.QueryRow(ctx, `SELECT fingerprint FROM alerts WHERE id = $1::uuid`, alertID).Scan(&fingerprint)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apierror.New(http.StatusNotFound, apierror.CodeAlertNotFound, "Alert episode not found.")
		}
		return nil, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to query alert.").WithCause(err)
	}

	events, err := r.listAuditEvents(ctx, alertID, 0, false)
	if err != nil {
		return nil, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to load audit history.").WithCause(err)
	}

	prevRows, err := r.pool.Query(ctx, `
		SELECT a.id::text, a.starts_at, a.resolved_at, a.state::text, s.incident_number, s.snow_state_label
		FROM alerts a
		LEFT JOIN servicenow_incidents s ON s.alert_id = a.id
		WHERE a.fingerprint = $1 AND a.id <> $2::uuid
		ORDER BY a.starts_at DESC, a.id DESC
	`, fingerprint, alertID)
	if err != nil {
		return nil, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to query previous episodes.").WithCause(err)
	}
	defer prevRows.Close()

	prev := make([]model.PreviousEpisodeSummary, 0)
	for prevRows.Next() {
		var p model.PreviousEpisodeSummary
		var st string
		if err := prevRows.Scan(&p.ID, &p.StartsAt, &p.ResolvedAt, &st, &p.IncidentNumber, &p.SnowStateLabel); err != nil {
			return nil, apierror.New(http.StatusInternalServerError, apierror.CodeInternalError, "Failed to scan previous episode.").WithCause(err)
		}
		p.State = model.AlertState(st)
		prev = append(prev, p)
	}
	if err := prevRows.Err(); err != nil {
		return nil, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to iterate previous episodes.").WithCause(err)
	}

	return &model.AlertHistoryResponse{
		AlertID:          alertID,
		Fingerprint:      fingerprint,
		AuditEvents:      events,
		PreviousEpisodes: prev,
	}, nil
}

// AcknowledgeAlert transitions firing -> acknowledged under row lock and optimistic version check.
func (r *AlertRepository) AcknowledgeAlert(ctx context.Context, alertID string, op auth.OperatorContext, version int, comment string) (*model.Alert, error) {
	var result *model.Alert
	err := r.withTx(ctx, func(tx pgx.Tx) error {
		locked, err := lockAlertForTriage(ctx, tx, alertID)
		if err != nil {
			return err
		}

		// Idempotent retry by the same operator immediately following acknowledgement.
		if locked.State == model.AlertStateAcknowledged &&
			locked.AcknowledgedBy != nil && *locked.AcknowledgedBy == op.Login &&
			locked.Version == version+1 {
			result, err = getAlertItemTx(ctx, tx, alertID)
			return err
		}

		if !model.CanAcknowledge(locked.State) {
			return conflictSnapshotError(apierror.CodeInvalidStateTransition, fmt.Sprintf("Cannot acknowledge alert in %q state.", locked.State), locked)
		}
		if locked.Version != version {
			return conflictSnapshotError(apierror.CodeConflictStaleVersion, fmt.Sprintf("Alert state has changed since version %d; refresh and retry.", version), locked)
		}

		if _, err := tx.Exec(ctx, `
			UPDATE alerts
			SET state = 'acknowledged',
				version = version + 1,
				acknowledged_by = $2,
				acknowledged_by_email = $3,
				acknowledged_at = NOW()
			WHERE id = $1::uuid
		`, alertID, op.Login, model.StringPtrOrNil(op.Email)); err != nil {
			return apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to acknowledge alert.").WithCause(err)
		}

		prev := model.AlertStateFiring
		next := model.AlertStateAcknowledged
		if err := insertAuditLogTx(ctx, tx, auditEntry{
			AlertID:       alertID,
			EventType:     model.AuditAlertAcknowledged,
			ActorType:     model.ActorOperator,
			ActorLogin:    op.Login,
			ActorEmail:    model.StringPtrOrNil(op.Email),
			ActorRole:     model.StringPtrOrNil(op.Role),
			PreviousState: &prev,
			NewState:      &next,
			Comment:       model.StringPtrOrNil(comment),
		}); err != nil {
			return apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to record audit log.").WithCause(err)
		}

		result, err = getAlertItemTx(ctx, tx, alertID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// UnacknowledgeAlert transitions acknowledged -> firing under row lock and optimistic version check.
func (r *AlertRepository) UnacknowledgeAlert(ctx context.Context, alertID string, op auth.OperatorContext, version int, comment string) (*model.Alert, error) {
	var result *model.Alert
	err := r.withTx(ctx, func(tx pgx.Tx) error {
		locked, err := lockAlertForTriage(ctx, tx, alertID)
		if err != nil {
			return err
		}

		if !model.CanUnacknowledge(locked.State) {
			return conflictSnapshotError(apierror.CodeInvalidStateTransition, fmt.Sprintf("Cannot unacknowledge alert in %q state.", locked.State), locked)
		}
		if locked.Version != version {
			return conflictSnapshotError(apierror.CodeConflictStaleVersion, fmt.Sprintf("Alert state has changed since version %d; refresh and retry.", version), locked)
		}

		if _, err := tx.Exec(ctx, `
			UPDATE alerts
			SET state = 'firing',
				version = version + 1,
				acknowledged_by = NULL,
				acknowledged_by_email = NULL,
				acknowledged_at = NULL
			WHERE id = $1::uuid
		`, alertID); err != nil {
			return apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to unacknowledge alert.").WithCause(err)
		}

		prev := model.AlertStateAcknowledged
		next := model.AlertStateFiring
		if err := insertAuditLogTx(ctx, tx, auditEntry{
			AlertID:       alertID,
			EventType:     model.AuditAlertUnacknowledged,
			ActorType:     model.ActorOperator,
			ActorLogin:    op.Login,
			ActorEmail:    model.StringPtrOrNil(op.Email),
			ActorRole:     model.StringPtrOrNil(op.Role),
			PreviousState: &prev,
			NewState:      &next,
			Comment:       model.StringPtrOrNil(comment),
		}); err != nil {
			return apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to record audit log.").WithCause(err)
		}

		result, err = getAlertItemTx(ctx, tx, alertID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// DismissAlert transitions firing/acknowledged -> dismissed under row lock and optimistic version check.
func (r *AlertRepository) DismissAlert(ctx context.Context, alertID string, op auth.OperatorContext, version int, reason string) (*model.Alert, error) {
	var result *model.Alert
	err := r.withTx(ctx, func(tx pgx.Tx) error {
		locked, err := lockAlertForTriage(ctx, tx, alertID)
		if err != nil {
			return err
		}

		if !model.CanDismiss(locked.State) {
			return conflictSnapshotError(apierror.CodeInvalidStateTransition, fmt.Sprintf("Cannot dismiss alert in %q state.", locked.State), locked)
		}
		if locked.Version != version {
			return conflictSnapshotError(apierror.CodeConflictStaleVersion, fmt.Sprintf("Alert state has changed since version %d; refresh and retry.", version), locked)
		}

		if _, err := tx.Exec(ctx, `
			UPDATE alerts
			SET state = 'dismissed',
				version = version + 1,
				dismissed_by = $2,
				dismissed_by_email = $3,
				dismissed_at = NOW(),
				dismiss_reason = $4
			WHERE id = $1::uuid
		`, alertID, op.Login, model.StringPtrOrNil(op.Email), reason); err != nil {
			return apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to dismiss alert.").WithCause(err)
		}

		prev := locked.State
		next := model.AlertStateDismissed
		if err := insertAuditLogTx(ctx, tx, auditEntry{
			AlertID:       alertID,
			EventType:     model.AuditAlertDismissed,
			ActorType:     model.ActorOperator,
			ActorLogin:    op.Login,
			ActorEmail:    model.StringPtrOrNil(op.Email),
			ActorRole:     model.StringPtrOrNil(op.Role),
			PreviousState: &prev,
			NewState:      &next,
			Comment:       &reason,
		}); err != nil {
			return apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to record audit log.").WithCause(err)
		}

		result, err = getAlertItemTx(ctx, tx, alertID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// ReopenAlert transitions dismissed -> firing under row lock and optimistic version check.
func (r *AlertRepository) ReopenAlert(ctx context.Context, alertID string, op auth.OperatorContext, version int, comment string) (*model.Alert, error) {
	var result *model.Alert
	err := r.withTx(ctx, func(tx pgx.Tx) error {
		locked, err := lockAlertForTriage(ctx, tx, alertID)
		if err != nil {
			return err
		}

		if !model.CanReopen(locked.State) {
			return conflictSnapshotError(apierror.CodeInvalidStateTransition, fmt.Sprintf("Cannot reopen alert in %q state.", locked.State), locked)
		}
		if locked.Version != version {
			return conflictSnapshotError(apierror.CodeConflictStaleVersion, fmt.Sprintf("Alert state has changed since version %d; refresh and retry.", version), locked)
		}

		if _, err := tx.Exec(ctx, `
			UPDATE alerts
			SET state = 'firing',
				version = version + 1,
				dismissed_by = NULL,
				dismissed_by_email = NULL,
				dismissed_at = NULL,
				dismiss_reason = NULL
			WHERE id = $1::uuid
		`, alertID); err != nil {
			return apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to reopen alert.").WithCause(err)
		}

		prev := model.AlertStateDismissed
		next := model.AlertStateFiring
		if err := insertAuditLogTx(ctx, tx, auditEntry{
			AlertID:       alertID,
			EventType:     model.AuditAlertReopened,
			ActorType:     model.ActorOperator,
			ActorLogin:    op.Login,
			ActorEmail:    model.StringPtrOrNil(op.Email),
			ActorRole:     model.StringPtrOrNil(op.Role),
			PreviousState: &prev,
			NewState:      &next,
			Comment:       model.StringPtrOrNil(comment),
		}); err != nil {
			return apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to record audit log.").WithCause(err)
		}

		result, err = getAlertItemTx(ctx, tx, alertID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// AddOperatorNote appends an audit comment to alert_audit_log without changing alert state.
func (r *AlertRepository) AddOperatorNote(ctx context.Context, alertID string, op auth.OperatorContext, comment string) (*model.AuditEvent, error) {
	var created model.AuditEvent
	err := r.withTx(ctx, func(tx pgx.Tx) error {
		var currentStateStr string
		err := tx.QueryRow(ctx, `SELECT state::text FROM alerts WHERE id = $1::uuid`, alertID).Scan(&currentStateStr)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return apierror.New(http.StatusNotFound, apierror.CodeAlertNotFound, "Alert episode not found.")
			}
			return apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to query alert.").WithCause(err)
		}

		st := model.AlertState(currentStateStr)
		var id int64
		var createdAt time.Time
		if err := tx.QueryRow(ctx, `
			INSERT INTO alert_audit_log (
				alert_id, event_type, actor_type, actor_login, actor_email, actor_role,
				previous_state, new_state, comment, metadata
			)
			VALUES ($1::uuid, $2::audit_event_type, $3::actor_type, $4, $5, $6, $7::alert_state, $8::alert_state, $9, '{}'::jsonb)
			RETURNING id, created_at
		`, alertID, string(model.AuditOperatorNoteAdded), string(model.ActorOperator), op.Login,
			model.StringPtrOrNil(op.Email), model.StringPtrOrNil(op.Role),
			string(st), string(st), comment).Scan(&id, &createdAt); err != nil {
			return apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to insert operator note.").WithCause(err)
		}

		created = model.AuditEvent{
			ID:            id,
			AlertID:       alertID,
			EventType:     model.AuditOperatorNoteAdded,
			ActorType:     model.ActorOperator,
			ActorLogin:    op.Login,
			ActorEmail:    model.StringPtrOrNil(op.Email),
			ActorRole:     model.StringPtrOrNil(op.Role),
			PreviousState: &st,
			NewState:      &st,
			Comment:       &comment,
			Metadata:      map[string]any{},
			CreatedAt:     createdAt,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &created, nil
}

func (r *AlertRepository) withTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Database unavailable.").WithCause(err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to commit database transaction.").WithCause(err)
	}
	return nil
}

func (r *AlertRepository) listAuditEvents(ctx context.Context, alertID string, limit int, desc bool) ([]model.AuditEvent, error) {
	order := "ASC"
	if desc {
		order = "DESC"
	}
	query := fmt.Sprintf(`
		SELECT id, event_type::text, actor_type::text, actor_login, actor_email, actor_role,
		       previous_state::text, new_state::text, comment, metadata, created_at
		FROM alert_audit_log
		WHERE alert_id = $1::uuid
		ORDER BY created_at %s, id %s
	`, order, order)
	var rows pgx.Rows
	var err error
	if limit > 0 {
		rows, err = r.pool.Query(ctx, query+" LIMIT $2", alertID, limit)
	} else {
		rows, err = r.pool.Query(ctx, query, alertID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := make([]model.AuditEvent, 0)
	for rows.Next() {
		var ev model.AuditEvent
		var eventType, actorType string
		var prevState, newState *string
		var metaRaw []byte
		if err := rows.Scan(
			&ev.ID, &eventType, &actorType, &ev.ActorLogin, &ev.ActorEmail, &ev.ActorRole,
			&prevState, &newState, &ev.Comment, &metaRaw, &ev.CreatedAt,
		); err != nil {
			return nil, err
		}
		ev.AlertID = alertID
		ev.EventType = model.AuditEventType(eventType)
		ev.ActorType = model.ActorType(actorType)
		if prevState != nil {
			s := model.AlertState(*prevState)
			ev.PreviousState = &s
		}
		if newState != nil {
			s := model.AlertState(*newState)
			ev.NewState = &s
		}
		ev.Metadata = map[string]any{}
		if len(metaRaw) > 0 {
			_ = json.Unmarshal(metaRaw, &ev.Metadata)
		}
		events = append(events, ev)
	}
	return events, rows.Err()
}

type lockedAlertSnapshot struct {
	ID             string
	State          model.AlertState
	Version        int
	AcknowledgedBy *string
	DismissedBy    *string
}

func lockAlertForTriage(ctx context.Context, tx pgx.Tx, alertID string) (lockedAlertSnapshot, error) {
	var snap lockedAlertSnapshot
	var st string
	err := tx.QueryRow(ctx, `
		SELECT id::text, state::text, version, acknowledged_by, dismissed_by
		FROM alerts
		WHERE id = $1::uuid
		FOR UPDATE
	`, alertID).Scan(&snap.ID, &st, &snap.Version, &snap.AcknowledgedBy, &snap.DismissedBy)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return snap, apierror.New(http.StatusNotFound, apierror.CodeAlertNotFound, "Alert episode not found.")
		}
		return snap, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to lock alert episode.").WithCause(err)
	}
	snap.State = model.AlertState(st)
	return snap, nil
}

func conflictSnapshotError(code, msg string, snap lockedAlertSnapshot) error {
	details := map[string]any{
		"current_version": snap.Version,
		"current_state":   string(snap.State),
	}
	if snap.AcknowledgedBy != nil {
		details["acknowledged_by"] = *snap.AcknowledgedBy
	}
	if snap.DismissedBy != nil {
		details["dismissed_by"] = *snap.DismissedBy
	}
	return apierror.New(http.StatusConflict, code, msg).WithDetails(details)
}

func getAlertItemTx(ctx context.Context, tx pgx.Tx, alertID string) (*model.Alert, error) {
	row := tx.QueryRow(ctx, `
		SELECT
			a.id::text, a.fingerprint, a.starts_at, a.ends_at, a.state::text, a.version,
			a.alert_name, a.severity, a.labels, a.annotations, a.generator_url,
			a.acknowledged_by, a.acknowledged_by_email, a.acknowledged_at,
			a.dismissed_by, a.dismissed_at, a.dismiss_reason, a.resolved_at,
			(SELECT COUNT(*) FROM alerts prev WHERE prev.fingerprint = a.fingerprint AND prev.starts_at < a.starts_at) AS previous_episode_count,
			s.id::text, s.sync_status::text, s.snow_sys_id, s.incident_number, s.incident_url,
			s.snow_state, s.snow_state_label, s.priority, s.assignment_group,
			s.caller_mode::text, s.caller_resolution_status::text, s.caller_sys_id,
			s.caller_fallback_reason, s.snow_state_synced_at, s.resolution_note_posted_at, s.last_error
		FROM alerts a
		LEFT JOIN servicenow_incidents s ON s.alert_id = a.id
		WHERE a.id = $1::uuid
	`, alertID)
	item, err := scanAlertItemRow(row)
	if err != nil {
		return nil, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to reload updated alert.").WithCause(err)
	}
	return &item, nil
}

type auditEntry struct {
	AlertID       string
	EventType     model.AuditEventType
	ActorType     model.ActorType
	ActorLogin    string
	ActorEmail    *string
	ActorRole     *string
	PreviousState *model.AlertState
	NewState      *model.AlertState
	Comment       *string
	Metadata      map[string]any
}

func insertAuditLogTx(ctx context.Context, tx pgx.Tx, entry auditEntry) error {
	var prevPtr, newPtr *string
	if entry.PreviousState != nil {
		s := string(*entry.PreviousState)
		prevPtr = &s
	}
	if entry.NewState != nil {
		s := string(*entry.NewState)
		newPtr = &s
	}
	metaJSON := []byte("{}")
	if len(entry.Metadata) > 0 {
		encoded, err := json.Marshal(entry.Metadata)
		if err == nil {
			metaJSON = encoded
		}
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO alert_audit_log (
			alert_id, event_type, actor_type, actor_login, actor_email, actor_role,
			previous_state, new_state, comment, metadata
		)
		VALUES ($1::uuid, $2::audit_event_type, $3::actor_type, $4, $5, $6, $7::alert_state, $8::alert_state, $9, $10::jsonb)
	`, entry.AlertID, string(entry.EventType), string(entry.ActorType), entry.ActorLogin,
		entry.ActorEmail, entry.ActorRole, prevPtr, newPtr, entry.Comment, string(metaJSON))
	return err
}

func scanAlertItemRow(row pgx.Row) (model.Alert, error) {
	var a model.Alert
	var stateStr string
	var labelsRaw, annRaw []byte

	var snowID, snowSyncStatus, snowSysID, snowNum, snowURL *string
	var snowState, snowPriority *int16
	var snowStateLabel, snowAssignGrp, snowCallerMode, snowCallerRes, snowCallerSysID, snowFallbackReason, snowLastErr *string
	var snowSyncedAt, snowNotePostedAt *time.Time

	err := row.Scan(
		&a.ID, &a.Fingerprint, &a.StartsAt, &a.EndsAt, &stateStr, &a.Version,
		&a.AlertName, &a.Severity, &labelsRaw, &annRaw, &a.GeneratorURL,
		&a.AcknowledgedBy, &a.AcknowledgedByEmail, &a.AcknowledgedAt,
		&a.DismissedBy, &a.DismissedAt, &a.DismissReason, &a.ResolvedAt,
		&a.PreviousEpisodeCount,
		&snowID, &snowSyncStatus, &snowSysID, &snowNum, &snowURL,
		&snowState, &snowStateLabel, &snowPriority, &snowAssignGrp,
		&snowCallerMode, &snowCallerRes, &snowCallerSysID,
		&snowFallbackReason, &snowSyncedAt, &snowNotePostedAt, &snowLastErr,
	)
	if err != nil {
		return a, err
	}

	a.State = model.AlertState(stateStr)
	a.Labels = unmarshalStringMap(labelsRaw)
	a.Annotations = unmarshalStringMap(annRaw)

	if snowID != nil && snowSyncStatus != nil && snowCallerMode != nil && snowCallerRes != nil {
		a.ServiceNowIncident = &model.ServiceNowIncident{
			ID:                     *snowID,
			AlertID:                a.ID,
			SyncStatus:             model.SnowSyncStatus(*snowSyncStatus),
			SnowSysID:              snowSysID,
			IncidentNumber:         snowNum,
			IncidentURL:            snowURL,
			SnowState:              snowState,
			SnowStateLabel:         snowStateLabel,
			Priority:               snowPriority,
			AssignmentGroup:        snowAssignGrp,
			CallerMode:             model.SnowCallerMode(*snowCallerMode),
			CallerResolutionStatus: model.SnowCallerResolutionStatus(*snowCallerRes),
			CallerSysID:            snowCallerSysID,
			CallerFallbackReason:   snowFallbackReason,
			SnowStateSyncedAt:      snowSyncedAt,
			ResolutionNotePostedAt: snowNotePostedAt,
			LastError:              snowLastErr,
		}
	}
	a.IncidentDriftStatus = model.ComputeDriftStatus(a.State, a.ServiceNowIncident)
	return a, nil
}

func scanAlertDetailRow(row pgx.Row) (*model.AlertDetail, error) {
	var a model.AlertDetail
	var stateStr string
	var labelsRaw, annRaw []byte

	var snowID, snowSyncStatus, snowSysID, snowNum, snowURL *string
	var snowState, snowPriority *int16
	var snowStateLabel, snowAssignGrp, snowCallerMode, snowCallerRes, snowCallerSysID, snowFallbackReason, snowLastErr *string
	var snowSyncedAt, snowNotePostedAt *time.Time

	err := row.Scan(
		&a.ID, &a.Fingerprint, &a.StartsAt, &a.EndsAt, &stateStr, &a.Version,
		&a.AlertName, &a.Severity, &a.GroupKey, &a.Receiver, &labelsRaw, &annRaw,
		&a.GeneratorURL, &a.ExternalURL,
		&a.AcknowledgedBy, &a.AcknowledgedByEmail, &a.AcknowledgedAt,
		&a.DismissedBy, &a.DismissedAt, &a.DismissReason, &a.ResolvedAt,
		&a.DeliveryCount, &a.FirstReceivedAt, &a.LastReceivedAt,
		&a.PreviousEpisodeCount,
		&snowID, &snowSyncStatus, &snowSysID, &snowNum, &snowURL,
		&snowState, &snowStateLabel, &snowPriority, &snowAssignGrp,
		&snowCallerMode, &snowCallerRes, &snowCallerSysID,
		&snowFallbackReason, &snowSyncedAt, &snowNotePostedAt, &snowLastErr,
	)
	if err != nil {
		return nil, err
	}

	a.State = model.AlertState(stateStr)
	a.Labels = unmarshalStringMap(labelsRaw)
	a.Annotations = unmarshalStringMap(annRaw)

	if snowID != nil && snowSyncStatus != nil && snowCallerMode != nil && snowCallerRes != nil {
		a.ServiceNowIncident = &model.ServiceNowIncident{
			ID:                     *snowID,
			AlertID:                a.ID,
			SyncStatus:             model.SnowSyncStatus(*snowSyncStatus),
			SnowSysID:              snowSysID,
			IncidentNumber:         snowNum,
			IncidentURL:            snowURL,
			SnowState:              snowState,
			SnowStateLabel:         snowStateLabel,
			Priority:               snowPriority,
			AssignmentGroup:        snowAssignGrp,
			CallerMode:             model.SnowCallerMode(*snowCallerMode),
			CallerResolutionStatus: model.SnowCallerResolutionStatus(*snowCallerRes),
			CallerSysID:            snowCallerSysID,
			CallerFallbackReason:   snowFallbackReason,
			SnowStateSyncedAt:      snowSyncedAt,
			ResolutionNotePostedAt: snowNotePostedAt,
			LastError:              snowLastErr,
		}
	}
	a.IncidentDriftStatus = model.ComputeDriftStatus(a.State, a.ServiceNowIncident)
	return &a, nil
}

func unmarshalStringMap(raw []byte) map[string]string {
	out := map[string]string{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

func escapeLikePattern(s string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(s)
}
