package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/oriraz/grafana-snow-integration/backend/internal/apierror"
	"github.com/oriraz/grafana-snow-integration/backend/internal/auth"
	"github.com/oriraz/grafana-snow-integration/backend/internal/model"
)

const stalePendingThreshold = 60 * time.Second

// IncidentReservation holds Tx 1 output before the outbound ServiceNow call runs.
type IncidentReservation struct {
	IncidentID string
	Alert      model.AlertDetail
	IsRetry    bool
}

// SnowCreateResult carries parsed ServiceNow incident metadata into Tx 2.
type SnowCreateResult struct {
	SysID           string
	Number          string
	IncidentURL     string
	State           int16
	StateLabel      string
	Priority        *int16
	AssignmentGroup *string
	OpenedAt        *time.Time
	ResolvedAt      *time.Time
	ClosedAt        *time.Time
}

// SnowSyncUpdate carries remote state changes synced via webhook, manual sync, or poller.
type SnowSyncUpdate struct {
	State           int16
	StateLabel      string
	Priority        *int16
	AssignmentGroup *string
	ResolvedAt      *time.Time
	ClosedAt        *time.Time
	CloseNotes      string
	UpdatedBy       string
}

// CallerResolution records the outcome of resolving a ServiceNow sys_user caller_id.
type CallerResolution struct {
	SysID            string
	ResolutionStatus model.SnowCallerResolutionStatus
	FallbackReason   *string
}

// ServiceNowRepository manages PostgreSQL state for ServiceNow incidents, user cache, and gauges.
type ServiceNowRepository struct {
	pool *pgxpool.Pool
}

// NewServiceNowRepository constructs a ServiceNowRepository.
func NewServiceNowRepository(pool *pgxpool.Pool) *ServiceNowRepository {
	return &ServiceNowRepository{pool: pool}
}

// ReserveIncidentCreation runs Phase 1 (Tx 1) of incident creation without holding locks across HTTP.
func (r *ServiceNowRepository) ReserveIncidentCreation(
	ctx context.Context,
	alertID string,
	op auth.OperatorContext,
	req model.CreateIncidentRequest,
	allowRetrospective bool,
) (*IncidentReservation, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Database unavailable.").WithCause(err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	var alertRow model.AlertDetail
	var stateStr string
	var labelsRaw, annRaw []byte
	err = tx.QueryRow(ctx, `
		SELECT id::text, fingerprint, starts_at, ends_at, state::text, version,
		       alert_name, severity, group_key, receiver, labels, annotations,
		       generator_url, external_url, acknowledged_by, acknowledged_by_email, acknowledged_at
		FROM alerts
		WHERE id = $1::uuid
		FOR UPDATE
	`, alertID).Scan(
		&alertRow.ID, &alertRow.Fingerprint, &alertRow.StartsAt, &alertRow.EndsAt, &stateStr, &alertRow.Version,
		&alertRow.AlertName, &alertRow.Severity, &alertRow.GroupKey, &alertRow.Receiver, &labelsRaw, &annRaw,
		&alertRow.GeneratorURL, &alertRow.ExternalURL, &alertRow.AcknowledgedBy, &alertRow.AcknowledgedByEmail, &alertRow.AcknowledgedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apierror.New(http.StatusNotFound, apierror.CodeAlertNotFound, "Alert episode not found.")
		}
		return nil, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to lock alert episode.").WithCause(err)
	}
	alertRow.State = model.AlertState(stateStr)
	alertRow.Labels = unmarshalStringMap(labelsRaw)
	alertRow.Annotations = unmarshalStringMap(annRaw)

	if !model.CanAttachIncident(alertRow.State, allowRetrospective) {
		return nil, apierror.New(http.StatusConflict, apierror.CodeInvalidStateTransition,
			fmt.Sprintf("Alert must be acknowledged before creating a ServiceNow incident (current state: %s).", alertRow.State),
		).WithDetails(map[string]any{"current_state": string(alertRow.State), "current_version": alertRow.Version})
	}

	var existingID string
	var existingStatusStr string
	var existingUpdatedAt time.Time
	var existingNum, existingSysID *string
	err = tx.QueryRow(ctx, `
		SELECT id::text, sync_status::text, updated_at, incident_number, snow_sys_id
		FROM servicenow_incidents
		WHERE alert_id = $1::uuid
		FOR UPDATE
	`, alertID).Scan(&existingID, &existingStatusStr, &existingUpdatedAt, &existingNum, &existingSysID)

	isRetry := false
	var incidentID string

	if err == nil {
		existingStatus := model.SnowSyncStatus(existingStatusStr)
		switch existingStatus {
		case model.SnowSyncCreated, model.SnowSyncLinked:
			return nil, apierror.New(http.StatusConflict, apierror.CodeIncidentAlreadyLinked,
				"A ServiceNow incident is already linked to this alert episode.",
			).WithDetails(map[string]any{
				"incident_id":     existingID,
				"sync_status":     string(existingStatus),
				"incident_number": existingNum,
				"snow_sys_id":     existingSysID,
			})
		case model.SnowSyncPending:
			if time.Since(existingUpdatedAt) <= stalePendingThreshold {
				return nil, apierror.New(http.StatusConflict, apierror.CodeIncidentCreationInProgress,
					"ServiceNow incident creation is already in progress for this alert.",
				).WithDetails(map[string]any{"incident_id": existingID})
			}
			isRetry = true
		case model.SnowSyncFailed:
			isRetry = true
		}

		if err := tx.QueryRow(ctx, `
			UPDATE servicenow_incidents
			SET sync_status = 'pending',
				impact = $2,
				urgency = $3,
				short_description = $4,
				description = $5,
				assignment_group = $6,
				category = $7,
				caller_mode = $8::snow_caller_mode,
				caller_resolution_status = 'pending',
				caller_sys_id = NULL,
				caller_fallback_reason = NULL,
				last_error = NULL,
				retry_count = retry_count + 1
			WHERE id = $1::uuid
			RETURNING id::text
		`, existingID, req.Impact, req.Urgency, req.ShortDescription, model.StringPtrOrNil(req.Description),
			model.StringPtrOrNil(req.AssignmentGroup), model.StringPtrOrNil(req.Category), string(req.CallerMode)).Scan(&incidentID); err != nil {
			return nil, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to update pending incident reservation.").WithCause(err)
		}
	} else if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `
			INSERT INTO servicenow_incidents (
				alert_id, correlation_id, sync_status, impact, urgency,
				short_description, description, assignment_group, category,
				caller_mode, caller_resolution_status, created_by_login, created_by_email
			)
			VALUES ($1::uuid, $1, 'pending', $2, $3, $4, $5, $6, $7, $8::snow_caller_mode, 'pending', $9, $10)
			RETURNING id::text
		`, alertID, req.Impact, req.Urgency, req.ShortDescription, model.StringPtrOrNil(req.Description),
			model.StringPtrOrNil(req.AssignmentGroup), model.StringPtrOrNil(req.Category),
			string(req.CallerMode), op.Login, model.StringPtrOrNil(op.Email)).Scan(&incidentID)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return nil, apierror.New(http.StatusConflict, apierror.CodeIncidentCreationInProgress,
					"ServiceNow incident creation is already in progress for this alert.")
			}
			return nil, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to reserve ServiceNow incident.").WithCause(err)
		}
	} else {
		return nil, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to query existing incident reservation.").WithCause(err)
	}

	curState := alertRow.State
	if err := insertAuditLogTx(ctx, tx, auditEntry{
		AlertID:       alertID,
		EventType:     model.AuditSnowIncidentReq,
		ActorType:     model.ActorOperator,
		ActorLogin:    op.Login,
		ActorEmail:    model.StringPtrOrNil(op.Email),
		ActorRole:     model.StringPtrOrNil(op.Role),
		PreviousState: &curState,
		NewState:      &curState,
		Comment:       model.StringPtrOrNil(req.WorkNotes),
		Metadata: map[string]any{
			"caller_mode": string(req.CallerMode),
			"is_retry":    isRetry,
		},
	}); err != nil {
		return nil, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to record incident request audit event.").WithCause(err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to commit incident reservation.").WithCause(err)
	}

	return &IncidentReservation{
		IncidentID: incidentID,
		Alert:      alertRow,
		IsRetry:    isRetry,
	}, nil
}

// FinalizeIncidentCreated completes Tx 2 after ServiceNow creates or adopts an incident.
func (r *ServiceNowRepository) FinalizeIncidentCreated(
	ctx context.Context,
	alertID string,
	incidentID string,
	alertState model.AlertState,
	op auth.OperatorContext,
	res SnowCreateResult,
	caller CallerResolution,
	adopted bool,
) (*model.ServiceNowIncident, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Database unavailable.").WithCause(err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	// Lock parent alert row before servicenow_incidents to preserve canonical lock order
	// (alerts -> servicenow_incidents -> alert_audit_log) and prevent AB-BA deadlocks.
	var lockedStateStr string
	if err := tx.QueryRow(ctx, `SELECT state::text FROM alerts WHERE id = $1::uuid FOR UPDATE`, alertID).Scan(&lockedStateStr); err != nil {
		return nil, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to lock alert row.").WithCause(err)
	}
	alertState = model.AlertState(lockedStateStr)

	var inc model.ServiceNowIncident
	var syncStatusStr, callerModeStr, callerResStr string
	err = tx.QueryRow(ctx, `
		UPDATE servicenow_incidents
		SET sync_status = 'created',
			snow_sys_id = $2,
			incident_number = $3,
			incident_url = $4,
			snow_state = $5,
			snow_state_label = $6,
			priority = $7,
			assignment_group = COALESCE($8, assignment_group),
			caller_resolution_status = $9::snow_caller_resolution_status,
			caller_sys_id = $10,
			caller_fallback_reason = $11,
			snow_opened_at = $12,
			snow_resolved_at = $13,
			snow_closed_at = $14,
			snow_state_synced_at = NOW(),
			last_error = NULL
		WHERE id = $1::uuid
		RETURNING id::text, alert_id::text, correlation_id, sync_status::text,
		          snow_sys_id, incident_number, incident_url,
		          snow_state, snow_state_label, priority, assignment_group,
		          caller_mode::text, caller_resolution_status::text, caller_sys_id, caller_fallback_reason,
		          snow_opened_at, snow_resolved_at, snow_closed_at, snow_state_synced_at, resolution_note_posted_at
	`, incidentID, res.SysID, res.Number, res.IncidentURL, res.State, res.StateLabel,
		res.Priority, res.AssignmentGroup, string(caller.ResolutionStatus),
		model.StringPtrOrNil(caller.SysID), caller.FallbackReason,
		res.OpenedAt, res.ResolvedAt, res.ClosedAt,
	).Scan(
		&inc.ID, &inc.AlertID, &inc.CorrelationID, &syncStatusStr,
		&inc.SnowSysID, &inc.IncidentNumber, &inc.IncidentURL,
		&inc.SnowState, &inc.SnowStateLabel, &inc.Priority, &inc.AssignmentGroup,
		&callerModeStr, &callerResStr, &inc.CallerSysID, &inc.CallerFallbackReason,
		&inc.SnowOpenedAt, &inc.SnowResolvedAt, &inc.SnowClosedAt, &inc.SnowStateSyncedAt, &inc.ResolutionNotePostedAt,
	)
	if err != nil {
		return nil, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to finalize created incident.").WithCause(err)
	}
	inc.SyncStatus = model.SnowSyncStatus(syncStatusStr)
	inc.CallerMode = model.SnowCallerMode(callerModeStr)
	inc.CallerResolutionStatus = model.SnowCallerResolutionStatus(callerResStr)

	if err := insertAuditLogTx(ctx, tx, auditEntry{
		AlertID:       alertID,
		EventType:     model.AuditSnowIncidentCreated,
		ActorType:     model.ActorOperator,
		ActorLogin:    op.Login,
		ActorEmail:    model.StringPtrOrNil(op.Email),
		ActorRole:     model.StringPtrOrNil(op.Role),
		PreviousState: &alertState,
		NewState:      &alertState,
		Metadata: map[string]any{
			"incident_number":          res.Number,
			"snow_sys_id":              res.SysID,
			"caller_resolution_status": string(caller.ResolutionStatus),
			"adopted_existing":         adopted,
		},
	}); err != nil {
		return nil, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to record incident creation audit event.").WithCause(err)
	}

	if caller.ResolutionStatus == model.CallerResFallbackNOC {
		if err := insertAuditLogTx(ctx, tx, auditEntry{
			AlertID:       alertID,
			EventType:     model.AuditSnowCallerFallback,
			ActorType:     model.ActorSystem,
			ActorLogin:    "system",
			ActorEmail:    model.StringPtrOrNil(op.Email),
			PreviousState: &alertState,
			NewState:      &alertState,
			Comment:       caller.FallbackReason,
			Metadata: map[string]any{
				"incident_number":       res.Number,
				"fallback_caller_sysid": caller.SysID,
				"operator_email":        op.Email,
			},
		}); err != nil {
			return nil, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to record caller fallback audit event.").WithCause(err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to commit finalized incident.").WithCause(err)
	}
	return &inc, nil
}

// FinalizeIncidentFailed records a failed ServiceNow creation attempt while keeping the alert acknowledged.
func (r *ServiceNowRepository) FinalizeIncidentFailed(ctx context.Context, alertID, incidentID string, alertState model.AlertState, op auth.OperatorContext, errMsg string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	var lockedStateStr string
	if err := tx.QueryRow(ctx, `SELECT state::text FROM alerts WHERE id = $1::uuid FOR UPDATE`, alertID).Scan(&lockedStateStr); err != nil {
		return err
	}
	alertState = model.AlertState(lockedStateStr)

	if _, err := tx.Exec(ctx, `
		UPDATE servicenow_incidents
		SET sync_status = 'failed',
			last_error = $2
		WHERE id = $1::uuid
	`, incidentID, errMsg); err != nil {
		return err
	}

	if err := insertAuditLogTx(ctx, tx, auditEntry{
		AlertID:       alertID,
		EventType:     model.AuditSnowIncidentFailed,
		ActorType:     model.ActorOperator,
		ActorLogin:    op.Login,
		ActorEmail:    model.StringPtrOrNil(op.Email),
		ActorRole:     model.StringPtrOrNil(op.Role),
		PreviousState: &alertState,
		NewState:      &alertState,
		Comment:       &errMsg,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SaveLinkedIncident links an existing ServiceNow incident to an alert episode.
func (r *ServiceNowRepository) SaveLinkedIncident(
	ctx context.Context,
	alertID string,
	op auth.OperatorContext,
	snowInc SnowCreateResult,
	allowRetrospective bool,
) (*model.IncidentSyncResponse, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Database unavailable.").WithCause(err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	var stateStr string
	var version int
	err = tx.QueryRow(ctx, `
		SELECT state::text, version
		FROM alerts
		WHERE id = $1::uuid
		FOR UPDATE
	`, alertID).Scan(&stateStr, &version)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apierror.New(http.StatusNotFound, apierror.CodeAlertNotFound, "Alert episode not found.")
		}
		return nil, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to lock alert.").WithCause(err)
	}
	alertState := model.AlertState(stateStr)
	if !model.CanAttachIncident(alertState, allowRetrospective) {
		return nil, apierror.New(http.StatusConflict, apierror.CodeInvalidStateTransition,
			fmt.Sprintf("Alert must be acknowledged before linking a ServiceNow incident (current state: %s).", alertState),
		).WithDetails(map[string]any{"current_state": string(alertState), "current_version": version})
	}

	var existingID, existingStatusStr string
	err = tx.QueryRow(ctx, `
		SELECT id::text, sync_status::text
		FROM servicenow_incidents
		WHERE alert_id = $1::uuid
		FOR UPDATE
	`, alertID).Scan(&existingID, &existingStatusStr)
	if err == nil {
		st := model.SnowSyncStatus(existingStatusStr)
		if st == model.SnowSyncCreated || st == model.SnowSyncLinked {
			return nil, apierror.New(http.StatusConflict, apierror.CodeIncidentAlreadyLinked,
				"A ServiceNow incident is already linked to this alert episode.")
		}
		if st == model.SnowSyncPending {
			return nil, apierror.New(http.StatusConflict, apierror.CodeIncidentCreationInProgress,
				"ServiceNow incident creation is currently in progress for this alert.")
		}
		// Replace failed reservation row with the linked ticket.
		_, err = tx.Exec(ctx, `
			UPDATE servicenow_incidents
			SET sync_status = 'linked',
				snow_sys_id = $2,
				incident_number = $3,
				incident_url = $4,
				snow_state = $5,
				snow_state_label = $6,
				priority = $7,
				assignment_group = $8,
				short_description = $9,
				snow_opened_at = $10,
				snow_resolved_at = $11,
				snow_closed_at = $12,
				snow_state_synced_at = NOW(),
				last_error = NULL
			WHERE id = $1::uuid
		`, existingID, snowInc.SysID, snowInc.Number, snowInc.IncidentURL,
			snowInc.State, snowInc.StateLabel, snowInc.Priority, snowInc.AssignmentGroup,
			fmt.Sprintf("Linked incident %s", snowInc.Number),
			snowInc.OpenedAt, snowInc.ResolvedAt, snowInc.ClosedAt)
		if err != nil {
			return nil, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to update linked incident.").WithCause(err)
		}
	} else if errors.Is(err, pgx.ErrNoRows) {
		_, err = tx.Exec(ctx, `
			INSERT INTO servicenow_incidents (
				alert_id, correlation_id, sync_status, snow_sys_id, incident_number, incident_url,
				snow_state, snow_state_label, priority, assignment_group,
				short_description, caller_mode, caller_resolution_status,
				created_by_login, created_by_email,
				snow_opened_at, snow_resolved_at, snow_closed_at, snow_state_synced_at
			)
			VALUES (
				$1::uuid, $1, 'linked', $2, $3, $4,
				$5, $6, $7, $8,
				$9, 'sso_user', 'pending',
				$10, $11,
				$12, $13, $14, NOW()
			)
		`, alertID, snowInc.SysID, snowInc.Number, snowInc.IncidentURL,
			snowInc.State, snowInc.StateLabel, snowInc.Priority, snowInc.AssignmentGroup,
			fmt.Sprintf("Linked incident %s", snowInc.Number),
			op.Login, model.StringPtrOrNil(op.Email),
			snowInc.OpenedAt, snowInc.ResolvedAt, snowInc.ClosedAt)
		if err != nil {
			return nil, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to save linked incident.").WithCause(err)
		}
	} else {
		return nil, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to check linked incident.").WithCause(err)
	}

	if err := insertAuditLogTx(ctx, tx, auditEntry{
		AlertID:       alertID,
		EventType:     model.AuditSnowIncidentLinked,
		ActorType:     model.ActorOperator,
		ActorLogin:    op.Login,
		ActorEmail:    model.StringPtrOrNil(op.Email),
		ActorRole:     model.StringPtrOrNil(op.Role),
		PreviousState: &alertState,
		NewState:      &alertState,
		Metadata: map[string]any{
			"incident_number": snowInc.Number,
			"snow_sys_id":     snowInc.SysID,
		},
	}); err != nil {
		return nil, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to record incident link audit event.").WithCause(err)
	}

	inc, err := getIncidentByAlertTx(ctx, tx, alertID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to commit linked incident.").WithCause(err)
	}

	return &model.IncidentSyncResponse{
		AlertID:             alertID,
		ServiceNowIncident:  inc,
		IncidentDriftStatus: model.ComputeDriftStatus(alertState, inc),
	}, nil
}

// GetAlertAndIncident returns the alert state and linked incident (if any) for sync/work-note operations.
func (r *ServiceNowRepository) GetAlertAndIncident(ctx context.Context, alertID string) (model.AlertState, *model.ServiceNowIncident, error) {
	var stateStr string
	var inc model.ServiceNowIncident
	var incID, syncStatus, callerMode, callerRes *string

	err := r.pool.QueryRow(ctx, `
		SELECT a.state::text,
		       s.id::text, s.sync_status::text, s.snow_sys_id, s.incident_number, s.incident_url,
		       s.snow_state, s.snow_state_label, s.priority, s.assignment_group,
		       s.caller_mode::text, s.caller_resolution_status::text, s.caller_sys_id,
		       s.caller_fallback_reason, s.snow_state_synced_at, s.resolution_note_posted_at
		FROM alerts a
		LEFT JOIN servicenow_incidents s ON s.alert_id = a.id
		WHERE a.id = $1::uuid
	`, alertID).Scan(
		&stateStr,
		&incID, &syncStatus, &inc.SnowSysID, &inc.IncidentNumber, &inc.IncidentURL,
		&inc.SnowState, &inc.SnowStateLabel, &inc.Priority, &inc.AssignmentGroup,
		&callerMode, &callerRes, &inc.CallerSysID,
		&inc.CallerFallbackReason, &inc.SnowStateSyncedAt, &inc.ResolutionNotePostedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil, apierror.New(http.StatusNotFound, apierror.CodeAlertNotFound, "Alert episode not found.")
		}
		return "", nil, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to load alert incident.").WithCause(err)
	}

	alertState := model.AlertState(stateStr)
	if incID == nil || syncStatus == nil {
		return alertState, nil, apierror.New(http.StatusNotFound, apierror.CodeIncidentNotFound, "No ServiceNow incident is linked to this alert.")
	}
	inc.ID = *incID
	inc.AlertID = alertID
	inc.SyncStatus = model.SnowSyncStatus(*syncStatus)
	if callerMode != nil {
		inc.CallerMode = model.SnowCallerMode(*callerMode)
	}
	if callerRes != nil {
		inc.CallerResolutionStatus = model.SnowCallerResolutionStatus(*callerRes)
	}
	if inc.SnowSysID == nil || (inc.SyncStatus != model.SnowSyncCreated && inc.SyncStatus != model.SnowSyncLinked) {
		return alertState, nil, apierror.New(http.StatusNotFound, apierror.CodeIncidentNotFound, "Linked ServiceNow incident is not active.")
	}
	return alertState, &inc, nil
}

// UpdateIncidentForAlert updates a single alert's linked incident after live sync.
func (r *ServiceNowRepository) UpdateIncidentForAlert(
	ctx context.Context,
	alertID string,
	op auth.OperatorContext,
	update SnowSyncUpdate,
) (*model.IncidentSyncResponse, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Database unavailable.").WithCause(err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	var alertStateStr string
	if err := tx.QueryRow(ctx, `SELECT state::text FROM alerts WHERE id = $1::uuid FOR UPDATE`, alertID).Scan(&alertStateStr); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apierror.New(http.StatusNotFound, apierror.CodeAlertNotFound, "Alert episode not found.")
		}
		return nil, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to lock alert row.").WithCause(err)
	}

	var prevState *int16
	err = tx.QueryRow(ctx, `
		SELECT s.snow_state
		FROM servicenow_incidents s
		WHERE s.alert_id = $1::uuid
		FOR UPDATE
	`, alertID).Scan(&prevState)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apierror.New(http.StatusNotFound, apierror.CodeIncidentNotFound, "Linked ServiceNow incident not found.")
		}
		return nil, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to lock incident record.").WithCause(err)
	}
	alertState := model.AlertState(alertStateStr)

	if _, err := tx.Exec(ctx, `
		UPDATE servicenow_incidents
		SET snow_state = $2,
			snow_state_label = $3,
			priority = COALESCE($4, priority),
			assignment_group = COALESCE($5, assignment_group),
			snow_resolved_at = COALESCE($6, snow_resolved_at),
			snow_closed_at = COALESCE($7, snow_closed_at),
			snow_state_synced_at = NOW()
		WHERE alert_id = $1::uuid
	`, alertID, update.State, update.StateLabel, update.Priority, update.AssignmentGroup, update.ResolvedAt, update.ClosedAt); err != nil {
		return nil, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to update incident status.").WithCause(err)
	}

	if err := insertAuditLogTx(ctx, tx, auditEntry{
		AlertID:       alertID,
		EventType:     model.AuditSnowStateSynced,
		ActorType:     model.ActorOperator,
		ActorLogin:    op.Login,
		ActorEmail:    model.StringPtrOrNil(op.Email),
		ActorRole:     model.StringPtrOrNil(op.Role),
		PreviousState: &alertState,
		NewState:      &alertState,
		Metadata: map[string]any{
			"previous_snow_state": prevState,
			"new_snow_state":      update.State,
			"new_snow_label":      update.StateLabel,
		},
	}); err != nil {
		return nil, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to record sync audit event.").WithCause(err)
	}

	inc, err := getIncidentByAlertTx(ctx, tx, alertID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to commit sync update.").WithCause(err)
	}

	return &model.IncidentSyncResponse{
		AlertID:             alertID,
		ServiceNowIncident:  inc,
		IncidentDriftStatus: model.ComputeDriftStatus(alertState, inc),
	}, nil
}

// UpdateIncidentsBySysID syncs all linked alert rows sharing snow_sys_id (used by inbound webhook and poller).
func (r *ServiceNowRepository) UpdateIncidentsBySysID(
	ctx context.Context,
	sysID string,
	update SnowSyncUpdate,
	actorType model.ActorType,
	actorLogin string,
	logOnlyOnChange bool,
) (int, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Database unavailable.").WithCause(err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	type targetRow struct {
		IncidentID      string
		AlertID         string
		AlertState      model.AlertState
		PrevSnowState   *int16
		PrevAssignGroup *string
	}

	// Lock alerts in deterministic id order first, then servicenow_incidents.
	alertRows, err := tx.Query(ctx, `
		SELECT a.id::text, a.state::text
		FROM alerts a
		INNER JOIN servicenow_incidents s ON s.alert_id = a.id
		WHERE s.snow_sys_id = $1 AND s.sync_status IN ('created', 'linked')
		ORDER BY a.id ASC
		FOR UPDATE OF a
	`, sysID)
	if err != nil {
		return 0, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to lock alerts for sys_id.").WithCause(err)
	}
	alertStates := make(map[string]model.AlertState)
	for alertRows.Next() {
		var aID, stStr string
		if err := alertRows.Scan(&aID, &stStr); err != nil {
			alertRows.Close()
			return 0, err
		}
		alertStates[aID] = model.AlertState(stStr)
	}
	alertRows.Close()
	if err := alertRows.Err(); err != nil {
		return 0, err
	}
	if len(alertStates) == 0 {
		if err := tx.Commit(ctx); err != nil {
			return 0, err
		}
		return 0, nil
	}

	rows, err := tx.Query(ctx, `
		SELECT s.id::text, s.alert_id::text, s.snow_state, s.assignment_group
		FROM servicenow_incidents s
		WHERE s.snow_sys_id = $1 AND s.sync_status IN ('created', 'linked')
		ORDER BY s.alert_id ASC
		FOR UPDATE
	`, sysID)
	if err != nil {
		return 0, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to query incidents by sys_id.").WithCause(err)
	}
	var targets []targetRow
	for rows.Next() {
		var t targetRow
		if err := rows.Scan(&t.IncidentID, &t.AlertID, &t.PrevSnowState, &t.PrevAssignGroup); err != nil {
			rows.Close()
			return 0, err
		}
		t.AlertState = alertStates[t.AlertID]
		targets = append(targets, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(targets) == 0 {
		if err := tx.Commit(ctx); err != nil {
			return 0, err
		}
		return 0, nil
	}

	for _, t := range targets {
		if _, err := tx.Exec(ctx, `
			UPDATE servicenow_incidents
			SET snow_state = $2,
				snow_state_label = $3,
				priority = COALESCE($4, priority),
				assignment_group = COALESCE($5, assignment_group),
				snow_resolved_at = COALESCE($6, snow_resolved_at),
				snow_closed_at = COALESCE($7, snow_closed_at),
				snow_state_synced_at = NOW()
			WHERE id = $1::uuid
		`, t.IncidentID, update.State, update.StateLabel, update.Priority, update.AssignmentGroup, update.ResolvedAt, update.ClosedAt); err != nil {
			return 0, err
		}

		stateChanged := t.PrevSnowState == nil || *t.PrevSnowState != update.State
		groupChanged := update.AssignmentGroup != nil && (t.PrevAssignGroup == nil || *t.PrevAssignGroup != *update.AssignmentGroup)
		if logOnlyOnChange && !stateChanged && !groupChanged {
			continue
		}

		meta := map[string]any{
			"snow_sys_id":         sysID,
			"previous_snow_state": t.PrevSnowState,
			"new_snow_state":      update.State,
			"new_snow_label":      update.StateLabel,
		}
		if update.UpdatedBy != "" {
			meta["updated_by"] = update.UpdatedBy
		}
		if err := insertAuditLogTx(ctx, tx, auditEntry{
			AlertID:       t.AlertID,
			EventType:     model.AuditSnowStateSynced,
			ActorType:     actorType,
			ActorLogin:    actorLogin,
			PreviousState: &t.AlertState,
			NewState:      &t.AlertState,
			Comment:       model.StringPtrOrNil(update.CloseNotes),
			Metadata:      meta,
		}); err != nil {
			return 0, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(targets), nil
}

// RecordWorkNotePosted logs a snow_work_note_posted audit entry for an operator work note.
func (r *ServiceNowRepository) RecordWorkNotePosted(
	ctx context.Context,
	alertID string,
	alertState model.AlertState,
	op auth.OperatorContext,
	noteText string,
	incidentNumber *string,
) (*model.AuditEvent, error) {
	var id int64
	var createdAt time.Time
	err := r.pool.QueryRow(ctx, `
		INSERT INTO alert_audit_log (
			alert_id, event_type, actor_type, actor_login, actor_email, actor_role,
			previous_state, new_state, comment, metadata
		)
		VALUES ($1::uuid, $2::audit_event_type, $3::actor_type, $4, $5, $6, $7::alert_state, $8::alert_state, $9,
		        jsonb_build_object('incident_number', COALESCE($10, '')))
		RETURNING id, created_at
	`, alertID, string(model.AuditSnowWorkNotePosted), string(model.ActorOperator),
		op.Login, model.StringPtrOrNil(op.Email), model.StringPtrOrNil(op.Role),
		string(alertState), string(alertState), noteText, incidentNumber).Scan(&id, &createdAt)
	if err != nil {
		return nil, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to log work note audit event.").WithCause(err)
	}

	return &model.AuditEvent{
		ID:            id,
		AlertID:       alertID,
		EventType:     model.AuditSnowWorkNotePosted,
		ActorType:     model.ActorOperator,
		ActorLogin:    op.Login,
		ActorEmail:    model.StringPtrOrNil(op.Email),
		ActorRole:     model.StringPtrOrNil(op.Role),
		PreviousState: &alertState,
		NewState:      &alertState,
		Comment:       &noteText,
		Metadata:      map[string]any{"incident_number": incidentNumber},
		CreatedAt:     createdAt,
	}, nil
}

// LookupUserCache reads a valid non-expired user lookup entry from snow_user_cache.
func (r *ServiceNowRepository) LookupUserCache(ctx context.Context, cacheKey string) (*model.SnowUserCacheEntry, error) {
	var entry model.SnowUserCacheEntry
	err := r.pool.QueryRow(ctx, `
		SELECT email, snow_sys_id, snow_user_name, is_active, fetched_at, expires_at
		FROM snow_user_cache
		WHERE email = $1 AND expires_at > NOW()
	`, strings.ToLower(strings.TrimSpace(cacheKey))).Scan(
		&entry.Email, &entry.SnowSysID, &entry.SnowUserName, &entry.IsActive, &entry.FetchedAt, &entry.ExpiresAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &entry, nil
}

// UpsertUserCache writes or updates an entry in snow_user_cache.
func (r *ServiceNowRepository) UpsertUserCache(ctx context.Context, entry model.SnowUserCacheEntry) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO snow_user_cache (email, snow_sys_id, snow_user_name, is_active, fetched_at, expires_at)
		VALUES ($1, $2, $3, $4, NOW(), $5)
		ON CONFLICT (email) DO UPDATE
		SET snow_sys_id = EXCLUDED.snow_sys_id,
			snow_user_name = EXCLUDED.snow_user_name,
			is_active = EXCLUDED.is_active,
			fetched_at = NOW(),
			expires_at = EXCLUDED.expires_at
	`, strings.ToLower(strings.TrimSpace(entry.Email)), entry.SnowSysID, entry.SnowUserName, entry.IsActive, entry.ExpiresAt)
	return err
}

// PurgeExpiredUserCache deletes expired rows from snow_user_cache.
func (r *ServiceNowRepository) PurgeExpiredUserCache(ctx context.Context) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM snow_user_cache WHERE expires_at <= NOW()`)
	return err
}

// FetchOpenIncidentSysIDsForPoll selects up to limit open linked ServiceNow incident sys_ids oldest-synced first.
func (r *ServiceNowRepository) FetchOpenIncidentSysIDsForPoll(ctx context.Context, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT ON (snow_sys_id) snow_sys_id
		FROM servicenow_incidents
		WHERE sync_status IN ('created', 'linked')
		  AND snow_state IN (1, 2, 3)
		  AND snow_sys_id IS NOT NULL
		ORDER BY snow_sys_id, snow_state_synced_at ASC NULLS FIRST
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sysIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		sysIDs = append(sysIDs, id)
	}
	return sysIDs, rows.Err()
}

// FetchPendingResolutionNotes finds resolved alerts with open tickets that have not received an auto resolution note.
func (r *ServiceNowRepository) FetchPendingResolutionNotes(ctx context.Context, limit int) ([]model.PendingResolutionNote, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.pool.Query(ctx, `
		SELECT s.id::text, a.id::text, s.snow_sys_id, s.incident_number, a.alert_name, a.resolved_at
		FROM alerts a
		INNER JOIN servicenow_incidents s ON s.alert_id = a.id
		WHERE a.state = 'resolved'
		  AND s.sync_status IN ('created', 'linked')
		  AND s.snow_state IN (1, 2, 3)
		  AND s.resolution_note_posted_at IS NULL
		  AND s.snow_sys_id IS NOT NULL
		ORDER BY a.resolved_at ASC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []model.PendingResolutionNote
	for rows.Next() {
		var item model.PendingResolutionNote
		if err := rows.Scan(&item.IncidentID, &item.AlertID, &item.SnowSysID, &item.IncidentNumber, &item.AlertName, &item.ResolvedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// MarkResolutionNotePosted stamps resolution_note_posted_at and logs snow_work_note_posted.
func (r *ServiceNowRepository) MarkResolutionNotePosted(ctx context.Context, incidentID, alertID, noteText string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	var lockedStateStr string
	if err := tx.QueryRow(ctx, `SELECT state::text FROM alerts WHERE id = $1::uuid FOR UPDATE`, alertID).Scan(&lockedStateStr); err != nil {
		return err
	}
	resolved := model.AlertState(lockedStateStr)

	if _, err := tx.Exec(ctx, `
		UPDATE servicenow_incidents
		SET resolution_note_posted_at = NOW()
		WHERE id = $1::uuid AND resolution_note_posted_at IS NULL
	`, incidentID); err != nil {
		return err
	}

	if err := insertAuditLogTx(ctx, tx, auditEntry{
		AlertID:       alertID,
		EventType:     model.AuditSnowWorkNotePosted,
		ActorType:     model.ActorSystem,
		ActorLogin:    "system",
		PreviousState: &resolved,
		NewState:      &resolved,
		Comment:       &noteText,
		Metadata:      map[string]any{"reason": "alert_resolved_open_ticket"},
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// FetchGaugeCounts retrieves current queue and drift counts for Prometheus gauges.
func (r *ServiceNowRepository) FetchGaugeCounts(ctx context.Context) (model.GaugeCounts, error) {
	var c model.GaugeCounts
	err := r.pool.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE a.state = 'firing') AS firing,
			COUNT(*) FILTER (WHERE a.state = 'acknowledged') AS acknowledged,
			COUNT(*) FILTER (WHERE a.state = 'dismissed') AS dismissed,
			COUNT(*) FILTER (WHERE a.state = 'resolved' AND s.sync_status IN ('created', 'linked') AND s.snow_state IN (1, 2, 3)) AS resolved_open_ticket,
			COUNT(*) FILTER (WHERE a.state IN ('firing', 'acknowledged') AND s.sync_status IN ('created', 'linked') AND s.snow_state IN (6, 7, 8)) AS active_closed_ticket
		FROM alerts a
		LEFT JOIN servicenow_incidents s ON s.alert_id = a.id
	`).Scan(&c.Firing, &c.Acknowledged, &c.Dismissed, &c.ResolvedWithOpenTicket, &c.ActiveWithClosedTicket)
	return c, err
}

func getIncidentByAlertTx(ctx context.Context, tx pgx.Tx, alertID string) (*model.ServiceNowIncident, error) {
	var inc model.ServiceNowIncident
	var syncStatusStr, callerModeStr, callerResStr string
	err := tx.QueryRow(ctx, `
		SELECT id::text, alert_id::text, correlation_id, sync_status::text,
		       snow_sys_id, incident_number, incident_url,
		       snow_state, snow_state_label, priority, assignment_group,
		       caller_mode::text, caller_resolution_status::text, caller_sys_id, caller_fallback_reason,
		       snow_opened_at, snow_resolved_at, snow_closed_at, snow_state_synced_at, resolution_note_posted_at
		FROM servicenow_incidents
		WHERE alert_id = $1::uuid
	`, alertID).Scan(
		&inc.ID, &inc.AlertID, &inc.CorrelationID, &syncStatusStr,
		&inc.SnowSysID, &inc.IncidentNumber, &inc.IncidentURL,
		&inc.SnowState, &inc.SnowStateLabel, &inc.Priority, &inc.AssignmentGroup,
		&callerModeStr, &callerResStr, &inc.CallerSysID, &inc.CallerFallbackReason,
		&inc.SnowOpenedAt, &inc.SnowResolvedAt, &inc.SnowClosedAt, &inc.SnowStateSyncedAt, &inc.ResolutionNotePostedAt,
	)
	if err != nil {
		return nil, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to reload incident.").WithCause(err)
	}
	inc.SyncStatus = model.SnowSyncStatus(syncStatusStr)
	inc.CallerMode = model.SnowCallerMode(callerModeStr)
	inc.CallerResolutionStatus = model.SnowCallerResolutionStatus(callerResStr)
	return &inc, nil
}
