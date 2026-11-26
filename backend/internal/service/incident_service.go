package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/oriraz/grafana-snow-integration/backend/internal/apierror"
	"github.com/oriraz/grafana-snow-integration/backend/internal/auth"
	"github.com/oriraz/grafana-snow-integration/backend/internal/config"
	"github.com/oriraz/grafana-snow-integration/backend/internal/model"
	"github.com/oriraz/grafana-snow-integration/backend/internal/servicenow"
	"github.com/oriraz/grafana-snow-integration/backend/internal/store"
)

const metadataCacheTTL = 5 * time.Minute

var snowSysIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// IncidentRepository abstracts PostgreSQL operations needed by IncidentService.
type IncidentRepository interface {
	ReserveIncidentCreation(ctx context.Context, alertID string, op auth.OperatorContext, req model.CreateIncidentRequest, allowRetrospective bool, idem store.IdempotencyMeta) (*store.IncidentReservation, error)
	FinalizeIncidentCreated(ctx context.Context, alertID, incidentID string, alertState model.AlertState, op auth.OperatorContext, res store.SnowCreateResult, caller store.CallerResolution, adopted bool, idemKey string, warnings []string) (*model.ServiceNowIncident, error)
	FinalizeIncidentFailed(ctx context.Context, alertID, incidentID string, alertState model.AlertState, op auth.OperatorContext, errMsg, idemKey string) error
	ReserveBulkIncidentCreation(ctx context.Context, alertIDs []string, op auth.OperatorContext, req model.CreateBulkIncidentRequest, allowTriage bool, idem store.IdempotencyMeta) (*store.BulkIncidentReservation, error)
	FinalizeBulkIncidentCreated(ctx context.Context, resv *store.BulkIncidentReservation, op auth.OperatorContext, res store.SnowCreateResult, caller store.CallerResolution, adopted bool, warnings []string) (*model.ServiceNowIncident, error)
	FinalizeBulkIncidentFailed(ctx context.Context, resv *store.BulkIncidentReservation, op auth.OperatorContext, errMsg string) error
	SaveLinkedIncident(ctx context.Context, alertID string, op auth.OperatorContext, snowInc store.SnowCreateResult, allowRetrospective bool) (*model.IncidentSyncResponse, error)
	GetAlertAndIncident(ctx context.Context, alertID string) (model.AlertState, *model.ServiceNowIncident, error)
	UpdateIncidentForAlert(ctx context.Context, alertID string, op auth.OperatorContext, update store.SnowSyncUpdate) (*model.IncidentSyncResponse, error)
	UpdateIncidentsBySysID(ctx context.Context, sysID string, update store.SnowSyncUpdate, actorType model.ActorType, actorLogin string, logOnlyOnChange bool) (int, error)
	RecordWorkNotePosted(ctx context.Context, alertID string, alertState model.AlertState, op auth.OperatorContext, noteText string, incidentNumber *string) (*model.AuditEvent, error)
	GetIncidentActiveStatus(ctx context.Context, snowSysID string) (*model.IncidentActiveStatusResponse, error)
	GetAutoReopenState(ctx context.Context, snowSysID string) (*store.AutoReopenState, error)
	RecordIncidentAutoReopened(ctx context.Context, snowSysID string, update store.SnowSyncUpdate, workNote, triggerSource string, activeAlertCount int) error
	UpsertTeamAssignmentGroup(ctx context.Context, team, assignmentGroup string, op auth.OperatorContext) error
	LookupTeamAssignmentGroup(ctx context.Context, team string) (string, error)
	ListTeamAssignmentGroups(ctx context.Context) (map[string]string, error)
}

// SnowTableClient abstracts ServiceNow Table API methods needed by IncidentService.
type SnowTableClient interface {
	FindIncidentByCorrelationID(ctx context.Context, correlationID string) (*servicenow.IncidentRecord, error)
	FindIncidentByNumber(ctx context.Context, number string) (*servicenow.IncidentRecord, error)
	GetIncidentsBySysIDs(ctx context.Context, operation string, sysIDs []string) ([]servicenow.IncidentRecord, error)
	CreateIncident(ctx context.Context, payload servicenow.CreateIncidentPayload) (*servicenow.IncidentRecord, error)
	PostWorkNote(ctx context.Context, snowSysID, workNotes string) error
	ReopenIncident(ctx context.Context, snowSysID, workNotes string) (*servicenow.IncidentRecord, error)
	ListAssignmentGroups(ctx context.Context) ([]model.AssignmentGroupInfo, error)
}

// CallerLookup abstracts SSO caller resolution with fallback.
type CallerLookup interface {
	Resolve(ctx context.Context, mode model.SnowCallerMode, op auth.OperatorContext) store.CallerResolution
}

// IncidentService coordinates two-phase ServiceNow incident creation, linking, sync, and inbound webhooks.
type IncidentService struct {
	repo        IncidentRepository
	snowClient  SnowTableClient
	caller      CallerLookup
	cfg         *config.Config
	logWriter   func(context.Context, model.WebhookIngestionLogEntry) error
	logger      *slog.Logger

	metaMu          sync.Mutex
	cachedGroups    []model.AssignmentGroupInfo
	groupsFetchedAt time.Time

	reopenMu    sync.Mutex
	reopenLocks map[string]*sync.Mutex
}

// NewIncidentService constructs an IncidentService.
func NewIncidentService(
	repo IncidentRepository,
	snowClient SnowTableClient,
	caller CallerLookup,
	cfg *config.Config,
	logWriter func(context.Context, model.WebhookIngestionLogEntry) error,
	logger *slog.Logger,
) *IncidentService {
	return &IncidentService{
		repo:        repo,
		snowClient:  snowClient,
		caller:      caller,
		cfg:         cfg,
		logWriter:   logWriter,
		logger:      logger,
		reopenLocks: make(map[string]*sync.Mutex),
	}
}

// CreateIncident executes two-phase incident reservation + ServiceNow Table API creation,
// honoring Idempotency-Key replays when provided.
func (s *IncidentService) CreateIncident(
	ctx context.Context,
	alertID string,
	op auth.OperatorContext,
	req model.CreateIncidentRequest,
	idempotencyKey string,
) (*model.CreateIncidentResponse, error) {
	req.ShortDescription = strings.TrimSpace(req.ShortDescription)
	if len(req.ShortDescription) < 5 || len(req.ShortDescription) > 160 {
		return nil, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "short_description must be between 5 and 160 characters.")
	}
	if req.Impact < 1 || req.Impact > 3 {
		return nil, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "impact must be 1, 2, or 3.")
	}
	if req.Urgency < 1 || req.Urgency > 3 {
		return nil, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "urgency must be 1, 2, or 3.")
	}
	if req.CallerMode == "" {
		req.CallerMode = model.CallerModeSSOUser
	} else if req.CallerMode != model.CallerModeSSOUser && req.CallerMode != model.CallerModeGenericNOC {
		return nil, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "caller_mode must be \"sso_user\" or \"generic_noc\".")
	}
	req.AssignmentGroup = strings.TrimSpace(req.AssignmentGroup)
	req.Category = strings.TrimSpace(req.Category)
	if req.Category == "" {
		req.Category = s.cfg.DefaultCategory
	}

	idemMeta := buildSingleIdempotencyMeta(idempotencyKey, alertID, req)

	// Phase 1: Tx 1 reserve pending row (no HTTP inside DB transaction).
	resv, err := s.repo.ReserveIncidentCreation(ctx, alertID, op, req, s.cfg.AllowRetrospectiveIncident, idemMeta)
	if err != nil {
		return nil, err
	}
	if resv.Replayed && resv.ReplayResponse != nil {
		resv.ReplayResponse.Replayed = true
		return resv.ReplayResponse, nil
	}

	// Phase 2: External ServiceNow calls outside DB transaction.
	callerRes := s.caller.Resolve(ctx, req.CallerMode, op)

	if resv.IsRetry {
		existing, lookupErr := s.snowClient.FindIncidentByCorrelationID(ctx, alertID)
		if lookupErr != nil {
			_ = s.repo.FinalizeIncidentFailed(ctx, alertID, resv.IncidentID, resv.Alert.State, op, lookupErr.Error(), idemMeta.Key)
			return nil, lookupErr
		}
		if existing != nil {
			warnings := buildCallerWarnings(callerRes, op)
			inc, finErr := s.repo.FinalizeIncidentCreated(ctx, alertID, resv.IncidentID, resv.Alert.State, op, existing.ToCreateResult(), callerRes, true, idemMeta.Key, warnings)
			if finErr != nil {
				return nil, finErr
			}
			s.recordLearnedTeamGroup(ctx, []model.AlertDetail{resv.Alert}, req.AssignmentGroup, existing.AssignmentGroup, op)
			return &model.CreateIncidentResponse{
				AlertID:            alertID,
				ServiceNowIncident: inc,
				Warnings:           warnings,
			}, nil
		}
	}

	desc := strings.TrimSpace(req.Description)
	if desc == "" {
		desc = formatDefaultDescription(s.cfg.GrafanaPublicURL, resv.Alert)
	}
	assignGroup := req.AssignmentGroup
	if assignGroup == "" {
		assignGroup = s.resolveDefaultAssignmentGroup(ctx, []model.AlertDetail{resv.Alert}, s.cfg.DefaultAssignmentGroup)
	}

	payload := servicenow.CreateIncidentPayload{
		ShortDescription:   req.ShortDescription,
		Description:        desc,
		Impact:             fmt.Sprintf("%d", req.Impact),
		Urgency:            fmt.Sprintf("%d", req.Urgency),
		AssignmentGroup:    assignGroup,
		Category:           req.Category,
		CallerID:           callerRes.SysID,
		CorrelationID:      alertID,
		CorrelationDisplay: "Grafana NOC Alert Manager",
		ContactType:        "monitoring",
		WorkNotes:          formatInitialWorkNotes(op, req.WorkNotes, callerRes),
	}

	created, createErr := s.snowClient.CreateIncident(ctx, payload)
	if createErr != nil {
		var snowErr *servicenow.SnowError
		if errors.As(createErr, &snowErr) && snowErr.IsCallerIDRejection() && callerRes.SysID != s.cfg.DefaultCallerSysID {
			reason := fmt.Sprintf("ServiceNow rejected caller_id (%s); fell back to generic service user.", callerRes.SysID)
			callerRes = store.CallerResolution{
				SysID:            s.cfg.DefaultCallerSysID,
				ResolutionStatus: model.CallerResFallbackNOC,
				FallbackReason:   &reason,
			}
			payload.CallerID = s.cfg.DefaultCallerSysID
			payload.WorkNotes = formatInitialWorkNotes(op, req.WorkNotes, callerRes)
			created, createErr = s.snowClient.CreateIncident(ctx, payload)
		}
	}

	if createErr != nil {
		_ = s.repo.FinalizeIncidentFailed(ctx, alertID, resv.IncidentID, resv.Alert.State, op, createErr.Error(), idemMeta.Key)
		return nil, createErr
	}

	// Phase 3: Tx 2 finalize created incident.
	warnings := buildCallerWarnings(callerRes, op)
	inc, finErr := s.repo.FinalizeIncidentCreated(ctx, alertID, resv.IncidentID, resv.Alert.State, op, created.ToCreateResult(), callerRes, false, idemMeta.Key, warnings)
	if finErr != nil {
		return nil, finErr
	}
	s.recordLearnedTeamGroup(ctx, []model.AlertDetail{resv.Alert}, assignGroup, created.AssignmentGroup, op)

	return &model.CreateIncidentResponse{
		AlertID:            alertID,
		ServiceNowIncident: inc,
		Warnings:           warnings,
	}, nil
}

// CreateBulkIncident creates one ServiceNow incident covering multiple selected alert episodes,
// auto-acknowledging any selected firing alerts under a single deterministic reservation tx.
func (s *IncidentService) CreateBulkIncident(
	ctx context.Context,
	op auth.OperatorContext,
	req model.CreateBulkIncidentRequest,
	idempotencyKey string,
) (*model.CreateBulkIncidentResponse, error) {
	if len(req.AlertIDs) == 0 || len(req.AlertIDs) > 50 {
		return nil, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "alert_ids must contain between 1 and 50 alert IDs.")
	}
	for _, rawID := range req.AlertIDs {
		if !model.IsValidUUID(strings.TrimSpace(rawID)) {
			return nil, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "All alert_ids must be valid UUIDs.")
		}
	}

	req.ShortDescription = strings.TrimSpace(req.ShortDescription)
	if len(req.ShortDescription) < 5 || len(req.ShortDescription) > 160 {
		return nil, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "short_description must be between 5 and 160 characters.")
	}
	if req.Impact < 1 || req.Impact > 3 {
		return nil, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "impact must be 1, 2, or 3.")
	}
	if req.Urgency < 1 || req.Urgency > 3 {
		return nil, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "urgency must be 1, 2, or 3.")
	}
	if req.CallerMode == "" {
		req.CallerMode = model.CallerModeSSOUser
	} else if req.CallerMode != model.CallerModeSSOUser && req.CallerMode != model.CallerModeGenericNOC {
		return nil, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "caller_mode must be \"sso_user\" or \"generic_noc\".")
	}
	req.AssignmentGroup = strings.TrimSpace(req.AssignmentGroup)
	req.Category = strings.TrimSpace(req.Category)
	if req.Category == "" {
		req.Category = s.cfg.DefaultCategory
	}

	idemMeta := buildBulkIdempotencyMeta(idempotencyKey, req)

	allowTriage := auth.HasPermission(op, auth.PermAlertsTriage)
	resv, err := s.repo.ReserveBulkIncidentCreation(ctx, req.AlertIDs, op, req, allowTriage, idemMeta)
	if err != nil {
		return nil, err
	}
	if resv.Replayed && resv.ReplayResponse != nil {
		resv.ReplayResponse.Replayed = true
		return resv.ReplayResponse, nil
	}

	resolvedIDs := make([]string, 0, len(resv.Alerts))
	for _, a := range resv.Alerts {
		resolvedIDs = append(resolvedIDs, a.ID)
	}

	callerRes := s.caller.Resolve(ctx, req.CallerMode, op)

	if resv.IsRetry || len(resv.RetryCorrelationIDs) > 0 {
		candidates := resv.RetryCorrelationIDs
		if len(candidates) == 0 {
			candidates = []string{resv.PrimaryAlertID}
		}
		var adoptedRec *servicenow.IncidentRecord
		for _, corrID := range candidates {
			existing, lookupErr := s.snowClient.FindIncidentByCorrelationID(ctx, corrID)
			if lookupErr != nil {
				_ = s.repo.FinalizeBulkIncidentFailed(ctx, resv, op, lookupErr.Error())
				return nil, lookupErr
			}
			if existing != nil {
				adoptedRec = existing
				break
			}
		}
		if adoptedRec != nil {
			warnings := buildCallerWarnings(callerRes, op)
			inc, finErr := s.repo.FinalizeBulkIncidentCreated(ctx, resv, op, adoptedRec.ToCreateResult(), callerRes, true, warnings)
			if finErr != nil {
				return nil, finErr
			}
			s.recordLearnedTeamGroup(ctx, resv.Alerts, req.AssignmentGroup, adoptedRec.AssignmentGroup, op)
			return &model.CreateBulkIncidentResponse{
				AlertIDs:           resolvedIDs,
				ServiceNowIncident: inc,
				Warnings:           warnings,
			}, nil
		}
	}

	desc := strings.TrimSpace(req.Description)
	if desc == "" {
		desc = formatBulkDefaultDescription(s.cfg.GrafanaPublicURL, resv.Alerts)
	}
	assignGroup := req.AssignmentGroup
	if assignGroup == "" {
		assignGroup = s.resolveDefaultAssignmentGroup(ctx, resv.Alerts, s.cfg.DefaultAssignmentGroup)
	}

	payload := servicenow.CreateIncidentPayload{
		ShortDescription:   req.ShortDescription,
		Description:        desc,
		Impact:             fmt.Sprintf("%d", req.Impact),
		Urgency:            fmt.Sprintf("%d", req.Urgency),
		AssignmentGroup:    assignGroup,
		Category:           req.Category,
		CallerID:           callerRes.SysID,
		CorrelationID:      resv.PrimaryAlertID,
		CorrelationDisplay: "Grafana NOC Alert Manager",
		ContactType:        "monitoring",
		WorkNotes:          formatInitialWorkNotes(op, req.WorkNotes, callerRes),
	}

	created, createErr := s.snowClient.CreateIncident(ctx, payload)
	if createErr != nil {
		var snowErr *servicenow.SnowError
		if errors.As(createErr, &snowErr) && snowErr.IsCallerIDRejection() && callerRes.SysID != s.cfg.DefaultCallerSysID {
			reason := fmt.Sprintf("ServiceNow rejected caller_id (%s); fell back to generic service user.", callerRes.SysID)
			callerRes = store.CallerResolution{
				SysID:            s.cfg.DefaultCallerSysID,
				ResolutionStatus: model.CallerResFallbackNOC,
				FallbackReason:   &reason,
			}
			payload.CallerID = s.cfg.DefaultCallerSysID
			payload.WorkNotes = formatInitialWorkNotes(op, req.WorkNotes, callerRes)
			created, createErr = s.snowClient.CreateIncident(ctx, payload)
		}
	}

	if createErr != nil {
		_ = s.repo.FinalizeBulkIncidentFailed(ctx, resv, op, createErr.Error())
		return nil, createErr
	}

	warnings := buildCallerWarnings(callerRes, op)
	inc, finErr := s.repo.FinalizeBulkIncidentCreated(ctx, resv, op, created.ToCreateResult(), callerRes, false, warnings)
	if finErr != nil {
		return nil, finErr
	}
	s.recordLearnedTeamGroup(ctx, resv.Alerts, assignGroup, created.AssignmentGroup, op)

	return &model.CreateBulkIncidentResponse{
		AlertIDs:           resolvedIDs,
		ServiceNowIncident: inc,
		Warnings:           warnings,
	}, nil
}

// LinkIncident verifies an existing ServiceNow incident and links it to alertID.
func (s *IncidentService) LinkIncident(
	ctx context.Context,
	alertID string,
	op auth.OperatorContext,
	req model.LinkIncidentRequest,
) (*model.IncidentSyncResponse, error) {
	num, err := servicenow.ValidateIncidentNumber(req.IncidentNumber)
	if err != nil {
		return nil, err
	}

	found, err := s.snowClient.FindIncidentByNumber(ctx, num)
	if err != nil {
		return nil, err
	}
	if found == nil {
		return nil, apierror.New(http.StatusNotFound, apierror.CodeIncidentNotFound, fmt.Sprintf("ServiceNow incident %s was not found.", num))
	}

	resp, err := s.repo.SaveLinkedIncident(ctx, alertID, op, found.ToCreateResult(), s.cfg.AllowRetrospectiveIncident)
	if err != nil {
		return nil, err
	}

	if req.PostWorkNote {
		note := fmt.Sprintf("[Grafana NOC — %s] Linked alert episode %s in Grafana.", formatOperatorAttribution(op), alertID)
		if postErr := s.snowClient.PostWorkNote(ctx, found.SysID, note); postErr != nil && s.logger != nil {
			s.logger.Warn("failed posting link work note to servicenow",
				slog.String("component", "incident_service"),
				slog.String("alert_id", alertID),
				slog.String("incident_number", found.Number),
				slog.String("error", postErr.Error()),
			)
		}
	}

	return resp, nil
}

// SyncIncident refreshes linked incident state from ServiceNow.
func (s *IncidentService) SyncIncident(ctx context.Context, alertID string, op auth.OperatorContext) (*model.IncidentSyncResponse, error) {
	_, inc, err := s.repo.GetAlertAndIncident(ctx, alertID)
	if err != nil {
		return nil, err
	}

	records, err := s.snowClient.GetIncidentsBySysIDs(ctx, "get", []string{*inc.SnowSysID})
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, apierror.New(http.StatusNotFound, apierror.CodeIncidentNotFound, "Linked incident was not found in ServiceNow.")
	}

	return s.repo.UpdateIncidentForAlert(ctx, alertID, op, records[0].ToSyncUpdate())
}

// PostWorkNote appends an operator-attributed work note to the linked ServiceNow incident.
func (s *IncidentService) PostWorkNote(
	ctx context.Context,
	alertID string,
	op auth.OperatorContext,
	req model.WorkNoteRequest,
) (*model.AuditEvent, error) {
	note := strings.TrimSpace(req.WorkNotes)
	if note == "" || len(note) > 4000 {
		return nil, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "work_notes must be between 1 and 4000 characters.")
	}

	alertState, inc, err := s.repo.GetAlertAndIncident(ctx, alertID)
	if err != nil {
		return nil, err
	}

	formatted := fmt.Sprintf("[Grafana NOC — %s] %s", formatOperatorAttribution(op), note)
	if err := s.snowClient.PostWorkNote(ctx, *inc.SnowSysID, formatted); err != nil {
		return nil, err
	}

	return s.repo.RecordWorkNotePosted(ctx, alertID, alertState, op, formatted, inc.IncidentNumber)
}

// GetMetadata returns modal dropdown metadata with graceful fallback if ServiceNow is unreachable.
func (s *IncidentService) GetMetadata(ctx context.Context) model.ServiceNowMetadataResponse {
	categories := []string{"Infrastructure", "Database", "Network", "Application", "Security"}
	hasDefaultCat := false
	for _, c := range categories {
		if strings.EqualFold(c, s.cfg.DefaultCategory) {
			hasDefaultCat = true
			break
		}
	}
	if s.cfg.DefaultCategory != "" && !hasDefaultCat {
		categories = append([]string{s.cfg.DefaultCategory}, categories...)
	}

	groups, reachable := s.getCachedOrLiveGroups(ctx)
	teamGroups, err := s.repo.ListTeamAssignmentGroups(ctx)
	if err != nil || teamGroups == nil {
		teamGroups = map[string]string{}
	}
	return model.ServiceNowMetadataResponse{
		DefaultAssignmentGroup: s.cfg.DefaultAssignmentGroup,
		DefaultCategory:        s.cfg.DefaultCategory,
		Categories:             categories,
		DefaultCallerMode:      model.CallerModeSSOUser,
		DefaultCallerUsername:  s.cfg.DefaultCallerUsername,
		AssignmentGroups:       groups,
		TeamAssignmentGroups:   teamGroups,
		SnowReachable:          reachable,
	}
}

// GetIncidentActiveStatus reports whether any active alerts remain linked to snowSysID.
func (s *IncidentService) GetIncidentActiveStatus(ctx context.Context, snowSysID string) (*model.IncidentActiveStatusResponse, error) {
	trimmed := strings.TrimSpace(snowSysID)
	if !snowSysIDPattern.MatchString(trimmed) {
		return nil, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "snow_sys_id must be 1-64 alphanumeric, underscore, or hyphen characters.")
	}
	return s.repo.GetIncidentActiveStatus(ctx, trimmed)
}

func (s *IncidentService) getCachedOrLiveGroups(ctx context.Context) ([]model.AssignmentGroupInfo, bool) {
	s.metaMu.Lock()
	cached := slices.Clone(s.cachedGroups)
	fresh := len(cached) > 0 && time.Since(s.groupsFetchedAt) < metadataCacheTTL
	s.metaMu.Unlock()

	if fresh {
		return cached, true
	}

	groups, err := s.snowClient.ListAssignmentGroups(ctx)
	if err != nil {
		if cached == nil {
			cached = []model.AssignmentGroupInfo{}
		}
		return cached, false
	}

	s.metaMu.Lock()
	s.cachedGroups = slices.Clone(groups)
	s.groupsFetchedAt = time.Now()
	s.metaMu.Unlock()
	return groups, true
}

// ProcessInboundWebhook applies a ServiceNow Business Rule state-change webhook.
func (s *IncidentService) ProcessInboundWebhook(ctx context.Context, rawBody []byte, payload model.ServiceNowWebhookPayload) (int, error) {
	started := time.Now()
	hash := sha256Hex(rawBody)

	sysID := strings.TrimSpace(payload.SysID)
	if sysID == "" {
		errMsg := "sys_id is required"
		s.logInboundWebhook(ctx, hash, http.StatusBadRequest, started, &errMsg)
		return 0, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "sys_id is required.")
	}
	if !model.IsValidSnowState(payload.State) {
		errMsg := "invalid ServiceNow incident state"
		s.logInboundWebhook(ctx, hash, http.StatusBadRequest, started, &errMsg)
		return 0, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "state must be one of 1, 2, 3, 6, 7, or 8.")
	}

	label := strings.TrimSpace(payload.StateLabel)
	if label == "" {
		label = model.SnowStateLabel(payload.State)
	}

	update := store.SnowSyncUpdate{
		State:           payload.State,
		StateLabel:      label,
		Priority:        payload.Priority,
		AssignmentGroup: payload.AssignmentGroup,
		ResolvedAt:      parseOptionalTimePtr(payload.ResolvedAt),
		ClosedAt:        parseOptionalTimePtr(payload.ClosedAt),
		CloseNotes:      strings.TrimSpace(payload.CloseNotes),
		UpdatedBy:       strings.TrimSpace(payload.UpdatedBy),
	}

	updatedCount, err := s.repo.UpdateIncidentsBySysID(ctx, sysID, update, model.ActorServiceNow, "servicenow", false)
	if err != nil {
		errMsg := err.Error()
		s.logInboundWebhook(ctx, hash, http.StatusServiceUnavailable, started, &errMsg)
		return 0, err
	}

	if updatedCount > 0 && payload.State == 6 {
		s.HandlePrematureResolvedState(ctx, sysID, "webhook")
	}

	s.logInboundWebhook(ctx, hash, http.StatusOK, started, nil)
	return updatedCount, nil
}

// HandlePrematureResolvedState auto-reopens a ServiceNow incident moved to 6 (Resolved) if
// any linked alert remains active (firing/acknowledged), subject to cooldown and attempt limits.
func (s *IncidentService) HandlePrematureResolvedState(ctx context.Context, snowSysID, triggerSource string) {
	trimmed := strings.TrimSpace(snowSysID)
	if trimmed == "" {
		return
	}

	mu := s.acquireReopenLock(trimmed)
	mu.Lock()
	defer mu.Unlock()

	st, err := s.repo.GetAutoReopenState(ctx, trimmed)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("failed loading auto-reopen state for incident",
				slog.String("component", "incident_service"),
				slog.String("snow_sys_id", trimmed),
				slog.String("trigger_source", triggerSource),
				slog.String("error", err.Error()),
			)
		}
		return
	}
	if st == nil || len(st.ActiveAlerts) == 0 {
		return
	}

	maxAttempts := s.autoReopenMaxAttempts()
	if st.MaxAutoReopenCount >= maxAttempts {
		if s.logger != nil {
			s.logger.Warn("auto-reopen attempt cap reached; leaving incident resolved for NOC warning badge",
				slog.String("component", "incident_service"),
				slog.String("snow_sys_id", trimmed),
				slog.String("incident_number", st.IncidentNumber),
				slog.Int("auto_reopen_count", st.MaxAutoReopenCount),
				slog.Int("max_attempts", maxAttempts),
			)
		}
		return
	}

	cooldown := s.autoReopenCooldown()
	if st.LastAutoReopenedAt != nil && time.Since(*st.LastAutoReopenedAt) < cooldown {
		if s.logger != nil {
			s.logger.Warn("auto-reopen cooldown active; skipping ServiceNow reopen",
				slog.String("component", "incident_service"),
				slog.String("snow_sys_id", trimmed),
				slog.String("incident_number", st.IncidentNumber),
				slog.Duration("cooldown", cooldown),
			)
		}
		return
	}

	names := make([]string, 0, min(len(st.ActiveAlerts), 5))
	seen := make(map[string]struct{}, 5)
	for _, a := range st.ActiveAlerts {
		if _, ok := seen[a.AlertName]; ok {
			continue
		}
		seen[a.AlertName] = struct{}{}
		names = append(names, a.AlertName)
		if len(names) >= 5 {
			break
		}
	}
	note := fmt.Sprintf(
		"[Grafana NOC] Auto-reopened: %d linked alert(s) still firing (%s). Please wait until all linked alerts recover before resolving this incident.",
		len(st.ActiveAlerts),
		strings.Join(names, ", "),
	)

	rec, err := s.snowClient.ReopenIncident(ctx, trimmed, note)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("failed auto-reopening prematurely resolved ServiceNow incident",
				slog.String("component", "incident_service"),
				slog.String("snow_sys_id", trimmed),
				slog.String("incident_number", st.IncidentNumber),
				slog.String("trigger_source", triggerSource),
				slog.String("error", err.Error()),
			)
		}
		return
	}

	syncUpdate := store.SnowSyncUpdate{
		State:      2,
		StateLabel: model.SnowStateLabel(2),
	}
	if rec != nil {
		if rec.Priority != nil {
			syncUpdate.Priority = rec.Priority
		}
		if rec.AssignmentGroup != nil {
			syncUpdate.AssignmentGroup = rec.AssignmentGroup
		}
	}

	if err := s.repo.RecordIncidentAutoReopened(ctx, trimmed, syncUpdate, note, triggerSource, len(st.ActiveAlerts)); err != nil && s.logger != nil {
		s.logger.Warn("failed recording auto-reopen state in PostgreSQL",
			slog.String("component", "incident_service"),
			slog.String("snow_sys_id", trimmed),
			slog.String("error", err.Error()),
		)
	}
}

func (s *IncidentService) acquireReopenLock(snowSysID string) *sync.Mutex {
	s.reopenMu.Lock()
	defer s.reopenMu.Unlock()
	if s.reopenLocks == nil {
		s.reopenLocks = make(map[string]*sync.Mutex)
	}
	mu, ok := s.reopenLocks[snowSysID]
	if !ok {
		mu = &sync.Mutex{}
		s.reopenLocks[snowSysID] = mu
	}
	return mu
}

func (s *IncidentService) autoReopenCooldown() time.Duration {
	if s.cfg != nil && s.cfg.SnowAutoReopenCooldown > 0 {
		return s.cfg.SnowAutoReopenCooldown
	}
	return 5 * time.Minute
}

func (s *IncidentService) autoReopenMaxAttempts() int {
	if s.cfg != nil && s.cfg.SnowAutoReopenMaxAttempts > 0 {
		return s.cfg.SnowAutoReopenMaxAttempts
	}
	return 3
}

func (s *IncidentService) logInboundWebhook(ctx context.Context, hash string, status int, started time.Time, errMsg *string) {
	if s.logWriter == nil {
		return
	}
	_ = s.logWriter(ctx, model.WebhookIngestionLogEntry{
		Source:       "servicenow",
		PayloadHash:  hash,
		AlertCount:   1,
		HTTPStatus:   status,
		ProcessingMs: int(time.Since(started).Milliseconds()),
		ErrorMessage: errMsg,
	})
}

func (s *IncidentService) resolveDefaultAssignmentGroup(ctx context.Context, alerts []model.AlertDetail, fallback string) string {
	if team := sharedPrometheusTeam(alerts); team != "" {
		if learned, err := s.repo.LookupTeamAssignmentGroup(ctx, team); err == nil && learned != "" {
			return learned
		}
	}
	for _, a := range alerts {
		if g := strings.TrimSpace(a.Labels["assignment_group"]); g != "" {
			return g
		}
	}
	return fallback
}

func sharedPrometheusTeam(alerts []model.AlertDetail) string {
	if len(alerts) == 0 {
		return ""
	}
	first := strings.TrimSpace(alerts[0].Labels["team"])
	if first == "" {
		return ""
	}
	for _, a := range alerts[1:] {
		if strings.TrimSpace(a.Labels["team"]) != first {
			return ""
		}
	}
	return first
}

func (s *IncidentService) recordLearnedTeamGroup(
	ctx context.Context,
	alerts []model.AlertDetail,
	submittedGroup string,
	createdGroup *string,
	op auth.OperatorContext,
) {
	team := sharedPrometheusTeam(alerts)
	if team == "" {
		return
	}
	effective := strings.TrimSpace(submittedGroup)
	if createdGroup != nil && strings.TrimSpace(*createdGroup) != "" {
		effective = strings.TrimSpace(*createdGroup)
	}
	if effective == "" {
		return
	}
	if err := s.repo.UpsertTeamAssignmentGroup(ctx, team, effective, op); err != nil && s.logger != nil {
		s.logger.Warn("failed persisting learned team assignment group mapping",
			slog.String("component", "incident_service"),
			slog.String("team", team),
			slog.String("assignment_group", effective),
			slog.String("error", err.Error()),
		)
	}
}

func formatBulkDefaultDescription(grafanaPublicURL string, alerts []model.AlertDetail) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Grafana Alert Manager — Combined Incident (%d alerts)\n---------------------------------------------------\n", len(alerts))
	for i, a := range alerts {
		env, site, team, _ := model.PopulateDerivedMetadata(a.Labels, a.AssignedTeam)
		fmt.Fprintf(&b, "\n%d. %s (severity=%s", i+1, a.AlertName, a.Severity)
		if env != "" {
			fmt.Fprintf(&b, ", environment=%s", env)
		}
		if site != "" {
			fmt.Fprintf(&b, ", site=%s", site)
		}
		if team != "" {
			fmt.Fprintf(&b, ", team=%s", team)
		}
		fmt.Fprintf(&b, ", starts_at=%s)\n", a.StartsAt.UTC().Format(time.RFC3339))
		if summary := strings.TrimSpace(a.Annotations["summary"]); summary != "" {
			fmt.Fprintf(&b, "   Summary: %s\n", summary)
		}
		if grafanaPublicURL != "" {
			fmt.Fprintf(&b, "   Link: %s/a/noc-alert-manager-app/alerts/%s\n", grafanaPublicURL, a.ID)
		}
	}
	return b.String()
}

func formatDefaultDescription(grafanaPublicURL string, alert model.AlertDetail) string {
	var b strings.Builder
	b.WriteString("Grafana NOC Alert Details\n-------------------------\n")
	fmt.Fprintf(&b, "Alert Name: %s\n", alert.AlertName)
	fmt.Fprintf(&b, "Severity: %s\n", alert.Severity)
	fmt.Fprintf(&b, "Starts At: %s\n", alert.StartsAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "Fingerprint: %s\n", alert.Fingerprint)
	if grafanaPublicURL != "" {
		fmt.Fprintf(&b, "Grafana Link: %s/a/noc-alert-manager-app/alerts/%s\n", grafanaPublicURL, alert.ID)
	}
	if alert.GeneratorURL != nil && *alert.GeneratorURL != "" {
		fmt.Fprintf(&b, "Prometheus Query: %s\n", *alert.GeneratorURL)
	}
	if runbook := strings.TrimSpace(alert.Annotations["runbook_url"]); runbook != "" {
		fmt.Fprintf(&b, "Runbook: %s\n", runbook)
	}
	if len(alert.Labels) > 0 {
		b.WriteString("\nLabels:\n")
		keys := make([]string, 0, len(alert.Labels))
		for k := range alert.Labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "- %s: %s\n", k, alert.Labels[k])
		}
	}
	return b.String()
}

func formatInitialWorkNotes(op auth.OperatorContext, operatorNotes string, caller store.CallerResolution) string {
	var parts []string
	parts = append(parts, fmt.Sprintf("[Grafana NOC] Initiated in Grafana by: %s.", formatOperatorAttribution(op)))
	if strings.TrimSpace(operatorNotes) != "" {
		parts = append(parts, strings.TrimSpace(operatorNotes))
	}
	if caller.FallbackReason != nil && *caller.FallbackReason != "" {
		parts = append(parts, "Caller fallback note: "+*caller.FallbackReason)
	}
	return strings.Join(parts, "\n")
}

func formatOperatorAttribution(op auth.OperatorContext) string {
	name := strings.TrimSpace(op.Name)
	login := strings.TrimSpace(op.Login)
	email := strings.TrimSpace(op.Email)

	primary := name
	if primary == "" {
		primary = login
	}
	if primary == "" {
		primary = email
	}
	if primary == "" {
		return "unknown"
	}

	var extras []string
	addExtra := func(val string) {
		if val == "" || strings.EqualFold(val, primary) {
			return
		}
		for _, existing := range extras {
			if strings.EqualFold(existing, val) {
				return
			}
		}
		extras = append(extras, val)
	}
	addExtra(login)
	addExtra(email)

	if len(extras) == 0 {
		return primary
	}
	return fmt.Sprintf("%s (%s)", primary, strings.Join(extras, ", "))
}

func buildCallerWarnings(caller store.CallerResolution, op auth.OperatorContext) []string {
	if caller.ResolutionStatus != model.CallerResFallbackNOC {
		return nil
	}
	identity := emailOrLogin(op)
	return []string{
		fmt.Sprintf("Incident created using the generic fallback account because %s could not be matched in ServiceNow.", identity),
	}
}

func emailOrLogin(op auth.OperatorContext) string {
	if strings.TrimSpace(op.Email) != "" {
		return op.Email
	}
	return op.Login
}

func parseOptionalTimePtr(s *string) *time.Time {
	if s == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*s)
	if trimmed == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.ParseInLocation(layout, trimmed, time.UTC); err == nil {
			utc := t.UTC()
			return &utc
		}
	}
	return nil
}

func buildSingleIdempotencyMeta(key, alertID string, req model.CreateIncidentRequest) store.IdempotencyMeta {
	key = strings.TrimSpace(key)
	if key == "" {
		return store.IdempotencyMeta{}
	}
	canonical, _ := json.Marshal(struct {
		AlertID          string               `json:"alert_id"`
		ShortDescription string               `json:"short_description"`
		Description      string               `json:"description"`
		Impact           int16                `json:"impact"`
		Urgency          int16                `json:"urgency"`
		AssignmentGroup  string               `json:"assignment_group"`
		Category         string               `json:"category"`
		CallerMode       model.SnowCallerMode `json:"caller_mode"`
		WorkNotes        string               `json:"work_notes"`
	}{
		AlertID:          alertID,
		ShortDescription: req.ShortDescription,
		Description:      strings.TrimSpace(req.Description),
		Impact:           req.Impact,
		Urgency:          req.Urgency,
		AssignmentGroup:  req.AssignmentGroup,
		Category:         req.Category,
		CallerMode:       req.CallerMode,
		WorkNotes:        strings.TrimSpace(req.WorkNotes),
	})
	return store.IdempotencyMeta{
		Key:         key,
		Endpoint:    "POST:/api/v1/alerts/{alert_id}/servicenow/incident",
		RequestHash: sha256Hex(canonical),
	}
}

func buildBulkIdempotencyMeta(key string, req model.CreateBulkIncidentRequest) store.IdempotencyMeta {
	key = strings.TrimSpace(key)
	if key == "" {
		return store.IdempotencyMeta{}
	}
	seen := make(map[string]struct{}, len(req.AlertIDs))
	sortedIDs := make([]string, 0, len(req.AlertIDs))
	for _, raw := range req.AlertIDs {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			sortedIDs = append(sortedIDs, id)
		}
	}
	sort.Strings(sortedIDs)

	canonical, _ := json.Marshal(struct {
		AlertIDs         []string             `json:"alert_ids"`
		ShortDescription string               `json:"short_description"`
		Description      string               `json:"description"`
		Impact           int16                `json:"impact"`
		Urgency          int16                `json:"urgency"`
		AssignmentGroup  string               `json:"assignment_group"`
		Category         string               `json:"category"`
		CallerMode       model.SnowCallerMode `json:"caller_mode"`
		WorkNotes        string               `json:"work_notes"`
	}{
		AlertIDs:         sortedIDs,
		ShortDescription: req.ShortDescription,
		Description:      strings.TrimSpace(req.Description),
		Impact:           req.Impact,
		Urgency:          req.Urgency,
		AssignmentGroup:  req.AssignmentGroup,
		Category:         req.Category,
		CallerMode:       req.CallerMode,
		WorkNotes:        strings.TrimSpace(req.WorkNotes),
	})
	return store.IdempotencyMeta{
		Key:         key,
		Endpoint:    "POST:/api/v1/servicenow/incidents/bulk",
		RequestHash: sha256Hex(canonical),
	}
}
