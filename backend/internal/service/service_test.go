package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/oriraz/grafana-snow-integration/backend/internal/apierror"
	"github.com/oriraz/grafana-snow-integration/backend/internal/auth"
	"github.com/oriraz/grafana-snow-integration/backend/internal/config"
	"github.com/oriraz/grafana-snow-integration/backend/internal/model"
	"github.com/oriraz/grafana-snow-integration/backend/internal/observability"
	"github.com/oriraz/grafana-snow-integration/backend/internal/servicenow"
	"github.com/oriraz/grafana-snow-integration/backend/internal/store"
)

type fakeAMStore struct {
	received []model.NormalizedAlert
	outcome  store.ReconcileOutcome
}

func (f *fakeAMStore) ReconcileAlertmanagerBatch(_ context.Context, _ *slog.Logger, alerts []model.NormalizedAlert) (store.ReconcileOutcome, error) {
	f.received = append([]model.NormalizedAlert(nil), alerts...)
	return f.outcome, nil
}

type fakeNoteNotifier struct {
	triggered int
}

func (f *fakeNoteNotifier) Trigger() { f.triggered++ }

func TestValidateAndNormalizeAlertmanagerPayload(t *testing.T) {
	t.Run("deduplicates resolved over firing, maps zero endsAt to nil, sorts deterministically", func(t *testing.T) {
		t1 := time.Date(2026, 11, 25, 16, 10, 0, 0, time.UTC)
		t2 := time.Date(2026, 11, 25, 16, 15, 0, 0, time.UTC)
		zeroTime := time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)

		raw := model.AlertmanagerWebhookPayload{
			Version:         "4",
			GroupKey:        "gk-1",
			Receiver:        "alert-api",
			TruncatedAlerts: 2,
			Alerts: []model.AlertmanagerAlert{
				{
					Status:      "firing",
					Fingerprint: "bbbb",
					StartsAt:    t2,
					EndsAt:      zeroTime,
					Labels:      map[string]string{"alertname": "DiskFull", "severity": "CRITICAL"},
				},
				{
					Status:      "firing",
					Fingerprint: "aaaa",
					StartsAt:    t1,
					EndsAt:      zeroTime,
					Labels:      map[string]string{"alertname": "HighLag"},
				},
				{
					Status:      "resolved",
					Fingerprint: "aaaa",
					StartsAt:    t1,
					EndsAt:      t2,
					Labels:      map[string]string{"alertname": "HighLag"},
				},
			},
		}

		out, err := ValidateAndNormalizeAlertmanagerPayload(&raw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(out) != 2 {
			t.Fatalf("expected 2 deduplicated alerts, got %d", len(out))
		}
		if out[0].Fingerprint != "aaaa" || out[0].Status != model.AlertStateResolved || out[0].Severity != "unknown" {
			t.Fatalf("expected resolved aaaa first with severity unknown, got %+v", out[0])
		}
		if out[1].Fingerprint != "bbbb" || out[1].EndsAt != nil || out[1].Severity != "critical" {
			t.Fatalf("expected firing bbbb second with nil EndsAt and critical severity, got %+v", out[1])
		}
	})

	t.Run("rejects unsupported Alertmanager version", func(t *testing.T) {
		_, err := ValidateAndNormalizeAlertmanagerPayload(&model.AlertmanagerWebhookPayload{
			Version:  "3",
			GroupKey: "gk",
			Receiver: "r",
		})
		if err == nil {
			t.Fatalf("expected error for version 3")
		}
	})
}

func TestAlertmanagerServiceIngestMetricsAndNotifier(t *testing.T) {
	storeFake := &fakeAMStore{
		outcome: store.ReconcileOutcome{
			Processed:             1,
			Resolved:              1,
			HasOpenTicketResolved: true,
		},
	}
	notifier := &fakeNoteNotifier{}
	metrics := observability.NewMetrics()
	var loggedEntries []model.WebhookIngestionLogEntry

	svc := NewAlertmanagerService(storeFake, func(_ context.Context, e model.WebhookIngestionLogEntry) error {
		loggedEntries = append(loggedEntries, e)
		return nil
	}, notifier, metrics, nil)

	payload, _ := json.Marshal(model.AlertmanagerWebhookPayload{
		Version:         "4",
		GroupKey:        "gk-1",
		Receiver:        "alert-api",
		TruncatedAlerts: 3,
		Alerts: []model.AlertmanagerAlert{
			{
				Status:      "resolved",
				Fingerprint: "fp1",
				StartsAt:    time.Now().Add(-5 * time.Minute).UTC(),
				EndsAt:      time.Now().UTC(),
				Labels:      map[string]string{"alertname": "CPUHigh", "severity": "warning"},
			},
		},
	})

	resp, err := svc.Ingest(context.Background(), payload)
	if err != nil {
		t.Fatalf("Ingest error: %v", err)
	}
	if !resp.TruncatedAlertsWarning || resp.Resolved != 1 || notifier.triggered != 1 {
		t.Fatalf("unexpected response or notifier state: resp=%+v triggered=%d", resp, notifier.triggered)
	}
	if len(loggedEntries) != 1 || loggedEntries[0].TruncatedAlerts != 3 || loggedEntries[0].HTTPStatus != http.StatusOK {
		t.Fatalf("unexpected webhook log entries: %+v", loggedEntries)
	}
}

type fakeIncidentRepo struct {
	reservation         *store.IncidentReservation
	reserveErr          error
	bulkReservation     *store.BulkIncidentReservation
	bulkReserveErr      error
	finalized           bool
	failedMsg           string
	adoptedFlag         bool
	callerUsed          store.CallerResolution
	lastIdem            store.IdempotencyMeta
	lastWarnings        []string
	completedSingleKeys map[string]*model.CreateIncidentResponse
	completedBulkKeys   map[string]*model.CreateBulkIncidentResponse
	releasedKeys        []string

	alertStateForNote   model.AlertState
	incForNote          *model.ServiceNowIncident
	teamGroups          map[string]string
	autoReopenState     *store.AutoReopenState
	reopenRecordedCalls int
	lastReopenNote      string
	lastReopenSource    string
	updateBySysIDCount  int
}

func (f *fakeIncidentRepo) ReserveIncidentCreation(_ context.Context, _ string, _ auth.OperatorContext, _ model.CreateIncidentRequest, _ bool, idem store.IdempotencyMeta) (*store.IncidentReservation, error) {
	f.lastIdem = idem
	if f.reserveErr != nil {
		return nil, f.reserveErr
	}
	if idem.Key != "" && f.completedSingleKeys != nil {
		if cached, ok := f.completedSingleKeys[idem.Key]; ok {
			return &store.IncidentReservation{
				Idempotency:    idem,
				Replayed:       true,
				ReplayResponse: cached,
			}, nil
		}
	}
	cp := *f.reservation
	cp.Idempotency = idem
	return &cp, nil
}

func (f *fakeIncidentRepo) FinalizeIncidentCreated(_ context.Context, alertID, incidentID string, _ model.AlertState, _ auth.OperatorContext, res store.SnowCreateResult, caller store.CallerResolution, adopted bool, idemKey string, warnings []string) (*model.ServiceNowIncident, error) {
	f.finalized = true
	f.adoptedFlag = adopted
	f.callerUsed = caller
	f.lastWarnings = warnings
	inc := &model.ServiceNowIncident{
		ID:                     incidentID,
		AlertID:                alertID,
		SyncStatus:             model.SnowSyncCreated,
		SnowSysID:              &res.SysID,
		IncidentNumber:         &res.Number,
		CallerResolutionStatus: caller.ResolutionStatus,
		CallerSysID:            &caller.SysID,
		CallerFallbackReason:   caller.FallbackReason,
	}
	if idemKey != "" {
		if f.completedSingleKeys == nil {
			f.completedSingleKeys = make(map[string]*model.CreateIncidentResponse)
		}
		f.completedSingleKeys[idemKey] = &model.CreateIncidentResponse{
			AlertID:            alertID,
			ServiceNowIncident: inc,
			Warnings:           warnings,
		}
	}
	return inc, nil
}

func (f *fakeIncidentRepo) FinalizeIncidentFailed(_ context.Context, _, _ string, _ model.AlertState, _ auth.OperatorContext, errMsg, idemKey string) error {
	f.failedMsg = errMsg
	if idemKey != "" {
		f.releasedKeys = append(f.releasedKeys, idemKey)
	}
	return nil
}

func (f *fakeIncidentRepo) ReserveBulkIncidentCreation(_ context.Context, _ []string, _ auth.OperatorContext, _ model.CreateBulkIncidentRequest, _ bool, idem store.IdempotencyMeta) (*store.BulkIncidentReservation, error) {
	f.lastIdem = idem
	if f.bulkReserveErr != nil {
		return nil, f.bulkReserveErr
	}
	if idem.Key != "" && f.completedBulkKeys != nil {
		if cached, ok := f.completedBulkKeys[idem.Key]; ok {
			return &store.BulkIncidentReservation{
				Idempotency:    idem,
				Replayed:       true,
				ReplayResponse: cached,
			}, nil
		}
	}
	cp := *f.bulkReservation
	cp.Idempotency = idem
	return &cp, nil
}

func (f *fakeIncidentRepo) FinalizeBulkIncidentCreated(_ context.Context, resv *store.BulkIncidentReservation, _ auth.OperatorContext, res store.SnowCreateResult, caller store.CallerResolution, adopted bool, warnings []string) (*model.ServiceNowIncident, error) {
	f.finalized = true
	f.adoptedFlag = adopted
	f.callerUsed = caller
	f.lastWarnings = warnings
	inc := &model.ServiceNowIncident{
		ID:                     resv.IncidentIDs[resv.PrimaryAlertID],
		AlertID:                resv.PrimaryAlertID,
		SyncStatus:             model.SnowSyncCreated,
		SnowSysID:              &res.SysID,
		IncidentNumber:         &res.Number,
		CallerResolutionStatus: caller.ResolutionStatus,
		CallerSysID:            &caller.SysID,
		CallerFallbackReason:   caller.FallbackReason,
	}
	if resv.Idempotency.Key != "" {
		if f.completedBulkKeys == nil {
			f.completedBulkKeys = make(map[string]*model.CreateBulkIncidentResponse)
		}
		alertIDs := make([]string, 0, len(resv.Alerts))
		for _, a := range resv.Alerts {
			alertIDs = append(alertIDs, a.ID)
		}
		f.completedBulkKeys[resv.Idempotency.Key] = &model.CreateBulkIncidentResponse{
			AlertIDs:           alertIDs,
			ServiceNowIncident: inc,
			Warnings:           warnings,
		}
	}
	return inc, nil
}

func (f *fakeIncidentRepo) FinalizeBulkIncidentFailed(_ context.Context, resv *store.BulkIncidentReservation, _ auth.OperatorContext, errMsg string) error {
	f.failedMsg = errMsg
	if resv != nil && resv.Idempotency.Key != "" {
		f.releasedKeys = append(f.releasedKeys, resv.Idempotency.Key)
	}
	return nil
}

func (f *fakeIncidentRepo) SaveLinkedIncident(_ context.Context, alertID string, _ auth.OperatorContext, snowInc store.SnowCreateResult, _ bool) (*model.IncidentSyncResponse, error) {
	return &model.IncidentSyncResponse{
		AlertID: alertID,
		ServiceNowIncident: &model.ServiceNowIncident{
			SyncStatus:     model.SnowSyncLinked,
			SnowSysID:      &snowInc.SysID,
			IncidentNumber: &snowInc.Number,
		},
	}, nil
}
func (f *fakeIncidentRepo) GetAlertAndIncident(_ context.Context, _ string) (model.AlertState, *model.ServiceNowIncident, error) {
	return f.alertStateForNote, f.incForNote, nil
}
func (f *fakeIncidentRepo) UpdateIncidentForAlert(_ context.Context, _ string, _ auth.OperatorContext, _ store.SnowSyncUpdate) (*model.IncidentSyncResponse, error) {
	return nil, nil
}
func (f *fakeIncidentRepo) UpdateIncidentsBySysID(_ context.Context, _ string, _ store.SnowSyncUpdate, _ model.ActorType, _ string, _ bool) (int, error) {
	if f.updateBySysIDCount > 0 {
		return f.updateBySysIDCount, nil
	}
	return 1, nil
}
func (f *fakeIncidentRepo) RecordWorkNotePosted(_ context.Context, alertID string, alertState model.AlertState, op auth.OperatorContext, noteText string, _ *string) (*model.AuditEvent, error) {
	return &model.AuditEvent{
		AlertID:       alertID,
		EventType:     model.AuditSnowWorkNotePosted,
		ActorType:     model.ActorOperator,
		ActorLogin:    op.Login,
		PreviousState: &alertState,
		NewState:      &alertState,
		Comment:       &noteText,
	}, nil
}
func (f *fakeIncidentRepo) GetIncidentActiveStatus(_ context.Context, snowSysID string) (*model.IncidentActiveStatusResponse, error) {
	return &model.IncidentActiveStatusResponse{SnowSysID: snowSysID, ActiveAlerts: []model.ActiveAlertSummary{}}, nil
}
func (f *fakeIncidentRepo) GetAutoReopenState(_ context.Context, _ string) (*store.AutoReopenState, error) {
	return f.autoReopenState, nil
}
func (f *fakeIncidentRepo) RecordIncidentAutoReopened(_ context.Context, _ string, _ store.SnowSyncUpdate, workNote, triggerSource string, _ int) error {
	f.reopenRecordedCalls++
	f.lastReopenNote = workNote
	f.lastReopenSource = triggerSource
	return nil
}
func (f *fakeIncidentRepo) UpsertTeamAssignmentGroup(_ context.Context, team, assignmentGroup string, _ auth.OperatorContext) error {
	if f.teamGroups == nil {
		f.teamGroups = make(map[string]string)
	}
	f.teamGroups[team] = assignmentGroup
	return nil
}
func (f *fakeIncidentRepo) LookupTeamAssignmentGroup(_ context.Context, team string) (string, error) {
	if f.teamGroups == nil {
		return "", nil
	}
	return f.teamGroups[team], nil
}
func (f *fakeIncidentRepo) ListTeamAssignmentGroups(_ context.Context) (map[string]string, error) {
	if f.teamGroups == nil {
		return map[string]string{}, nil
	}
	out := make(map[string]string, len(f.teamGroups))
	for k, v := range f.teamGroups {
		out[k] = v
	}
	return out, nil
}

type fakeSnowClient struct {
	existingByCorr    *servicenow.IncidentRecord
	existingByCorrMap map[string]*servicenow.IncidentRecord
	corrLookupCalls   []string
	findByNumCalls    int
	foundByNum        *servicenow.IncidentRecord
	createCalls       int
	createErrOnce     error
	createdRec        *servicenow.IncidentRecord
	lastCreatePayload servicenow.CreateIncidentPayload
	workNotesPosted   []string
	reopenCalls       int
	reopenErr         error
	reopenNotes       []string
}

func (f *fakeSnowClient) FindIncidentByCorrelationID(_ context.Context, corrID string) (*servicenow.IncidentRecord, error) {
	f.corrLookupCalls = append(f.corrLookupCalls, corrID)
	if f.existingByCorrMap != nil {
		if rec, ok := f.existingByCorrMap[corrID]; ok {
			return rec, nil
		}
		return nil, nil
	}
	return f.existingByCorr, nil
}
func (f *fakeSnowClient) FindIncidentByNumber(_ context.Context, _ string) (*servicenow.IncidentRecord, error) {
	f.findByNumCalls++
	return f.foundByNum, nil
}
func (f *fakeSnowClient) GetIncidentsBySysIDs(_ context.Context, _ string, _ []string) ([]servicenow.IncidentRecord, error) {
	return nil, nil
}
func (f *fakeSnowClient) CreateIncident(_ context.Context, payload servicenow.CreateIncidentPayload) (*servicenow.IncidentRecord, error) {
	f.createCalls++
	f.lastCreatePayload = payload
	if f.createErrOnce != nil {
		err := f.createErrOnce
		f.createErrOnce = nil
		return nil, err
	}
	return f.createdRec, nil
}
func (f *fakeSnowClient) PostWorkNote(_ context.Context, _, workNotes string) error {
	f.workNotesPosted = append(f.workNotesPosted, workNotes)
	return nil
}
func (f *fakeSnowClient) ReopenIncident(_ context.Context, snowSysID, workNotes string) (*servicenow.IncidentRecord, error) {
	f.reopenCalls++
	f.reopenNotes = append(f.reopenNotes, workNotes)
	if f.reopenErr != nil {
		return nil, f.reopenErr
	}
	return &servicenow.IncidentRecord{
		SysID:      snowSysID,
		State:      2,
		StateLabel: model.SnowStateLabel(2),
	}, nil
}
func (f *fakeSnowClient) ListAssignmentGroups(_ context.Context) ([]model.AssignmentGroupInfo, error) {
	return nil, nil
}

type fakeCallerLookup struct{ res store.CallerResolution }

func (f fakeCallerLookup) Resolve(_ context.Context, _ model.SnowCallerMode, _ auth.OperatorContext) store.CallerResolution {
	return f.res
}

func TestIncidentServiceRetryAdoptionAndCallerIDFallback(t *testing.T) {
	cfg := &config.Config{
		GrafanaPublicURL:       "https://grafana.example.com",
		DefaultCallerSysID:     "noc_001",
		DefaultCallerUsername:  "noc",
		DefaultAssignmentGroup: "Infra",
		DefaultCategory:        "Infrastructure",
	}

	t.Run("retry adopts existing incident by correlation_id without duplicate POST", func(t *testing.T) {
		repo := &fakeIncidentRepo{
			reservation: &store.IncidentReservation{
				IncidentID: "inc-rec-1",
				IsRetry:    true,
				Alert: model.AlertDetail{
					ID:        "11111111-1111-4111-8111-111111111111",
					State:     model.AlertStateAcknowledged,
					AlertName: "HighLag",
					Severity:  "critical",
				},
			},
		}
		snow := &fakeSnowClient{
			existingByCorr: &servicenow.IncidentRecord{
				SysID:  "snow-existing-1",
				Number: "INC0099999",
				State:  1,
			},
		}
		svc := NewIncidentService(repo, snow, fakeCallerLookup{
			res: store.CallerResolution{SysID: "u_alice", ResolutionStatus: model.CallerResSSOMatched},
		}, cfg, nil, nil)

		resp, err := svc.CreateIncident(context.Background(), "11111111-1111-4111-8111-111111111111", auth.OperatorContext{Login: "alice", Email: "alice@corp.example"}, model.CreateIncidentRequest{
			ShortDescription: "High replication lag",
			Impact:           1,
			Urgency:          1,
		}, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if snow.createCalls != 0 || !repo.adoptedFlag || *resp.ServiceNowIncident.IncidentNumber != "INC0099999" {
			t.Fatalf("expected adoption with 0 POST calls, createCalls=%d adopted=%v resp=%+v", snow.createCalls, repo.adoptedFlag, resp.ServiceNowIncident)
		}
	})

	t.Run("secondary caller_id rejection falls back once to default caller", func(t *testing.T) {
		repo := &fakeIncidentRepo{
			reservation: &store.IncidentReservation{
				IncidentID: "inc-rec-2",
				IsRetry:    false,
				Alert: model.AlertDetail{
					ID:        "22222222-2222-4222-8222-222222222222",
					State:     model.AlertStateAcknowledged,
					AlertName: "DiskFull",
					Severity:  "critical",
				},
			},
		}
		snow := &fakeSnowClient{
			createErrOnce: &servicenow.SnowError{
				API:            apierror.New(http.StatusUnprocessableEntity, apierror.CodeSnowValidationRejected, "rejected"),
				UpstreamStatus: 400,
				UpstreamBody:   `{"error":{"detail":"Invalid caller_id reference"}}`,
			},
			createdRec: &servicenow.IncidentRecord{
				SysID:  "snow-2",
				Number: "INC0000002",
				State:  1,
			},
		}
		svc := NewIncidentService(repo, snow, fakeCallerLookup{
			res: store.CallerResolution{SysID: "u_alice", ResolutionStatus: model.CallerResSSOMatched},
		}, cfg, nil, nil)

		resp, err := svc.CreateIncident(context.Background(), "22222222-2222-4222-8222-222222222222", auth.OperatorContext{Login: "alice", Email: "alice@corp.example"}, model.CreateIncidentRequest{
			ShortDescription: "Disk full on node-1",
			Impact:           1,
			Urgency:          1,
		}, "")
		if err != nil {
			t.Fatalf("unexpected error after caller_id fallback: %v", err)
		}
		if snow.createCalls != 2 || resp.ServiceNowIncident.CallerResolutionStatus != model.CallerResFallbackNOC || len(resp.Warnings) == 0 {
			t.Fatalf("expected 2 create calls and fallback warning, calls=%d resp=%+v", snow.createCalls, resp)
		}
	})
}

func TestIncidentServiceCreateBulkIncident(t *testing.T) {
	cfg := &config.Config{
		GrafanaPublicURL:       "https://grafana.example.com",
		DefaultCallerSysID:     "noc_001",
		DefaultCallerUsername:  "noc",
		DefaultAssignmentGroup: "Infra",
		DefaultCategory:        "Infrastructure",
	}

	id1 := "11111111-1111-4111-8111-111111111111"
	id2 := "22222222-2222-4222-8222-222222222222"
	teamObs := "observability"

	repo := &fakeIncidentRepo{
		bulkReservation: &store.BulkIncidentReservation{
			PrimaryAlertID: id1,
			IncidentIDs: map[string]string{
				id1: "inc-rec-1",
				id2: "inc-rec-2",
			},
			Alerts: []model.AlertDetail{
				{
					ID:           id1,
					State:        model.AlertStateAcknowledged,
					AlertName:    "HighLag",
					Severity:     "critical",
					Labels:       map[string]string{"environment": "production", "site": "eu-west-1"},
					AssignedTeam: &teamObs,
				},
				{
					ID:        id2,
					State:     model.AlertStateAcknowledged,
					AlertName: "ApiLatency",
					Severity:  "warning",
					Labels:    map[string]string{"environment": "production", "site": "eu-west-1", "team": "observability"},
				},
			},
		},
	}

	snow := &fakeSnowClient{
		createdRec: &servicenow.IncidentRecord{
			SysID:  "snow-bulk-1",
			Number: "INC0000123",
			State:  1,
		},
	}

	svc := NewIncidentService(repo, snow, fakeCallerLookup{
		res: store.CallerResolution{SysID: "u_alice", ResolutionStatus: model.CallerResSSOMatched},
	}, cfg, nil, nil)

	resp, err := svc.CreateBulkIncident(
		context.Background(),
		auth.OperatorContext{Login: "alice", Email: "alice@corp.example", Role: "Editor"},
		model.CreateBulkIncidentRequest{
			AlertIDs:         []string{id1, id2},
			ShortDescription: "Combined production incident",
			Impact:           1,
			Urgency:          1,
			CallerMode:       model.CallerModeSSOUser,
		},
		"",
	)
	if err != nil {
		t.Fatalf("CreateBulkIncident returned error: %v", err)
	}
	if snow.createCalls != 1 {
		t.Fatalf("expected exactly 1 ServiceNow POST call for bulk creation, got %d", snow.createCalls)
	}
	if !repo.finalized || len(resp.AlertIDs) != 2 || *resp.ServiceNowIncident.IncidentNumber != "INC0000123" {
		t.Fatalf("unexpected bulk response: %+v", resp)
	}
}

func TestBulkRetryAdoptionAndIdempotencyAndValidation(t *testing.T) {
	cfg := &config.Config{
		GrafanaPublicURL:       "https://grafana.example.com",
		DefaultCallerSysID:     "noc_001",
		DefaultCallerUsername:  "noc",
		DefaultAssignmentGroup: "Infra",
		DefaultCategory:        "Infrastructure",
	}

	idA := "00000000-0000-4000-8000-000000000001"
	idB := "00000000-0000-4000-8000-000000000002"
	idC := "00000000-0000-4000-8000-000000000003"

	t.Run("REV-01 bulk retry adopts existing ticket across changed alert set", func(t *testing.T) {
		repo := &fakeIncidentRepo{
			bulkReservation: &store.BulkIncidentReservation{
				PrimaryAlertID:      idA,
				IsRetry:             true,
				RetryCorrelationIDs: []string{idB, idC},
				IncidentIDs: map[string]string{
					idA: "inc-a",
					idB: "inc-b",
					idC: "inc-c",
				},
				Alerts: []model.AlertDetail{
					{ID: idA, State: model.AlertStateAcknowledged, AlertName: "AlertA", Severity: "critical"},
					{ID: idB, State: model.AlertStateAcknowledged, AlertName: "AlertB", Severity: "critical"},
					{ID: idC, State: model.AlertStateAcknowledged, AlertName: "AlertC", Severity: "warning"},
				},
			},
		}
		snow := &fakeSnowClient{
			existingByCorrMap: map[string]*servicenow.IncidentRecord{
				idB: {SysID: "snow-prior-b", Number: "INC0007777", State: 1},
			},
		}
		svc := NewIncidentService(repo, snow, fakeCallerLookup{
			res: store.CallerResolution{SysID: "u_alice", ResolutionStatus: model.CallerResSSOMatched},
		}, cfg, nil, nil)

		resp, err := svc.CreateBulkIncident(
			context.Background(),
			auth.OperatorContext{Login: "alice", Email: "alice@corp.example", Role: "Editor"},
			model.CreateBulkIncidentRequest{
				AlertIDs:         []string{idA, idB, idC},
				ShortDescription: "Expanded batch retry",
				Impact:           1,
				Urgency:          1,
			},
			"idem-bulk-retry",
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if snow.createCalls != 0 || !repo.adoptedFlag || *resp.ServiceNowIncident.IncidentNumber != "INC0007777" {
			t.Fatalf("expected adoption of INC0007777 with 0 POSTs, calls=%d adopted=%v resp=%+v", snow.createCalls, repo.adoptedFlag, resp.ServiceNowIncident)
		}
	})

	t.Run("REV-03 Idempotency-Key replays cached 201 and releases key on transient error", func(t *testing.T) {
		repo := &fakeIncidentRepo{
			reservation: &store.IncidentReservation{
				IncidentID: "inc-single-1",
				Alert: model.AlertDetail{
					ID:        idA,
					State:     model.AlertStateAcknowledged,
					AlertName: "DiskFull",
					Severity:  "critical",
				},
			},
		}
		snow := &fakeSnowClient{
			createErrOnce: apierror.New(http.StatusServiceUnavailable, apierror.CodeSnowUnavailable, "down"),
			createdRec:    &servicenow.IncidentRecord{SysID: "snow-1", Number: "INC0001001", State: 1},
		}
		svc := NewIncidentService(repo, snow, fakeCallerLookup{
			res: store.CallerResolution{SysID: "u_alice", ResolutionStatus: model.CallerResSSOMatched},
		}, cfg, nil, nil)

		req := model.CreateIncidentRequest{
			ShortDescription: "Disk full on db-1",
			Impact:           1,
			Urgency:          1,
		}
		op := auth.OperatorContext{Login: "alice", Email: "alice@corp.example", Role: "Editor"}

		// First attempt fails transiently and releases the in_progress key
		if _, err := svc.CreateIncident(context.Background(), idA, op, req, "key-retry-1"); err == nil {
			t.Fatalf("expected transient failure on first attempt")
		}
		if len(repo.releasedKeys) != 1 || repo.releasedKeys[0] != "key-retry-1" {
			t.Fatalf("expected key-retry-1 released on failure, got %v", repo.releasedKeys)
		}

		// Second attempt succeeds
		firstResp, err := svc.CreateIncident(context.Background(), idA, op, req, "key-retry-1")
		if err != nil {
			t.Fatalf("expected retry with same key to succeed, got %v", err)
		}
		if firstResp.Replayed || snow.createCalls != 2 {
			t.Fatalf("expected live creation on retry, replayed=%v calls=%d", firstResp.Replayed, snow.createCalls)
		}

		// Third attempt with same key replays without calling ServiceNow again
		replayResp, err := svc.CreateIncident(context.Background(), idA, op, req, "key-retry-1")
		if err != nil {
			t.Fatalf("expected idempotent replay to succeed, got %v", err)
		}
		if !replayResp.Replayed || snow.createCalls != 2 || *replayResp.ServiceNowIncident.IncidentNumber != "INC0001001" {
			t.Fatalf("expected cached replay with 0 extra SNOW calls, replayed=%v calls=%d resp=%+v", replayResp.Replayed, snow.createCalls, replayResp)
		}

		// Bulk replay check
		repo.bulkReservation = &store.BulkIncidentReservation{
			PrimaryAlertID: idA,
			IncidentIDs:    map[string]string{idA: "inc-a", idB: "inc-b"},
			Alerts: []model.AlertDetail{
				{ID: idA, State: model.AlertStateAcknowledged, AlertName: "A", Severity: "critical"},
				{ID: idB, State: model.AlertStateAcknowledged, AlertName: "B", Severity: "critical"},
			},
		}
		bulkReq := model.CreateBulkIncidentRequest{
			AlertIDs:         []string{idB, idA},
			ShortDescription: "Bulk outage",
			Impact:           1,
			Urgency:          1,
		}
		bulk1, err := svc.CreateBulkIncident(context.Background(), op, bulkReq, "bulk-key-1")
		if err != nil || bulk1.Replayed {
			t.Fatalf("expected initial bulk creation, err=%v replayed=%v", err, bulk1.Replayed)
		}
		bulk2, err := svc.CreateBulkIncident(context.Background(), op, bulkReq, "bulk-key-1")
		if err != nil || !bulk2.Replayed || snow.createCalls != 3 {
			t.Fatalf("expected bulk replay with 0 extra SNOW calls, err=%v replayed=%v calls=%d", err, bulk2.Replayed, snow.createCalls)
		}
	})

	t.Run("REV-06 LinkIncident rejects ServiceNow encoded-query operators", func(t *testing.T) {
		repo := &fakeIncidentRepo{}
		snow := &fakeSnowClient{}
		svc := NewIncidentService(repo, snow, fakeCallerLookup{}, cfg, nil, nil)

		_, err := svc.LinkIncident(context.Background(), idA, auth.OperatorContext{Login: "alice"}, model.LinkIncidentRequest{
			IncidentNumber: "INC001^ORpriority=1",
		})
		if err == nil || snow.findByNumCalls != 0 {
			t.Fatalf("expected validation error with 0 ServiceNow calls, err=%v calls=%d", err, snow.findByNumCalls)
		}
	})
}

type fakeNoteWorkerStore struct {
	pending     []model.PendingResolutionNote
	markedCalls []struct {
		items  []model.PendingResolutionNote
		note   string
		reason string
	}
}

func (f *fakeNoteWorkerStore) FetchPendingResolutionNotes(_ context.Context, _ int) ([]model.PendingResolutionNote, error) {
	return f.pending, nil
}

func (f *fakeNoteWorkerStore) MarkResolutionNotesPostedBatch(_ context.Context, items []model.PendingResolutionNote, noteText, reason string) error {
	f.markedCalls = append(f.markedCalls, struct {
		items  []model.PendingResolutionNote
		note   string
		reason string
	}{items: items, note: noteText, reason: reason})
	return nil
}

type fakeNotePoster struct {
	posted []struct {
		sysID string
		note  string
	}
}

func (f *fakeNotePoster) PostWorkNote(_ context.Context, snowSysID, workNotes string) error {
	f.posted = append(f.posted, struct {
		sysID string
		note  string
	}{sysID: snowSysID, note: workNotes})
	return nil
}

func TestSiblingAwareResolutionNotes(t *testing.T) {
	now := time.Date(2026, 11, 26, 8, 0, 0, 0, time.UTC)

	t.Run("REV-11 partial recovery keeps ticket open when active siblings remain", func(t *testing.T) {
		st := &fakeNoteWorkerStore{
			pending: []model.PendingResolutionNote{
				{
					IncidentID:              "inc-1",
					AlertID:                 "a-1",
					SnowSysID:               "sys-bulk-1",
					IncidentNumber:          "INC0012345",
					AlertName:               "HighLag",
					ResolvedAt:              now,
					TotalLinkedAlerts:       3,
					RemainingActiveSiblings: 2,
				},
			},
		}
		poster := &fakeNotePoster{}
		if err := processPendingResolutionNotes(context.Background(), st, poster, nil); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(poster.posted) != 1 || len(st.markedCalls) != 1 {
			t.Fatalf("expected 1 partial note posted and marked, posted=%d marked=%d", len(poster.posted), len(st.markedCalls))
		}
		if st.markedCalls[0].reason != "alert_resolved_siblings_active" {
			t.Fatalf("expected reason alert_resolved_siblings_active, got %q", st.markedCalls[0].reason)
		}
		note := poster.posted[0].note
		if !containsAll(note, "2 other linked alert(s)", "keep this incident open") {
			t.Fatalf("unexpected partial note wording: %q", note)
		}
	})

	t.Run("REV-11 posts single consolidated closure nudge when all bulk siblings resolve", func(t *testing.T) {
		st := &fakeNoteWorkerStore{
			pending: []model.PendingResolutionNote{
				{
					IncidentID:              "inc-1",
					AlertID:                 "a-1",
					SnowSysID:               "sys-bulk-1",
					IncidentNumber:          "INC0012345",
					AlertName:               "HighLag",
					ResolvedAt:              now,
					TotalLinkedAlerts:       2,
					RemainingActiveSiblings: 0,
				},
				{
					IncidentID:              "inc-2",
					AlertID:                 "a-2",
					SnowSysID:               "sys-bulk-1",
					IncidentNumber:          "INC0012345",
					AlertName:               "ApiLatency",
					ResolvedAt:              now.Add(1 * time.Minute),
					TotalLinkedAlerts:       2,
					RemainingActiveSiblings: 0,
				},
			},
		}
		poster := &fakeNotePoster{}
		if err := processPendingResolutionNotes(context.Background(), st, poster, nil); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(poster.posted) != 1 {
			t.Fatalf("expected 1 consolidated work note across 2 resolved siblings, got %d", len(poster.posted))
		}
		if len(st.markedCalls) != 1 || len(st.markedCalls[0].items) != 2 || st.markedCalls[0].reason != "alert_resolved_open_ticket" {
			t.Fatalf("expected batch mark of 2 siblings, got %+v", st.markedCalls)
		}
		if !containsAll(poster.posted[0].note, "All 2 linked", "HighLag", "ApiLatency", "resolve/close the ticket") {
			t.Fatalf("unexpected consolidated note wording: %q", poster.posted[0].note)
		}
	})
}

func containsAll(haystack string, subs ...string) bool {
	for _, s := range subs {
		if !containsStr(haystack, s) {
			return false
		}
	}
	return true
}

func containsStr(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && func() bool {
		for i := 0; i <= len(haystack)-len(needle); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	}())
}

func TestNormalizeSeverityAliasesAndRawPreservation(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"critical", "critical"},
		{" CRIT ", "critical"},
		{"p1", "critical"},
		{"Sev1", "critical"},
		{"s1", "critical"},
		{"fatal", "critical"},
		{"emergency", "critical"},
		{"emerg", "critical"},
		{"immediate", "critical"},
		{"disaster", "critical"},
		{"HIGH", "critical"},
		{"major", "critical"},
		{"error", "critical"},
		{"err", "critical"},
		{"page", "critical"},
		{"blocker", "critical"},
		{"warning", "warning"},
		{" WARN ", "warning"},
		{"p2", "warning"},
		{"Sev2", "warning"},
		{"s2", "warning"},
		{"medium", "warning"},
		{"med", "warning"},
		{"moderate", "warning"},
		{"minor", "warning"},
		{"info", "info"},
		{"informational", "informational"},
		{"low", "low"},
		{"debug", "debug"},
		{"notice", "notice"},
		{"none", "none"},
		{"p3", "p3"},
		{"p4", "p4"},
		{"sev3", "sev3"},
		{"sev4", "sev4"},
		{"", "unknown"},
	}

	for _, tc := range cases {
		if got := NormalizeSeverity(tc.raw); got != tc.want {
			t.Errorf("NormalizeSeverity(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}

	raw := model.AlertmanagerWebhookPayload{
		Version:  "4",
		GroupKey: "gk-sev",
		Receiver: "alert-api",
		Alerts: []model.AlertmanagerAlert{
			{
				Status:      "firing",
				Fingerprint: "fp-sev-1",
				StartsAt:    time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC),
				Labels:      map[string]string{"alertname": "HighCPU", "severity": " P1 "},
			},
			{
				Status:      "firing",
				Fingerprint: "fp-sev-2",
				StartsAt:    time.Date(2026, 11, 26, 10, 1, 0, 0, time.UTC),
				Labels:      map[string]string{"alertname": "DiskWarn", "severity": "Sev2"},
			},
			{
				Status:      "firing",
				Fingerprint: "fp-sev-3",
				StartsAt:    time.Date(2026, 11, 26, 10, 2, 0, 0, time.UTC),
				Labels:      map[string]string{"alertname": "InfoNoise", "severity": "p3"},
			},
		},
	}
	out, err := ValidateAndNormalizeAlertmanagerPayload(&raw)
	if err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
	if out[0].Severity != "critical" || out[0].Labels["severity"] != " P1 " {
		t.Fatalf("expected critical with preserved raw label, got %+v", out[0])
	}
	if out[1].Severity != "warning" || out[1].Labels["severity"] != "Sev2" {
		t.Fatalf("expected warning with preserved raw label, got %+v", out[1])
	}
	if out[2].Severity != "p3" || out[2].Labels["severity"] != "p3" {
		t.Fatalf("expected p3 excluded severity preserved, got %+v", out[2])
	}
}

func TestLearnedTeamAssignmentGroupAndNoBlindFallback(t *testing.T) {
	cfg := &config.Config{
		GrafanaPublicURL:       "https://grafana.example.com",
		DefaultCallerSysID:     "noc_001",
		DefaultCallerUsername:  "noc",
		DefaultAssignmentGroup: "Default NOC Queue",
		DefaultCategory:        "Infrastructure",
	}
	id1 := "11111111-1111-4111-8111-111111111111"
	id2 := "22222222-2222-4222-8222-222222222222"
	op := auth.OperatorContext{Login: "alice.smith", Email: "alice@corp.example", Name: "Alice Smith"}

	t.Run("does NOT blindly copy team label or assigned_team and learns from single create", func(t *testing.T) {
		assignedTeam := "operator-tagged-team"
		repo := &fakeIncidentRepo{
			reservation: &store.IncidentReservation{
				IncidentID: "inc-1",
				Alert: model.AlertDetail{
					ID:           id1,
					State:        model.AlertStateAcknowledged,
					AlertName:    "HighLag",
					Severity:     "critical",
					Labels:       map[string]string{"team": "core-db"},
					AssignedTeam: &assignedTeam,
				},
			},
		}
		snow := &fakeSnowClient{
			createdRec: &servicenow.IncidentRecord{SysID: "sys-1", Number: "INC0001001", State: 1},
		}
		svc := NewIncidentService(repo, snow, fakeCallerLookup{
			res: store.CallerResolution{SysID: "u_alice", ResolutionStatus: model.CallerResSSOMatched},
		}, cfg, nil, nil)

		// Empty assignment_group with unmapped team must use DefaultAssignmentGroup, NOT "core-db"
		if _, err := svc.CreateIncident(context.Background(), id1, op, model.CreateIncidentRequest{
			ShortDescription: "Replication lag alert",
			Impact:           1,
			Urgency:          1,
		}, ""); err != nil {
			t.Fatalf("CreateIncident error: %v", err)
		}
		if snow.lastCreatePayload.AssignmentGroup != "Default NOC Queue" {
			t.Fatalf("expected Default NOC Queue instead of blind team copy, got %q", snow.lastCreatePayload.AssignmentGroup)
		}

		// Operator creates incident choosing "Database SRE" -> learned mapping persisted for "core-db"
		snow.createdRec = &servicenow.IncidentRecord{
			SysID:           "sys-2",
			Number:          "INC0001002",
			State:           1,
			AssignmentGroup: model.StringPtrOrNil("Database SRE"),
		}
		if _, err := svc.CreateIncident(context.Background(), id1, op, model.CreateIncidentRequest{
			ShortDescription: "Replication lag alert 2",
			Impact:           1,
			Urgency:          1,
			AssignmentGroup:  "Database SRE",
		}, ""); err != nil {
			t.Fatalf("CreateIncident error: %v", err)
		}
		if repo.teamGroups["core-db"] != "Database SRE" {
			t.Fatalf("expected learned mapping core-db -> Database SRE, got %+v", repo.teamGroups)
		}

		meta := svc.GetMetadata(context.Background())
		if meta.TeamAssignmentGroups["core-db"] != "Database SRE" {
			t.Fatalf("expected metadata.team_assignment_groups[core-db]=Database SRE, got %+v", meta.TeamAssignmentGroups)
		}

		// Subsequent create with empty assignment_group resolves via learned mapping
		if _, err := svc.CreateIncident(context.Background(), id1, op, model.CreateIncidentRequest{
			ShortDescription: "Replication lag alert 3",
			Impact:           1,
			Urgency:          1,
		}, ""); err != nil {
			t.Fatalf("CreateIncident error: %v", err)
		}
		if snow.lastCreatePayload.AssignmentGroup != "Database SRE" {
			t.Fatalf("expected learned group Database SRE, got %q", snow.lastCreatePayload.AssignmentGroup)
		}
	})

	t.Run("bulk creation learns only when all alerts share the same Prometheus team label", func(t *testing.T) {
		repo := &fakeIncidentRepo{
			bulkReservation: &store.BulkIncidentReservation{
				PrimaryAlertID: id1,
				IncidentIDs:    map[string]string{id1: "i1", id2: "i2"},
				Alerts: []model.AlertDetail{
					{ID: id1, State: model.AlertStateAcknowledged, AlertName: "A1", Labels: map[string]string{"team": "platform"}},
					{ID: id2, State: model.AlertStateAcknowledged, AlertName: "A2", Labels: map[string]string{"team": "payments"}},
				},
			},
		}
		snow := &fakeSnowClient{
			createdRec: &servicenow.IncidentRecord{SysID: "sys-b1", Number: "INC0002001", State: 1},
		}
		svc := NewIncidentService(repo, snow, fakeCallerLookup{
			res: store.CallerResolution{SysID: "u_alice", ResolutionStatus: model.CallerResSSOMatched},
		}, cfg, nil, nil)

		if _, err := svc.CreateBulkIncident(context.Background(), op, model.CreateBulkIncidentRequest{
			AlertIDs:         []string{id1, id2},
			ShortDescription: "Mixed teams incident",
			Impact:           1,
			Urgency:          1,
			AssignmentGroup:  "Platform Engineering",
		}, ""); err != nil {
			t.Fatalf("CreateBulkIncident error: %v", err)
		}
		if len(repo.teamGroups) != 0 {
			t.Fatalf("expected no team mapping learned on mismatched bulk teams, got %+v", repo.teamGroups)
		}

		// Same team across all selected alerts -> learns mapping
		repo.bulkReservation.Alerts[1].Labels["team"] = "platform"
		if _, err := svc.CreateBulkIncident(context.Background(), op, model.CreateBulkIncidentRequest{
			AlertIDs:         []string{id1, id2},
			ShortDescription: "Shared team incident",
			Impact:           1,
			Urgency:          1,
			AssignmentGroup:  "Platform Engineering",
		}, ""); err != nil {
			t.Fatalf("CreateBulkIncident error: %v", err)
		}
		if repo.teamGroups["platform"] != "Platform Engineering" {
			t.Fatalf("expected learned mapping platform -> Platform Engineering, got %+v", repo.teamGroups)
		}
	})
}

func TestOperatorWorkNoteAttribution(t *testing.T) {
	cfg := &config.Config{
		GrafanaPublicURL:       "https://grafana.example.com",
		DefaultCallerSysID:     "noc_001",
		DefaultCallerUsername:  "noc",
		DefaultAssignmentGroup: "Infra",
		DefaultCategory:        "Infrastructure",
	}
	sysID := "snow_sys_1"
	incNum := "INC0000100"
	repo := &fakeIncidentRepo{
		alertStateForNote: model.AlertStateAcknowledged,
		incForNote: &model.ServiceNowIncident{
			ID:             "inc-1",
			SyncStatus:     model.SnowSyncCreated,
			SnowSysID:      &sysID,
			IncidentNumber: &incNum,
		},
	}
	snow := &fakeSnowClient{
		foundByNum: &servicenow.IncidentRecord{SysID: sysID, Number: incNum, State: 2},
	}
	svc := NewIncidentService(repo, snow, fakeCallerLookup{}, cfg, nil, nil)

	// 1. Blank Name -> no leading empty space before parentheses
	blankNameOp := auth.OperatorContext{Login: "alice.smith", Email: "alice@corp.example"}
	initial := formatInitialWorkNotes(blankNameOp, "", store.CallerResolution{})
	if !containsAll(initial, "by: alice.smith (alice@corp.example).") || containsStr(initial, "by:  (") {
		t.Fatalf("unexpected initial work note when Name is blank: %q", initial)
	}

	// 2. Full Name + Login + Email
	fullOp := auth.OperatorContext{Name: "Alice Smith", Login: "alice.smith", Email: "alice@corp.example"}
	if _, err := svc.PostWorkNote(context.Background(), "11111111-1111-4111-8111-111111111111", fullOp, model.WorkNoteRequest{
		WorkNotes: "Checking replica lag",
	}); err != nil {
		t.Fatalf("PostWorkNote error: %v", err)
	}
	if len(snow.workNotesPosted) != 1 || snow.workNotesPosted[0] != "[Grafana NOC — Alice Smith (alice.smith, alice@corp.example)] Checking replica lag" {
		t.Fatalf("unexpected PostWorkNote attribution: %v", snow.workNotesPosted)
	}

	// 3. Login only -> no empty parens
	loginOnlyOp := auth.OperatorContext{Login: "noc.bot"}
	if _, err := svc.LinkIncident(context.Background(), "11111111-1111-4111-8111-111111111111", loginOnlyOp, model.LinkIncidentRequest{
		IncidentNumber: incNum,
		PostWorkNote:   true,
	}); err != nil {
		t.Fatalf("LinkIncident error: %v", err)
	}
	if len(snow.workNotesPosted) != 2 || !containsStr(snow.workNotesPosted[1], "[Grafana NOC — noc.bot] Linked alert episode") {
		t.Fatalf("unexpected LinkIncident attribution: %v", snow.workNotesPosted)
	}
}

func TestAutoReopenLoopGuardAndClosedTickets(t *testing.T) {
	cfg := &config.Config{
		SnowAutoReopenCooldown:    5 * time.Minute,
		SnowAutoReopenMaxAttempts: 3,
	}

	t.Run("webhook state 6 auto-reopens when active alerts exist", func(t *testing.T) {
		repo := &fakeIncidentRepo{
			updateBySysIDCount: 1,
			autoReopenState: &store.AutoReopenState{
				SnowSysID:      "snow_1",
				IncidentNumber: "INC0000010",
				ActiveAlerts: []model.ActiveAlertSummary{
					{ID: "a1", AlertName: "HighLag", Severity: "critical", State: model.AlertStateAcknowledged},
				},
			},
		}
		snow := &fakeSnowClient{}
		svc := NewIncidentService(repo, snow, fakeCallerLookup{}, cfg, nil, nil)

		raw, _ := json.Marshal(model.ServiceNowWebhookPayload{SysID: "snow_1", State: 6})
		updated, err := svc.ProcessInboundWebhook(context.Background(), raw, model.ServiceNowWebhookPayload{
			SysID: "snow_1",
			State: 6,
		})
		if err != nil || updated != 1 {
			t.Fatalf("ProcessInboundWebhook failed: updated=%d err=%v", updated, err)
		}
		if snow.reopenCalls != 1 || repo.reopenRecordedCalls != 1 || repo.lastReopenSource != "webhook" {
			t.Fatalf("expected 1 auto-reopen via webhook, snow=%d repo=%d src=%q", snow.reopenCalls, repo.reopenRecordedCalls, repo.lastReopenSource)
		}
		if !containsAll(snow.reopenNotes[0], "[Grafana NOC] Auto-reopened:", "HighLag") {
			t.Fatalf("unexpected reopen note: %q", snow.reopenNotes[0])
		}
	})

	t.Run("cooldown and max attempts prevent infinite reopen loops", func(t *testing.T) {
		recent := time.Now().Add(-1 * time.Minute)
		repo := &fakeIncidentRepo{
			autoReopenState: &store.AutoReopenState{
				SnowSysID:          "snow_1",
				IncidentNumber:     "INC0000010",
				MaxAutoReopenCount: 1,
				LastAutoReopenedAt: &recent,
				ActiveAlerts: []model.ActiveAlertSummary{
					{ID: "a1", AlertName: "HighLag", Severity: "critical", State: model.AlertStateFiring},
				},
			},
		}
		snow := &fakeSnowClient{}
		svc := NewIncidentService(repo, snow, fakeCallerLookup{}, cfg, nil, nil)

		svc.HandlePrematureResolvedState(context.Background(), "snow_1", "poller")
		if snow.reopenCalls != 0 {
			t.Fatalf("expected cooldown to suppress reopen, got %d calls", snow.reopenCalls)
		}

		expired := time.Now().Add(-10 * time.Minute)
		repo.autoReopenState.LastAutoReopenedAt = &expired
		repo.autoReopenState.MaxAutoReopenCount = 3
		svc.HandlePrematureResolvedState(context.Background(), "snow_1", "poller")
		if snow.reopenCalls != 0 {
			t.Fatalf("expected max_attempts=3 cap to suppress reopen, got %d calls", snow.reopenCalls)
		}
	})

	t.Run("state 7 (Closed) and 8 (Canceled) or zero active alerts never auto-reopen", func(t *testing.T) {
		repo := &fakeIncidentRepo{
			updateBySysIDCount: 1,
			autoReopenState: &store.AutoReopenState{
				SnowSysID: "snow_2",
				ActiveAlerts: []model.ActiveAlertSummary{
					{ID: "a1", AlertName: "HighLag", Severity: "critical", State: model.AlertStateAcknowledged},
				},
			},
		}
		snow := &fakeSnowClient{}
		svc := NewIncidentService(repo, snow, fakeCallerLookup{}, cfg, nil, nil)

		for _, st := range []int16{7, 8} {
			raw, _ := json.Marshal(model.ServiceNowWebhookPayload{SysID: "snow_2", State: st})
			if _, err := svc.ProcessInboundWebhook(context.Background(), raw, model.ServiceNowWebhookPayload{SysID: "snow_2", State: st}); err != nil {
				t.Fatalf("webhook state %d error: %v", st, err)
			}
		}
		if snow.reopenCalls != 0 {
			t.Fatalf("expected 0 reopen calls for terminal states 7/8, got %d", snow.reopenCalls)
		}

		repo.autoReopenState.ActiveAlerts = nil
		svc.HandlePrematureResolvedState(context.Background(), "snow_2", "webhook")
		if snow.reopenCalls != 0 {
			t.Fatalf("expected 0 reopen calls when no active alerts remain, got %d", snow.reopenCalls)
		}
	})
}
