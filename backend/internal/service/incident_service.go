package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
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

// IncidentRepository abstracts PostgreSQL operations needed by IncidentService.
type IncidentRepository interface {
	ReserveIncidentCreation(ctx context.Context, alertID string, op auth.OperatorContext, req model.CreateIncidentRequest, allowRetrospective bool) (*store.IncidentReservation, error)
	FinalizeIncidentCreated(ctx context.Context, alertID, incidentID string, alertState model.AlertState, op auth.OperatorContext, res store.SnowCreateResult, caller store.CallerResolution, adopted bool) (*model.ServiceNowIncident, error)
	FinalizeIncidentFailed(ctx context.Context, alertID, incidentID string, alertState model.AlertState, op auth.OperatorContext, errMsg string) error
	ReserveBulkIncidentCreation(ctx context.Context, alertIDs []string, op auth.OperatorContext, req model.CreateBulkIncidentRequest, allowTriage bool) (*store.BulkIncidentReservation, error)
	FinalizeBulkIncidentCreated(ctx context.Context, resv *store.BulkIncidentReservation, op auth.OperatorContext, res store.SnowCreateResult, caller store.CallerResolution, adopted bool) (*model.ServiceNowIncident, error)
	FinalizeBulkIncidentFailed(ctx context.Context, resv *store.BulkIncidentReservation, op auth.OperatorContext, errMsg string) error
	SaveLinkedIncident(ctx context.Context, alertID string, op auth.OperatorContext, snowInc store.SnowCreateResult, allowRetrospective bool) (*model.IncidentSyncResponse, error)
	GetAlertAndIncident(ctx context.Context, alertID string) (model.AlertState, *model.ServiceNowIncident, error)
	UpdateIncidentForAlert(ctx context.Context, alertID string, op auth.OperatorContext, update store.SnowSyncUpdate) (*model.IncidentSyncResponse, error)
	UpdateIncidentsBySysID(ctx context.Context, sysID string, update store.SnowSyncUpdate, actorType model.ActorType, actorLogin string, logOnlyOnChange bool) (int, error)
	RecordWorkNotePosted(ctx context.Context, alertID string, alertState model.AlertState, op auth.OperatorContext, noteText string, incidentNumber *string) (*model.AuditEvent, error)
}

// SnowTableClient abstracts ServiceNow Table API methods needed by IncidentService.
type SnowTableClient interface {
	FindIncidentByCorrelationID(ctx context.Context, correlationID string) (*servicenow.IncidentRecord, error)
	FindIncidentByNumber(ctx context.Context, number string) (*servicenow.IncidentRecord, error)
	GetIncidentsBySysIDs(ctx context.Context, operation string, sysIDs []string) ([]servicenow.IncidentRecord, error)
	CreateIncident(ctx context.Context, payload servicenow.CreateIncidentPayload) (*servicenow.IncidentRecord, error)
	PostWorkNote(ctx context.Context, snowSysID, workNotes string) error
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
		repo:       repo,
		snowClient: snowClient,
		caller:     caller,
		cfg:        cfg,
		logWriter:  logWriter,
		logger:     logger,
	}
}

// CreateIncident executes two-phase incident reservation + ServiceNow Table API creation.
func (s *IncidentService) CreateIncident(
	ctx context.Context,
	alertID string,
	op auth.OperatorContext,
	req model.CreateIncidentRequest,
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

	// Phase 1: Tx 1 reserve pending row (no HTTP inside DB transaction).
	resv, err := s.repo.ReserveIncidentCreation(ctx, alertID, op, req, s.cfg.AllowRetrospectiveIncident)
	if err != nil {
		return nil, err
	}

	// Phase 2: External ServiceNow calls outside DB transaction.
	callerRes := s.caller.Resolve(ctx, req.CallerMode, op)

	if resv.IsRetry {
		existing, lookupErr := s.snowClient.FindIncidentByCorrelationID(ctx, alertID)
		if lookupErr != nil {
			_ = s.repo.FinalizeIncidentFailed(ctx, alertID, resv.IncidentID, resv.Alert.State, op, lookupErr.Error())
			return nil, lookupErr
		}
		if existing != nil {
			inc, finErr := s.repo.FinalizeIncidentCreated(ctx, alertID, resv.IncidentID, resv.Alert.State, op, existing.ToCreateResult(), callerRes, true)
			if finErr != nil {
				return nil, finErr
			}
			return &model.CreateIncidentResponse{
				AlertID:            alertID,
				ServiceNowIncident: inc,
				Warnings:           buildCallerWarnings(callerRes, op),
			}, nil
		}
	}

	desc := strings.TrimSpace(req.Description)
	if desc == "" {
		desc = formatDefaultDescription(s.cfg.GrafanaPublicURL, resv.Alert)
	}
	assignGroup := req.AssignmentGroup
	if assignGroup == "" {
		assignGroup = resolveDefaultAssignmentGroup([]model.AlertDetail{resv.Alert}, s.cfg.DefaultAssignmentGroup)
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
		_ = s.repo.FinalizeIncidentFailed(ctx, alertID, resv.IncidentID, resv.Alert.State, op, createErr.Error())
		return nil, createErr
	}

	// Phase 3: Tx 2 finalize created incident.
	inc, finErr := s.repo.FinalizeIncidentCreated(ctx, alertID, resv.IncidentID, resv.Alert.State, op, created.ToCreateResult(), callerRes, false)
	if finErr != nil {
		return nil, finErr
	}

	return &model.CreateIncidentResponse{
		AlertID:            alertID,
		ServiceNowIncident: inc,
		Warnings:           buildCallerWarnings(callerRes, op),
	}, nil
}

// CreateBulkIncident creates one ServiceNow incident covering multiple selected alert episodes,
// auto-acknowledging any selected firing alerts under a single deterministic reservation tx.
func (s *IncidentService) CreateBulkIncident(
	ctx context.Context,
	op auth.OperatorContext,
	req model.CreateBulkIncidentRequest,
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

	allowTriage := auth.HasPermission(op, auth.PermAlertsTriage)
	resv, err := s.repo.ReserveBulkIncidentCreation(ctx, req.AlertIDs, op, req, allowTriage)
	if err != nil {
		return nil, err
	}

	resolvedIDs := make([]string, 0, len(resv.Alerts))
	for _, a := range resv.Alerts {
		resolvedIDs = append(resolvedIDs, a.ID)
	}

	callerRes := s.caller.Resolve(ctx, req.CallerMode, op)

	if resv.IsRetry {
		existing, lookupErr := s.snowClient.FindIncidentByCorrelationID(ctx, resv.PrimaryAlertID)
		if lookupErr != nil {
			_ = s.repo.FinalizeBulkIncidentFailed(ctx, resv, op, lookupErr.Error())
			return nil, lookupErr
		}
		if existing != nil {
			inc, finErr := s.repo.FinalizeBulkIncidentCreated(ctx, resv, op, existing.ToCreateResult(), callerRes, true)
			if finErr != nil {
				return nil, finErr
			}
			return &model.CreateBulkIncidentResponse{
				AlertIDs:           resolvedIDs,
				ServiceNowIncident: inc,
				Warnings:           buildCallerWarnings(callerRes, op),
			}, nil
		}
	}

	desc := strings.TrimSpace(req.Description)
	if desc == "" {
		desc = formatBulkDefaultDescription(s.cfg.GrafanaPublicURL, resv.Alerts)
	}
	assignGroup := req.AssignmentGroup
	if assignGroup == "" {
		assignGroup = resolveDefaultAssignmentGroup(resv.Alerts, s.cfg.DefaultAssignmentGroup)
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

	inc, finErr := s.repo.FinalizeBulkIncidentCreated(ctx, resv, op, created.ToCreateResult(), callerRes, false)
	if finErr != nil {
		return nil, finErr
	}

	return &model.CreateBulkIncidentResponse{
		AlertIDs:           resolvedIDs,
		ServiceNowIncident: inc,
		Warnings:           buildCallerWarnings(callerRes, op),
	}, nil
}

// LinkIncident verifies an existing ServiceNow incident and links it to alertID.
func (s *IncidentService) LinkIncident(
	ctx context.Context,
	alertID string,
	op auth.OperatorContext,
	req model.LinkIncidentRequest,
) (*model.IncidentSyncResponse, error) {
	num := strings.TrimSpace(req.IncidentNumber)
	if num == "" {
		return nil, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "incident_number is required.")
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
		note := fmt.Sprintf("[Grafana NOC — %s (%s)] Linked alert episode %s in Grafana.", op.Login, emailOrLogin(op), alertID)
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

	formatted := fmt.Sprintf("[Grafana NOC — %s (%s)] %s", op.Login, emailOrLogin(op), note)
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
	return model.ServiceNowMetadataResponse{
		DefaultAssignmentGroup: s.cfg.DefaultAssignmentGroup,
		DefaultCategory:        s.cfg.DefaultCategory,
		Categories:             categories,
		DefaultCallerMode:      model.CallerModeSSOUser,
		DefaultCallerUsername:  s.cfg.DefaultCallerUsername,
		AssignmentGroups:       groups,
		SnowReachable:          reachable,
	}
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

	s.logInboundWebhook(ctx, hash, http.StatusOK, started, nil)
	return updatedCount, nil
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

func resolveDefaultAssignmentGroup(alerts []model.AlertDetail, fallback string) string {
	for _, a := range alerts {
		if g := strings.TrimSpace(a.Labels["assignment_group"]); g != "" {
			return g
		}
		if t := strings.TrimSpace(a.Labels["team"]); t != "" {
			return t
		}
		if a.AssignedTeam != nil && strings.TrimSpace(*a.AssignedTeam) != "" {
			return strings.TrimSpace(*a.AssignedTeam)
		}
	}
	return fallback
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
	parts = append(parts, fmt.Sprintf("[Grafana NOC] Initiated in Grafana by: %s (%s).", op.Name, emailOrLogin(op)))
	if strings.TrimSpace(operatorNotes) != "" {
		parts = append(parts, strings.TrimSpace(operatorNotes))
	}
	if caller.FallbackReason != nil && *caller.FallbackReason != "" {
		parts = append(parts, "Caller fallback note: "+*caller.FallbackReason)
	}
	return strings.Join(parts, "\n")
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
