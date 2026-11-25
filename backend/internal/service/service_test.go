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
	reservation *store.IncidentReservation
	finalized   bool
	failedMsg   string
	adoptedFlag bool
	callerUsed  store.CallerResolution
}

func (f *fakeIncidentRepo) ReserveIncidentCreation(_ context.Context, _ string, _ auth.OperatorContext, _ model.CreateIncidentRequest, _ bool) (*store.IncidentReservation, error) {
	return f.reservation, nil
}

func (f *fakeIncidentRepo) FinalizeIncidentCreated(_ context.Context, alertID, incidentID string, _ model.AlertState, _ auth.OperatorContext, res store.SnowCreateResult, caller store.CallerResolution, adopted bool) (*model.ServiceNowIncident, error) {
	f.finalized = true
	f.adoptedFlag = adopted
	f.callerUsed = caller
	return &model.ServiceNowIncident{
		ID:                     incidentID,
		AlertID:                alertID,
		SyncStatus:             model.SnowSyncCreated,
		SnowSysID:              &res.SysID,
		IncidentNumber:         &res.Number,
		CallerResolutionStatus: caller.ResolutionStatus,
		CallerSysID:            &caller.SysID,
		CallerFallbackReason:   caller.FallbackReason,
	}, nil
}

func (f *fakeIncidentRepo) FinalizeIncidentFailed(_ context.Context, _, _ string, _ model.AlertState, _ auth.OperatorContext, errMsg string) error {
	f.failedMsg = errMsg
	return nil
}

func (f *fakeIncidentRepo) SaveLinkedIncident(_ context.Context, _ string, _ auth.OperatorContext, _ store.SnowCreateResult, _ bool) (*model.IncidentSyncResponse, error) {
	return nil, nil
}
func (f *fakeIncidentRepo) GetAlertAndIncident(_ context.Context, _ string) (model.AlertState, *model.ServiceNowIncident, error) {
	return "", nil, nil
}
func (f *fakeIncidentRepo) UpdateIncidentForAlert(_ context.Context, _ string, _ auth.OperatorContext, _ store.SnowSyncUpdate) (*model.IncidentSyncResponse, error) {
	return nil, nil
}
func (f *fakeIncidentRepo) UpdateIncidentsBySysID(_ context.Context, _ string, _ store.SnowSyncUpdate, _ model.ActorType, _ string, _ bool) (int, error) {
	return 0, nil
}
func (f *fakeIncidentRepo) RecordWorkNotePosted(_ context.Context, _ string, _ model.AlertState, _ auth.OperatorContext, _ string, _ *string) (*model.AuditEvent, error) {
	return nil, nil
}

type fakeSnowClient struct {
	existingByCorr *servicenow.IncidentRecord
	createCalls    int
	createErrOnce  error
	createdRec     *servicenow.IncidentRecord
}

func (f *fakeSnowClient) FindIncidentByCorrelationID(_ context.Context, _ string) (*servicenow.IncidentRecord, error) {
	return f.existingByCorr, nil
}
func (f *fakeSnowClient) FindIncidentByNumber(_ context.Context, _ string) (*servicenow.IncidentRecord, error) {
	return nil, nil
}
func (f *fakeSnowClient) GetIncidentsBySysIDs(_ context.Context, _ string, _ []string) ([]servicenow.IncidentRecord, error) {
	return nil, nil
}
func (f *fakeSnowClient) CreateIncident(_ context.Context, payload servicenow.CreateIncidentPayload) (*servicenow.IncidentRecord, error) {
	f.createCalls++
	if f.createErrOnce != nil {
		err := f.createErrOnce
		f.createErrOnce = nil
		return nil, err
	}
	return f.createdRec, nil
}
func (f *fakeSnowClient) PostWorkNote(_ context.Context, _, _ string) error { return nil }
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
		})
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
		})
		if err != nil {
			t.Fatalf("unexpected error after caller_id fallback: %v", err)
		}
		if snow.createCalls != 2 || resp.ServiceNowIncident.CallerResolutionStatus != model.CallerResFallbackNOC || len(resp.Warnings) == 0 {
			t.Fatalf("expected 2 create calls and fallback warning, calls=%d resp=%+v", snow.createCalls, resp)
		}
	})
}
