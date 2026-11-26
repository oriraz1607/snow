package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/oriraz/grafana-snow-integration/backend/internal/apierror"
	"github.com/oriraz/grafana-snow-integration/backend/internal/auth"
	"github.com/oriraz/grafana-snow-integration/backend/internal/config"
	"github.com/oriraz/grafana-snow-integration/backend/internal/model"
)

type stubIncidentService struct {
	singleResp       *model.CreateIncidentResponse
	singleErr        error
	bulkResp         *model.CreateBulkIncidentResponse
	bulkErr          error
	activeStatusResp *model.IncidentActiveStatusResponse
	activeStatusErr  error
	lastActiveSysID  string
	lastIdemKey      string
	createCalls      int
	bulkCalls        int
}

func (s *stubIncidentService) CreateIncident(_ context.Context, _ string, _ auth.OperatorContext, _ model.CreateIncidentRequest, idempotencyKey string) (*model.CreateIncidentResponse, error) {
	s.createCalls++
	s.lastIdemKey = idempotencyKey
	return s.singleResp, s.singleErr
}

func (s *stubIncidentService) CreateBulkIncident(_ context.Context, _ auth.OperatorContext, _ model.CreateBulkIncidentRequest, idempotencyKey string) (*model.CreateBulkIncidentResponse, error) {
	s.bulkCalls++
	s.lastIdemKey = idempotencyKey
	return s.bulkResp, s.bulkErr
}

func (s *stubIncidentService) LinkIncident(_ context.Context, _ string, _ auth.OperatorContext, _ model.LinkIncidentRequest) (*model.IncidentSyncResponse, error) {
	return nil, nil
}
func (s *stubIncidentService) SyncIncident(_ context.Context, _ string, _ auth.OperatorContext) (*model.IncidentSyncResponse, error) {
	return nil, nil
}
func (s *stubIncidentService) PostWorkNote(_ context.Context, _ string, _ auth.OperatorContext, _ model.WorkNoteRequest) (*model.AuditEvent, error) {
	return nil, nil
}
func (s *stubIncidentService) GetMetadata(_ context.Context) model.ServiceNowMetadataResponse {
	return model.ServiceNowMetadataResponse{}
}
func (s *stubIncidentService) GetIncidentActiveStatus(_ context.Context, snowSysID string) (*model.IncidentActiveStatusResponse, error) {
	s.lastActiveSysID = snowSysID
	if s.activeStatusErr != nil {
		return nil, s.activeStatusErr
	}
	if s.activeStatusResp != nil {
		return s.activeStatusResp, nil
	}
	return &model.IncidentActiveStatusResponse{
		SnowSysID:    snowSysID,
		ActiveAlerts: []model.ActiveAlertSummary{},
	}, nil
}

type stubAlertStore struct {
	tagErr          error
	dismissErr      error
	bulkDismissErr  error
	historyResp     *model.AlertHistoryResponse
	lastHistoryLim  int
	lastHistoryOff  int
	lastFilter      model.ListAlertsFilter
}

func (s *stubAlertStore) ListAlerts(_ context.Context, f model.ListAlertsFilter) (*model.ListAlertsResponse, error) {
	s.lastFilter = f
	return &model.ListAlertsResponse{Items: []model.Alert{}}, nil
}
func (s *stubAlertStore) GetAlert(_ context.Context, _ string) (*model.AlertDetail, error) {
	return nil, nil
}
func (s *stubAlertStore) GetAlertHistory(_ context.Context, alertID string, limit, offset int) (*model.AlertHistoryResponse, error) {
	s.lastHistoryLim = limit
	s.lastHistoryOff = offset
	if s.historyResp != nil {
		return s.historyResp, nil
	}
	return &model.AlertHistoryResponse{
		AlertID:          alertID,
		AuditEvents:      []model.AuditEvent{},
		PreviousEpisodes: []model.PreviousEpisodeSummary{},
		Limit:            limit,
		Offset:           offset,
	}, nil
}
func (s *stubAlertStore) AcknowledgeAlert(_ context.Context, _ string, _ auth.OperatorContext, _ int, _ string) (*model.Alert, error) {
	return nil, nil
}
func (s *stubAlertStore) UnacknowledgeAlert(_ context.Context, _ string, _ auth.OperatorContext, _ int, _ string) (*model.Alert, error) {
	return nil, nil
}
func (s *stubAlertStore) DismissAlert(_ context.Context, _ string, _ auth.OperatorContext, _ int, _ string) (*model.Alert, error) {
	if s.dismissErr != nil {
		return nil, s.dismissErr
	}
	return nil, nil
}
func (s *stubAlertStore) BulkDismissAlerts(_ context.Context, alertIDs []string, _ auth.OperatorContext, _ string) (*model.BulkDismissResponse, error) {
	if s.bulkDismissErr != nil {
		return nil, s.bulkDismissErr
	}
	return &model.BulkDismissResponse{
		DismissedCount: len(alertIDs),
		AlertIDs:       alertIDs,
	}, nil
}
func (s *stubAlertStore) ReopenAlert(_ context.Context, _ string, _ auth.OperatorContext, _ int, _ string) (*model.Alert, error) {
	return nil, nil
}
func (s *stubAlertStore) AddOperatorNote(_ context.Context, _ string, _ auth.OperatorContext, _ string) (*model.AuditEvent, error) {
	return nil, nil
}
func (s *stubAlertStore) TagAlertTeam(_ context.Context, _ string, _ auth.OperatorContext, _ string) (*model.Alert, error) {
	return nil, s.tagErr
}

func TestServiceNowHandlerIdempotencyKey(t *testing.T) {
	const alertUUID = "11111111-1111-4111-8111-111111111111"
	incNum := "INC0001111"

	svc := &stubIncidentService{
		singleResp: &model.CreateIncidentResponse{
			AlertID: alertUUID,
			ServiceNowIncident: &model.ServiceNowIncident{
				ID:             "inc-1",
				SyncStatus:     model.SnowSyncCreated,
				IncidentNumber: &incNum,
			},
			Replayed: true,
		},
		bulkResp: &model.CreateBulkIncidentResponse{
			AlertIDs: []string{alertUUID},
			ServiceNowIncident: &model.ServiceNowIncident{
				ID:             "inc-1",
				SyncStatus:     model.SnowSyncCreated,
				IncidentNumber: &incNum,
			},
			Replayed: true,
		},
	}

	h := NewServiceNowHandler(svc)
	r := chi.NewRouter()
	r.Post("/api/v1/alerts/{alert_id}/servicenow/incident", h.CreateIncident)
	r.Post("/api/v1/servicenow/incidents/bulk", h.CreateBulkIncident)

	t.Run("rejects malformed Idempotency-Key with 400 VALIDATION_ERROR", func(t *testing.T) {
		body := []byte(`{"short_description":"Disk full alert","impact":1,"urgency":1}`)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/alerts/"+alertUUID+"/servicenow/incident", bytes.NewReader(body))
		req.Header.Set("Idempotency-Key", "bad key with spaces!")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest || svc.createCalls != 0 {
			t.Fatalf("expected 400 VALIDATION_ERROR with 0 service calls, got code=%d calls=%d", rec.Code, svc.createCalls)
		}
	})

	t.Run("replays single and bulk creation with 201 Created and Idempotency-Replayed header", func(t *testing.T) {
		body := []byte(`{"short_description":"Disk full alert","impact":1,"urgency":1}`)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/alerts/"+alertUUID+"/servicenow/incident", bytes.NewReader(body))
		req.Header.Set("Idempotency-Key", "key-single-001")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusCreated || rec.Header().Get("Idempotency-Replayed") != "true" || svc.lastIdemKey != "key-single-001" {
			t.Fatalf("unexpected single replay response: code=%d header=%q key=%q", rec.Code, rec.Header().Get("Idempotency-Replayed"), svc.lastIdemKey)
		}

		bulkBody := []byte(`{"alert_ids":["` + alertUUID + `"],"short_description":"Bulk outage","impact":1,"urgency":1}`)
		breq := httptest.NewRequest(http.MethodPost, "/api/v1/servicenow/incidents/bulk", bytes.NewReader(bulkBody))
		breq.Header.Set("Idempotency-Key", "key-bulk-001")
		brec := httptest.NewRecorder()
		r.ServeHTTP(brec, breq)

		if brec.Code != http.StatusCreated || brec.Header().Get("Idempotency-Replayed") != "true" || svc.lastIdemKey != "key-bulk-001" {
			t.Fatalf("unexpected bulk replay response: code=%d header=%q key=%q", brec.Code, brec.Header().Get("Idempotency-Replayed"), svc.lastIdemKey)
		}
	})

	t.Run("surfaces 409 IDEMPOTENCY_KEY_CONFLICT", func(t *testing.T) {
		svc.singleErr = apierror.New(http.StatusConflict, apierror.CodeIdempotencyKeyConflict, "payload mismatch")
		svc.singleResp = nil
		body := []byte(`{"short_description":"Different payload","impact":2,"urgency":2}`)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/alerts/"+alertUUID+"/servicenow/incident", bytes.NewReader(body))
		req.Header.Set("Idempotency-Key", "key-single-001")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusConflict || !bytes.Contains(rec.Body.Bytes(), []byte(apierror.CodeIdempotencyKeyConflict)) {
			t.Fatalf("expected 409 IDEMPOTENCY_KEY_CONFLICT, got %d (%s)", rec.Code, rec.Body.String())
		}
	})
}

func TestAlertsHandlerAcknowledgeRequiresTicket(t *testing.T) {
	const alertUUID = "11111111-1111-4111-8111-111111111111"
	h := NewAlertsHandler(&stubAlertStore{})
	r := chi.NewRouter()
	r.Post("/api/v1/alerts/{alert_id}/acknowledge", h.Acknowledge)

	t.Run("rejects invalid alert UUID with 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/alerts/not-a-uuid/acknowledge", bytes.NewReader([]byte(`{"version":1}`)))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 Bad Request, got %d (%s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("rejects standalone acknowledge with 409 TICKET_REQUIRED_FOR_ACKNOWLEDGE", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/alerts/"+alertUUID+"/acknowledge", bytes.NewReader([]byte(`{"version":1}`)))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusConflict || !bytes.Contains(rec.Body.Bytes(), []byte(apierror.CodeTicketRequiredForAck)) {
			t.Fatalf("expected 409 %s, got %d (%s)", apierror.CodeTicketRequiredForAck, rec.Code, rec.Body.String())
		}
	})
}

func TestAlertsHandlerTagTeamLabelConflict(t *testing.T) {
	const alertUUID = "11111111-1111-4111-8111-111111111111"
	repo := &stubAlertStore{
		tagErr: apierror.New(http.StatusConflict, apierror.CodeTeamManagedByLabel, "managed by label").
			WithDetails(map[string]any{"team": "database-sre", "team_source": "label"}),
	}
	h := NewAlertsHandler(repo)
	r := chi.NewRouter()
	r.Post("/api/v1/alerts/{alert_id}/team", h.TagTeam)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/alerts/"+alertUUID+"/team", bytes.NewReader([]byte(`{"team":"observability"}`)))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 Conflict, got %d (%s)", rec.Code, rec.Body.String())
	}
	var env map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	errBody, _ := env["error"].(map[string]any)
	if errBody["code"] != apierror.CodeTeamManagedByLabel {
		t.Fatalf("expected %s, got %v", apierror.CodeTeamManagedByLabel, errBody["code"])
	}
}

func TestAlertsHandlerListDefaultsAndSeverityFilter(t *testing.T) {
	repo := &stubAlertStore{}
	h := NewAlertsHandler(repo)
	r := chi.NewRouter()
	r.Get("/api/v1/alerts", h.List)

	t.Run("defaults to SortSeverityDesc and accepts critical/warning only", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/alerts?state=firing&severity=critical,warning", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d (%s)", rec.Code, rec.Body.String())
		}
		if repo.lastFilter.Sort != model.SortSeverityDesc {
			t.Fatalf("expected default sort %q, got %q", model.SortSeverityDesc, repo.lastFilter.Sort)
		}
		if len(repo.lastFilter.Severities) != 2 || repo.lastFilter.Severities[0] != "critical" || repo.lastFilter.Severities[1] != "warning" {
			t.Fatalf("unexpected severities filter: %+v", repo.lastFilter.Severities)
		}
	})

	for _, disallowed := range []string{"info", "low", "unknown"} {
		t.Run("rejects disallowed severity "+disallowed, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/alerts?severity="+disallowed, nil)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 Bad Request for severity=%s, got %d (%s)", disallowed, rec.Code, rec.Body.String())
			}
		})
	}

	t.Run("parses env, site, team, instance, sort, and search query filters", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/alerts?state=firing&env=prod,staging&site=eu-west-1&team=core-db,__untagged__&instance=web-01:9100,db-02:9100&sort=instance_asc&search=observability", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d (%s)", rec.Code, rec.Body.String())
		}
		if len(repo.lastFilter.Environments) != 2 || repo.lastFilter.Environments[0] != "prod" {
			t.Fatalf("expected environments [prod staging], got %+v", repo.lastFilter.Environments)
		}
		if len(repo.lastFilter.Sites) != 1 || repo.lastFilter.Sites[0] != "eu-west-1" {
			t.Fatalf("expected sites [eu-west-1], got %+v", repo.lastFilter.Sites)
		}
		if len(repo.lastFilter.Teams) != 2 || repo.lastFilter.Teams[0] != "core-db" || repo.lastFilter.Teams[1] != "__untagged__" {
			t.Fatalf("expected teams [core-db __untagged__], got %+v", repo.lastFilter.Teams)
		}
		if len(repo.lastFilter.Instances) != 2 || repo.lastFilter.Instances[0] != "web-01:9100" || repo.lastFilter.Instances[1] != "db-02:9100" {
			t.Fatalf("expected instances [web-01:9100 db-02:9100], got %+v", repo.lastFilter.Instances)
		}
		if repo.lastFilter.Sort != model.SortInstanceAsc {
			t.Fatalf("expected sort %q, got %q", model.SortInstanceAsc, repo.lastFilter.Sort)
		}
		if repo.lastFilter.Search != "observability" {
			t.Fatalf("expected search 'observability', got %q", repo.lastFilter.Search)
		}

		for _, sortOpt := range []string{
			model.SortSeverityAsc,
			model.SortAlertNameAsc,
			model.SortAlertNameDesc,
			model.SortInstanceDesc,
		} {
			sReq := httptest.NewRequest(http.MethodGet, "/api/v1/alerts?sort="+sortOpt, nil)
			sRec := httptest.NewRecorder()
			r.ServeHTTP(sRec, sReq)
			if sRec.Code != http.StatusOK || repo.lastFilter.Sort != sortOpt {
				t.Fatalf("expected 200 OK for sort=%s, got code=%d sort=%q", sortOpt, sRec.Code, repo.lastFilter.Sort)
			}
		}

		badReq := httptest.NewRequest(http.MethodGet, "/api/v1/alerts?sort=invalid_sort", nil)
		badRec := httptest.NewRecorder()
		r.ServeHTTP(badRec, badReq)
		if badRec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 Bad Request for unsupported sort, got %d", badRec.Code)
		}
	})
}

func TestAlertsHandlerBulkDismiss(t *testing.T) {
	repo := &stubAlertStore{}
	h := NewAlertsHandler(repo)
	r := chi.NewRouter()
	r.Post("/api/v1/alerts/bulk/dismiss", h.BulkDismiss)

	t.Run("rejects empty, invalid UUID, and short reason", func(t *testing.T) {
		for _, body := range []string{
			`{"alert_ids":[],"reason":"Maintenance"}`,
			`{"alert_ids":["not-a-uuid"],"reason":"Maintenance"}`,
			`{"alert_ids":["11111111-1111-4111-8111-111111111111"],"reason":"ok"}`,
		} {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/alerts/bulk/dismiss", bytes.NewReader([]byte(body)))
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 Bad Request for %s, got %d (%s)", body, rec.Code, rec.Body.String())
			}
		}
	})

	t.Run("dismisses valid UUID batch with 200 OK", func(t *testing.T) {
		id1 := "11111111-1111-4111-8111-111111111111"
		id2 := "22222222-2222-4222-8222-222222222222"
		body := `{"alert_ids":["` + id1 + `","` + id2 + `","` + id1 + `"],"reason":"Planned deployment noise"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/alerts/bulk/dismiss", bytes.NewReader([]byte(body)))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d (%s)", rec.Code, rec.Body.String())
		}
		var resp model.BulkDismissResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if resp.DismissedCount != 2 || len(resp.AlertIDs) != 2 {
			t.Fatalf("expected 2 deduplicated dismissed alerts, got %+v", resp)
		}
	})

	t.Run("returns 409 ALERT_HAS_OPEN_INCIDENT on single and bulk dismiss when ticket is open", func(t *testing.T) {
		const id1 = "11111111-1111-4111-8111-111111111111"
		repo.dismissErr = apierror.New(http.StatusConflict, apierror.CodeAlertHasOpenIncident, "Cannot dismiss alert HighLag while ServiceNow incident INC0001234 is open.").
			WithDetails(map[string]any{"alert_id": id1, "incident_number": "INC0001234"})
		repo.bulkDismissErr = apierror.New(http.StatusConflict, apierror.CodeAlertHasOpenIncident, "Cannot dismiss alerts while ServiceNow incident(s) are open: HighLag (INC0001234).")

		r.Post("/api/v1/alerts/{alert_id}/dismiss", h.Dismiss)

		sReq := httptest.NewRequest(http.MethodPost, "/api/v1/alerts/"+id1+"/dismiss", bytes.NewReader([]byte(`{"version":1,"reason":"Trying to dismiss"}`)))
		sRec := httptest.NewRecorder()
		r.ServeHTTP(sRec, sReq)
		if sRec.Code != http.StatusConflict || !bytes.Contains(sRec.Body.Bytes(), []byte(apierror.CodeAlertHasOpenIncident)) {
			t.Fatalf("expected 409 ALERT_HAS_OPEN_INCIDENT on single dismiss, got %d (%s)", sRec.Code, sRec.Body.String())
		}

		bReq := httptest.NewRequest(http.MethodPost, "/api/v1/alerts/bulk/dismiss", bytes.NewReader([]byte(`{"alert_ids":["`+id1+`"],"reason":"Trying bulk dismiss"}`)))
		bRec := httptest.NewRecorder()
		r.ServeHTTP(bRec, bReq)
		if bRec.Code != http.StatusConflict || !bytes.Contains(bRec.Body.Bytes(), []byte("INC0001234")) {
			t.Fatalf("expected 409 ALERT_HAS_OPEN_INCIDENT on bulk dismiss, got %d (%s)", bRec.Code, bRec.Body.String())
		}
	})
}

func TestIncidentActiveStatusEndpointAndAuth(t *testing.T) {
	const snowSysID = "51615243c0a801640139e875df691a31"
	const alertID = "11111111-1111-4111-8111-111111111111"

	svc := &stubIncidentService{
		activeStatusResp: &model.IncidentActiveStatusResponse{
			SnowSysID:        snowSysID,
			HasActiveAlerts:  true,
			ActiveAlertCount: 1,
			ActiveAlerts: []model.ActiveAlertSummary{
				{
					ID:        alertID,
					AlertName: "HighDatabaseReplicationLag",
					Severity:  "critical",
					State:     model.AlertStateAcknowledged,
				},
			},
		},
	}
	h := NewServiceNowHandler(svc)
	r := chi.NewRouter()
	r.With(auth.ServiceNowWebhookMiddleware("snow-shared-secret", 1024*1024)).
		Get("/api/v1/integrations/servicenow/incidents/{snow_sys_id}/active-status", h.IncidentActiveStatus)

	t.Run("rejects missing or invalid bearer secret with 401", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/servicenow/incidents/"+snowSysID+"/active-status", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 without Authorization header, got %d", rec.Code)
		}

		badReq := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/servicenow/incidents/"+snowSysID+"/active-status", nil)
		badReq.Header.Set("Authorization", "Bearer wrong-secret")
		badRec := httptest.NewRecorder()
		r.ServeHTTP(badRec, badReq)
		if badRec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 with invalid secret, got %d", badRec.Code)
		}
	})

	t.Run("returns 200 active alert summary when authenticated", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/servicenow/incidents/"+snowSysID+"/active-status", nil)
		req.Header.Set("Authorization", "Bearer snow-shared-secret")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d (%s)", rec.Code, rec.Body.String())
		}
		var body model.IncidentActiveStatusResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if !body.HasActiveAlerts || body.ActiveAlertCount != 1 || len(body.ActiveAlerts) != 1 || body.ActiveAlerts[0].AlertName != "HighDatabaseReplicationLag" {
			t.Fatalf("unexpected active-status body: %+v", body)
		}
	})

	t.Run("propagates 400 validation error for invalid snow_sys_id", func(t *testing.T) {
		svc.activeStatusErr = apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "invalid snow_sys_id")
		req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/servicenow/incidents/bad.sysid/active-status", nil)
		req.Header.Set("Authorization", "Bearer snow-shared-secret")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 Bad Request, got %d (%s)", rec.Code, rec.Body.String())
		}
	})
}

func TestAlertsHandlerHistoryPagination(t *testing.T) {
	const alertID = "11111111-1111-4111-8111-111111111111"
	repo := &stubAlertStore{}
	h := NewAlertsHandler(repo)
	r := chi.NewRouter()
	r.Get("/api/v1/alerts/{alert_id}/history", h.History)

	t.Run("defaults limit=20 and offset=0 and accepts explicit pagination", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/alerts/"+alertID+"/history", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || repo.lastHistoryLim != 20 || repo.lastHistoryOff != 0 {
			t.Fatalf("unexpected default history pagination: code=%d limit=%d offset=%d", rec.Code, repo.lastHistoryLim, repo.lastHistoryOff)
		}

		req2 := httptest.NewRequest(http.MethodGet, "/api/v1/alerts/"+alertID+"/history?limit=15&offset=30", nil)
		rec2 := httptest.NewRecorder()
		r.ServeHTTP(rec2, req2)
		if rec2.Code != http.StatusOK || repo.lastHistoryLim != 15 || repo.lastHistoryOff != 30 {
			t.Fatalf("unexpected explicit history pagination: code=%d limit=%d offset=%d", rec2.Code, repo.lastHistoryLim, repo.lastHistoryOff)
		}
	})

	t.Run("rejects out-of-range limit or negative offset", func(t *testing.T) {
		for _, urlStr := range []string{
			"/api/v1/alerts/" + alertID + "/history?limit=0",
			"/api/v1/alerts/" + alertID + "/history?limit=101",
			"/api/v1/alerts/" + alertID + "/history?offset=-1",
		} {
			req := httptest.NewRequest(http.MethodGet, urlStr, nil)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for %s, got %d", urlStr, rec.Code)
			}
		}
	})
}

func TestHardeningMiddlewareAndLegacyAuthWarning(t *testing.T) {
	t.Run("SecurityHeadersMiddleware sets standard security headers and HSTS when TLS active", func(t *testing.T) {
		h := SecurityHeadersMiddleware(true)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		req := httptest.NewRequest(http.MethodGet, "/api/v1/alerts", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Header().Get("X-Content-Type-Options") != "nosniff" ||
			rec.Header().Get("X-Frame-Options") != "DENY" ||
			rec.Header().Get("Referrer-Policy") != "no-referrer" ||
			rec.Header().Get("Strict-Transport-Security") == "" {
			t.Fatalf("missing expected security headers: %+v", rec.Header())
		}
	})

	t.Run("CORSMiddleware enforces origin allowlist and handles preflight", func(t *testing.T) {
		h := CORSMiddleware([]string{"https://grafana.corp.example"})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

		okReq := httptest.NewRequest(http.MethodOptions, "/api/v1/alerts", nil)
		okReq.Header.Set("Origin", "https://grafana.corp.example")
		okReq.Header.Set("Access-Control-Request-Method", "GET")
		okRec := httptest.NewRecorder()
		h.ServeHTTP(okRec, okReq)
		if okRec.Code != http.StatusNoContent || okRec.Header().Get("Access-Control-Allow-Origin") != "https://grafana.corp.example" {
			t.Fatalf("expected 204 CORS preflight for allowed origin, got %d (%+v)", okRec.Code, okRec.Header())
		}

		badReq := httptest.NewRequest(http.MethodOptions, "/api/v1/alerts", nil)
		badReq.Header.Set("Origin", "https://evil.example")
		badReq.Header.Set("Access-Control-Request-Method", "GET")
		badRec := httptest.NewRecorder()
		h.ServeHTTP(badRec, badReq)
		if badRec.Code != http.StatusForbidden {
			t.Fatalf("expected 403 CORS preflight for disallowed origin, got %d", badRec.Code)
		}
	})

	t.Run("RateLimitMiddleware enforces token bucket burst and emits 429 RATE_LIMITED", func(t *testing.T) {
		h := RateLimitMiddleware(1, 2, false)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

		for i := 0; i < 2; i++ {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/alertmanager", nil)
			req.RemoteAddr = "10.0.0.5:12345"
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected burst request %d to pass, got %d", i+1, rec.Code)
			}
		}

		req3 := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/alertmanager", nil)
		req3.RemoteAddr = "10.0.0.5:12345"
		rec3 := httptest.NewRecorder()
		h.ServeHTTP(rec3, req3)
		if rec3.Code != http.StatusTooManyRequests ||
			rec3.Header().Get("Retry-After") == "" ||
			!strings.Contains(rec3.Body.String(), apierror.CodeRateLimited) {
			t.Fatalf("expected 429 RATE_LIMITED with Retry-After, got code=%d headers=%+v body=%s", rec3.Code, rec3.Header(), rec3.Body.String())
		}
	})

	t.Run("NewAPIRouter emits startup warning when AllowLegacyProxyHeaderAuth is true", func(t *testing.T) {
		var logBuf bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
		cfg := &config.Config{
			JWTSecret:                  "test-secret",
			JWTIssuer:                  "noc-alert-manager-app",
			AllowLegacyProxyHeaderAuth: true,
			ProxySharedToken:           "legacy-token",
			AlertmanagerWebhookToken:   "am-token",
			RateLimitEnabled:           true,
			RateLimitAPIRPS:            10,
			RateLimitAPIBurst:          10,
			RateLimitWebhookRPS:        5,
			RateLimitWebhookBurst:      2,
		}
		router := NewAPIRouter(cfg, logger, nil, NewAlertsHandler(&stubAlertStore{}), NewServiceNowHandler(&stubIncidentService{}), &WebhooksHandler{})

		if !strings.Contains(logBuf.String(), "ALLOW_LEGACY_PROXY_HEADER_AUTH is enabled") {
			t.Fatalf("expected startup warning log for legacy proxy auth, got %s", logBuf.String())
		}

		// Verify webhook route gets rate limited when burst=2 is exceeded
		for i := 0; i < 3; i++ {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/alertmanager", nil)
			req.RemoteAddr = "192.0.2.10:5555"
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if i < 2 && rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401 from webhook auth before rate limit hit on call %d, got %d", i+1, rec.Code)
			}
			if i == 2 && rec.Code != http.StatusTooManyRequests {
				t.Fatalf("expected 429 RATE_LIMITED on 3rd webhook call, got %d", rec.Code)
			}
		}
	})
}

type stubConfluenceUpdater struct {
	ret *model.Alert
	err error
}

func (s *stubConfluenceUpdater) UpdateAlertConfluenceURL(_ context.Context, _ string, _ auth.OperatorContext, _ string) (*model.Alert, error) {
	return s.ret, s.err
}

func TestAlertsHandlerUpdateConfluenceURL(t *testing.T) {
	const alertUUID = "11111111-1111-4111-8111-111111111111"
	validURL := "https://confluence.corp.example/display/NOC/HighCPU"

	repo := &stubAlertStore{}
	conf := &stubConfluenceUpdater{
		ret: &model.Alert{
			ID:                  alertUUID,
			AlertName:           "HighCPU",
			ConfluenceURL:       &validURL,
			ConfluenceURLSource: model.ConfluenceSourceOverride,
		},
	}
	h := NewAlertsHandler(repo, conf)
	r := chi.NewRouter()
	r.With(auth.RequirePermission(auth.PermAlertsTriage)).
		Post("/api/v1/alerts/{alert_id}/confluence", h.UpdateConfluenceURL)

	t.Run("rejects Viewer with 403 FORBIDDEN_RBAC", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/alerts/"+alertUUID+"/confluence", bytes.NewReader([]byte(`{"confluence_url":"https://confluence.corp.example/page"}`)))
		req = req.WithContext(auth.WithOperator(req.Context(), auth.OperatorContext{Login: "viewer", Role: "Viewer"}))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected 403 for Viewer, got %d (%s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("rejects invalid UUID and propagates service validation error", func(t *testing.T) {
		badIDReq := httptest.NewRequest(http.MethodPost, "/api/v1/alerts/not-a-uuid/confluence", bytes.NewReader([]byte(`{"confluence_url":"https://confluence.corp.example/page"}`)))
		badIDReq = badIDReq.WithContext(auth.WithOperator(badIDReq.Context(), auth.OperatorContext{Login: "editor", Role: "Editor"}))
		badIDRec := httptest.NewRecorder()
		r.ServeHTTP(badIDRec, badIDReq)
		if badIDRec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for invalid UUID, got %d", badIDRec.Code)
		}

		conf.err = apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "confluence_url must use the https:// scheme")
		badURLReq := httptest.NewRequest(http.MethodPost, "/api/v1/alerts/"+alertUUID+"/confluence", bytes.NewReader([]byte(`{"confluence_url":"http://insecure.example"}`)))
		badURLReq = badURLReq.WithContext(auth.WithOperator(badURLReq.Context(), auth.OperatorContext{Login: "editor", Role: "Editor"}))
		badURLRec := httptest.NewRecorder()
		r.ServeHTTP(badURLRec, badURLReq)
		if badURLRec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for insecure URL, got %d", badURLRec.Code)
		}
	})

	t.Run("allows Editor to save valid HTTPS Confluence URL", func(t *testing.T) {
		conf.err = nil
		req := httptest.NewRequest(http.MethodPost, "/api/v1/alerts/"+alertUUID+"/confluence", bytes.NewReader([]byte(`{"confluence_url":"`+validURL+`"}`)))
		req = req.WithContext(auth.WithOperator(req.Context(), auth.OperatorContext{Login: "editor", Role: "Editor"}))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), validURL) {
			t.Fatalf("expected 200 OK with Confluence URL, got %d (%s)", rec.Code, rec.Body.String())
		}
	})
}
