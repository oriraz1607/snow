//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/oriraz/grafana-snow-integration/backend/internal/auth"
	"github.com/oriraz/grafana-snow-integration/backend/internal/mocksnow"
	"github.com/oriraz/grafana-snow-integration/backend/internal/model"
)

type testEnv struct {
	apiURL         string
	adminURL       string
	mockSnowURL    string
	alertmgrURL    string
	jwtSecret      string
	jwtIssuer      string
	amToken        string
	defaultNOCSys  string
	appDSN         string
	ownerDSN       string
	httpClient     *http.Client
}

func loadTestEnv(t *testing.T) *testEnv {
	t.Helper()
	get := func(key, def string) string {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
		return def
	}
	env := &testEnv{
		apiURL:        strings.TrimRight(get("INTEGRATION_API_URL", "http://127.0.0.1:8080"), "/"),
		adminURL:      strings.TrimRight(get("INTEGRATION_ADMIN_URL", "http://127.0.0.1:9090"), "/"),
		mockSnowURL:   strings.TrimRight(get("INTEGRATION_MOCKSNOW_URL", "http://127.0.0.1:8085"), "/"),
		alertmgrURL:   strings.TrimRight(get("INTEGRATION_ALERTMANAGER_URL", "http://127.0.0.1:9093"), "/"),
		jwtSecret:     get("GRAFANA_PLUGIN_JWT_SECRET", ""),
		jwtIssuer:     get("GRAFANA_PLUGIN_JWT_ISSUER", "noc-alert-manager-app"),
		amToken:       get("ALERTMANAGER_WEBHOOK_TOKEN", ""),
		defaultNOCSys: get("SNOW_DEFAULT_NOC_CALLER_SYS_ID", ""),
		appDSN:        get("INTEGRATION_APP_DATABASE_URL", ""),
		ownerDSN:      get("INTEGRATION_OWNER_DATABASE_URL", ""),
		httpClient:    &http.Client{Timeout: 10 * time.Second},
	}
	if env.jwtSecret == "" || env.amToken == "" || env.defaultNOCSys == "" || env.appDSN == "" || env.ownerDSN == "" {
		t.Fatalf("required integration test env vars missing (GRAFANA_PLUGIN_JWT_SECRET, ALERTMANAGER_WEBHOOK_TOKEN, SNOW_DEFAULT_NOC_CALLER_SYS_ID, INTEGRATION_APP_DATABASE_URL, INTEGRATION_OWNER_DATABASE_URL)")
	}
	return env
}

func (e *testEnv) signJWT(t *testing.T, login, email, name, role string) string {
	t.Helper()
	now := time.Now().UTC()
	claims := auth.OperatorClaims{
		Email: email,
		Name:  name,
		OrgID: 1,
		Role:  role,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    e.jwtIssuer,
			Audience:  jwt.ClaimStrings{auth.ExpectedAudience},
			Subject:   login,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(60 * time.Second)),
			ID:        fmt.Sprintf("jti-%d", now.UnixNano()),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, err := tok.SignedString([]byte(e.jwtSecret))
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}
	return s
}

func (e *testEnv) doJSON(t *testing.T, method, urlStr, bearer string, body any) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, urlStr, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := e.httpClient.Do(req)
	if err != nil {
		t.Fatalf("http %s %s failed: %v", method, urlStr, err)
	}
	defer resp.Body.Close()
	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return resp.StatusCode, respBytes
}

func (e *testEnv) resetState(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, e.ownerDSN)
	if err != nil {
		t.Fatalf("connect owner postgres: %v", err)
	}
	defer conn.Close(ctx)

	if _, err := conn.Exec(ctx, `TRUNCATE TABLE alerts, snow_user_cache, webhook_ingestion_log, alert_fingerprint_teams, idempotency_keys, team_assignment_group_mappings, alert_confluence_docs RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate tables: %v", err)
	}

	status, body := e.doJSON(t, http.MethodPost, e.mockSnowURL+"/_mock/reset", "", nil)
	if status != http.StatusOK {
		t.Fatalf("reset mocksnow failed: status=%d body=%s", status, string(body))
	}
}

func TestEndToEndPhase1Stack(t *testing.T) {
	env := loadTestEnv(t)
	env.resetState(t)

	t.Run("SchemaAndLeastPrivilegeAuditImmutability", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		ownerConn, err := pgx.Connect(ctx, env.ownerDSN)
		if err != nil {
			t.Fatalf("connect owner db: %v", err)
		}
		defer ownerConn.Close(ctx)

		var enumCount, tableCount, triggerCount, indexCount int
		if err := ownerConn.QueryRow(ctx, `
			SELECT COUNT(*) FROM pg_type t
			JOIN pg_namespace n ON n.oid = t.typnamespace
			WHERE n.nspname = 'public' AND t.typtype = 'e'
			  AND t.typname IN ('alert_state','snow_sync_status','snow_caller_mode','snow_caller_resolution_status','audit_event_type','actor_type')
		`).Scan(&enumCount); err != nil || enumCount != 6 {
			t.Fatalf("expected 6 enums, got %d (err=%v)", enumCount, err)
		}

		if err := ownerConn.QueryRow(ctx, `
			SELECT COUNT(*) FROM information_schema.tables
			WHERE table_schema = 'public'
			  AND table_name IN ('alerts','servicenow_incidents','alert_audit_log','snow_user_cache','webhook_ingestion_log','alert_fingerprint_teams','idempotency_keys','team_assignment_group_mappings','alert_confluence_docs')
		`).Scan(&tableCount); err != nil || tableCount != 9 {
			t.Fatalf("expected 9 tables, got %d (err=%v)", tableCount, err)
		}

		if err := ownerConn.QueryRow(ctx, `
			SELECT COUNT(*) FROM pg_trigger
			WHERE tgname IN ('trg_alerts_updated_at','trg_servicenow_incidents_updated_at','trg_alert_fingerprint_teams_updated_at','trg_idempotency_keys_updated_at','trg_team_assignment_group_mappings_updated_at','trg_alert_confluence_docs_updated_at')
		`).Scan(&triggerCount); err != nil || triggerCount != 6 {
			t.Fatalf("expected 6 triggers, got %d (err=%v)", triggerCount, err)
		}

		if err := ownerConn.QueryRow(ctx, `
			SELECT COUNT(*) FROM pg_indexes
			WHERE schemaname = 'public'
			  AND indexname IN (
			    'uq_alerts_active_fingerprint','idx_alerts_state_starts_at','idx_alerts_active_severity',
			    'idx_alerts_fingerprint_starts_at_desc','idx_alerts_labels_gin','idx_snow_incidents_open_sync',
			    'idx_snow_incidents_sys_id','idx_snow_incidents_number','idx_audit_log_alert_id_created',
			    'idx_snow_user_cache_expires_at','idx_webhook_ingestion_log_received_at',
			    'idx_alerts_non_active_ack','idx_idempotency_keys_expires_at','idx_alerts_active_severity_rank',
			    'idx_alerts_resolved_at_desc','idx_alerts_instance_lower_asc','idx_alerts_instance_lower_desc',
			    'idx_alerts_instance_exact'
			  )
		`).Scan(&indexCount); err != nil || indexCount != 18 {
			t.Fatalf("expected 18 indexes, got %d (err=%v)", indexCount, err)
		}

		// Verify alert_api_app cannot UPDATE or DELETE alert_audit_log
		appConn, err := pgx.Connect(ctx, env.appDSN)
		if err != nil {
			t.Fatalf("connect app db: %v", err)
		}
		defer appConn.Close(ctx)

		for _, stmt := range []string{
			`UPDATE alert_audit_log SET comment = 'tampered'`,
			`DELETE FROM alert_audit_log`,
		} {
			_, err := appConn.Exec(ctx, stmt)
			var pgErr *pgconn.PgError
			if err == nil || !errors.As(err, &pgErr) || pgErr.Code != "42501" {
				t.Fatalf("expected PostgreSQL 42501 permission denied for %q, got err=%v", stmt, err)
			}
		}
	})

	aliceJWT := env.signJWT(t, "alice.smith", "alice.smith@corp.example", "Alice Smith", "Editor")
	viewerJWT := env.signJWT(t, "viewer.user", "viewer@corp.example", "Viewer User", "Viewer")
	missingSSOJWT := env.signJWT(t, "missing.sso", "missing.sso@corp.example", "Missing SSO", "Editor")

	var flow1AlertID string
	var flow1Fingerprint string
	var flow1StartsAt time.Time
	var flow1SnowSysID string
	var flow1IncidentNum string

	t.Run("Flow1_AlertmanagerDelivery_Ack_CreateSNOW_SSOMatch_And_RBAC", func(t *testing.T) {
		startsAt := time.Now().UTC().Add(-1 * time.Minute).Truncate(time.Second)
		endsAt := time.Now().UTC().Add(30 * time.Minute).Truncate(time.Second)

		amAlerts := []map[string]any{
			{
				"labels": map[string]string{
					"alertname": "HighReplicationLag",
					"severity":  "critical",
					"cluster":   "e2e-eu1",
					"service":   "orders-db",
					"run_id":    fmt.Sprintf("%d", time.Now().UnixNano()),
				},
				"annotations": map[string]string{
					"summary":     "Replication lag > 30s on orders-db",
					"runbook_url": "https://runbooks.example.com/db-lag",
				},
				"startsAt":     startsAt.Format(time.RFC3339),
				"endsAt":       endsAt.Format(time.RFC3339),
				"generatorURL": "https://prometheus.example.com/graph",
			},
		}
		st, body := env.doJSON(t, http.MethodPost, env.alertmgrURL+"/api/v2/alerts", "", amAlerts)
		if st != http.StatusOK {
			t.Fatalf("push alert to alertmanager failed: status=%d body=%s", st, string(body))
		}

		// Wait for Alertmanager -> API webhook delivery
		deadline := time.Now().Add(15 * time.Second)
		for {
			code, listRaw := env.doJSON(t, http.MethodGet, env.apiURL+"/api/v1/alerts?search=HighReplicationLag&state=firing", viewerJWT, nil)
			if code == http.StatusOK {
				var listResp model.ListAlertsResponse
				if err := json.Unmarshal(listRaw, &listResp); err == nil && len(listResp.Items) > 0 {
					flow1AlertID = listResp.Items[0].ID
					flow1Fingerprint = listResp.Items[0].Fingerprint
					flow1StartsAt = listResp.Items[0].StartsAt
					break
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for Alertmanager webhook delivery into Go API")
			}
			time.Sleep(500 * time.Millisecond)
		}

		// Viewer cannot acknowledge
		st, _ = env.doJSON(t, http.MethodPost, env.apiURL+"/api/v1/alerts/"+flow1AlertID+"/acknowledge", viewerJWT, map[string]any{"version": 1})
		if st != http.StatusForbidden {
			t.Fatalf("expected 403 FORBIDDEN_RBAC for Viewer ack, got %d", st)
		}

		// Editor standalone acknowledge is blocked without a ticket
		st, ackBody := env.doJSON(t, http.MethodPost, env.apiURL+"/api/v1/alerts/"+flow1AlertID+"/acknowledge", aliceJWT, map[string]any{
			"version": 1,
			"comment": "Investigating WAL lag",
		})
		if st != http.StatusConflict || !strings.Contains(string(ackBody), "TICKET_REQUIRED_FOR_ACKNOWLEDGE") {
			t.Fatalf("expected 409 TICKET_REQUIRED_FOR_ACKNOWLEDGE for standalone ack, got status=%d body=%s", st, string(ackBody))
		}

		// Create SNOW incident directly from firing alert -> atomically acknowledges + creates ticket
		st, incRaw := env.doJSON(t, http.MethodPost, env.apiURL+"/api/v1/alerts/"+flow1AlertID+"/servicenow/incident", aliceJWT, map[string]any{
			"short_description": "[CRITICAL] HighReplicationLag on orders-db",
			"impact":            1,
			"urgency":           1,
			"assignment_group":  "Database SRE",
			"caller_mode":       "sso_user",
			"work_notes":        "Escalating to Database SRE",
		})
		if st != http.StatusCreated {
			t.Fatalf("create SNOW incident failed: status=%d body=%s", st, string(incRaw))
		}
		var incResp model.CreateIncidentResponse
		if err := json.Unmarshal(incRaw, &incResp); err != nil {
			t.Fatalf("unmarshal create incident resp: %v", err)
		}
		if incResp.ServiceNowIncident.CallerResolutionStatus != model.CallerResSSOMatched ||
			*incResp.ServiceNowIncident.CallerSysID != "u_alice_001" ||
			len(incResp.Warnings) != 0 {
			t.Fatalf("expected sso_matched u_alice_001 with 0 warnings, got %+v warnings=%v", incResp.ServiceNowIncident, incResp.Warnings)
		}
		st, detailRaw := env.doJSON(t, http.MethodGet, env.apiURL+"/api/v1/alerts/"+flow1AlertID, aliceJWT, nil)
		if st != http.StatusOK {
			t.Fatalf("get flow1 alert after incident create failed: %d", st)
		}
		var acked model.AlertDetail
		_ = json.Unmarshal(detailRaw, &acked)
		if acked.State != model.AlertStateAcknowledged || acked.Version != 2 || acked.AcknowledgedBy == nil || *acked.AcknowledgedBy != "alice.smith" {
			t.Fatalf("expected alert to be atomically acknowledged on SNOW create, got %+v", acked)
		}
		flow1SnowSysID = *incResp.ServiceNowIncident.SnowSysID
		flow1IncidentNum = *incResp.ServiceNowIncident.IncidentNumber
	})

	var flow2AlertID string
	var flow2Fingerprint = "fp_fallback_e2e_02"
	var flow2StartsAt = time.Now().UTC().Add(-5 * time.Minute).Truncate(time.Second)
	var flow2SnowSysID string

	t.Run("Flow2_MissingSSOUserFallbackToDefaultNOC", func(t *testing.T) {
		whPayload := model.AlertmanagerWebhookPayload{
			Version:  "4",
			GroupKey: "gk-fallback",
			Receiver: "alert-api",
			Alerts: []model.AlertmanagerAlert{
				{
					Status:      "firing",
					Fingerprint: flow2Fingerprint,
					StartsAt:    flow2StartsAt,
					EndsAt:      time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC),
					Labels: map[string]string{
						"alertname": "NodeMemoryHigh",
						"severity":  "warning",
						"cluster":   "e2e-eu1",
					},
					Annotations: map[string]string{"summary": "Memory > 92%"},
				},
			},
		}
		st, body := env.doJSON(t, http.MethodPost, env.apiURL+"/api/v1/webhooks/alertmanager", env.amToken, whPayload)
		if st != http.StatusOK {
			t.Fatalf("webhook ingest failed: status=%d body=%s", st, string(body))
		}

		st, listRaw := env.doJSON(t, http.MethodGet, env.apiURL+"/api/v1/alerts?fingerprint="+flow2Fingerprint, aliceJWT, nil)
		if st != http.StatusOK {
			t.Fatalf("list by fingerprint failed: %d", st)
		}
		var listResp model.ListAlertsResponse
		_ = json.Unmarshal(listRaw, &listResp)
		flow2AlertID = listResp.Items[0].ID

		st, incRaw := env.doJSON(t, http.MethodPost, env.apiURL+"/api/v1/alerts/"+flow2AlertID+"/servicenow/incident", missingSSOJWT, map[string]any{
			"short_description": "[WARNING] NodeMemoryHigh",
			"impact":            2,
			"urgency":           2,
			"caller_mode":       "sso_user",
		})
		if st != http.StatusCreated {
			t.Fatalf("create incident flow2 failed: status=%d body=%s", st, string(incRaw))
		}
		var incResp model.CreateIncidentResponse
		_ = json.Unmarshal(incRaw, &incResp)
		if incResp.ServiceNowIncident.CallerResolutionStatus != model.CallerResFallbackNOC ||
			*incResp.ServiceNowIncident.CallerSysID != env.defaultNOCSys ||
			len(incResp.Warnings) == 0 {
			t.Fatalf("expected fallback_noc with warning, got %+v warnings=%v", incResp.ServiceNowIncident, incResp.Warnings)
		}
		flow2SnowSysID = *incResp.ServiceNowIncident.SnowSysID

		st, d2Raw := env.doJSON(t, http.MethodGet, env.apiURL+"/api/v1/alerts/"+flow2AlertID, aliceJWT, nil)
		if st != http.StatusOK {
			t.Fatalf("get flow2 detail failed: %d", st)
		}
		var d2 model.AlertDetail
		_ = json.Unmarshal(d2Raw, &d2)
		if d2.State != model.AlertStateAcknowledged {
			t.Fatalf("expected flow2 alert to be acknowledged after incident creation, got %s", d2.State)
		}

		// Check snow_caller_fallback audit event
		st, histRaw := env.doJSON(t, http.MethodGet, env.apiURL+"/api/v1/alerts/"+flow2AlertID+"/history", aliceJWT, nil)
		if st != http.StatusOK {
			t.Fatalf("history failed: %d", st)
		}
		var hist model.AlertHistoryResponse
		_ = json.Unmarshal(histRaw, &hist)
		foundFallbackAudit := false
		for _, ev := range hist.AuditEvents {
			if ev.EventType == model.AuditSnowCallerFallback {
				foundFallbackAudit = true
				break
			}
		}
		if !foundFallbackAudit {
			t.Fatalf("expected snow_caller_fallback audit event in history")
		}
	})

	t.Run("Flow3_DriftDetection_And_AsyncResolutionNote", func(t *testing.T) {
		// Move Flow 1 SNOW ticket to In Progress (state=2)
		st, body := env.doJSON(t, http.MethodPost, env.mockSnowURL+"/_mock/incidents/"+flow1SnowSysID+"/transition", "", map[string]any{
			"state":        2,
			"send_webhook": true,
		})
		if st != http.StatusOK {
			t.Fatalf("mocksnow transition to In Progress failed: status=%d body=%s", st, string(body))
		}

		// Resolve Flow 1 alert via Alertmanager webhook
		resolvedAt := time.Now().UTC().Truncate(time.Second)
		st, body = env.doJSON(t, http.MethodPost, env.apiURL+"/api/v1/webhooks/alertmanager", env.amToken, model.AlertmanagerWebhookPayload{
			Version:  "4",
			GroupKey: "gk-flow1",
			Receiver: "alert-api",
			Alerts: []model.AlertmanagerAlert{
				{
					Status:      "resolved",
					Fingerprint: flow1Fingerprint,
					StartsAt:    flow1StartsAt,
					EndsAt:      resolvedAt,
					Labels:      map[string]string{"alertname": "HighReplicationLag", "severity": "critical"},
				},
			},
		})
		if st != http.StatusOK {
			t.Fatalf("resolved webhook failed: status=%d body=%s", st, string(body))
		}

		// Verify drift filter shows Flow 1
		st, driftRaw := env.doJSON(t, http.MethodGet, env.apiURL+"/api/v1/alerts?incident_drift=resolved_open_ticket", aliceJWT, nil)
		if st != http.StatusOK {
			t.Fatalf("drift list failed: %d", st)
		}
		var driftResp model.ListAlertsResponse
		_ = json.Unmarshal(driftRaw, &driftResp)
		found := false
		for _, item := range driftResp.Items {
			if item.ID == flow1AlertID && item.IncidentDriftStatus == model.DriftResolvedOpenTicket {
				found = true
			}
		}
		if !found || driftResp.Summary.ResolvedOpenTicket < 1 {
			t.Fatalf("expected Flow 1 in resolved_open_ticket drift query; summary=%+v items=%+v", driftResp.Summary, driftResp.Items)
		}

		// Verify async resolution note arrives on mocksnow and stamps resolution_note_posted_at
		deadline := time.Now().Add(10 * time.Second)
		noteVerified := false
		for time.Now().Before(deadline) {
			st, mockRaw := env.doJSON(t, http.MethodGet, env.mockSnowURL+"/_mock/incidents", "", nil)
			if st == http.StatusOK {
				var list struct {
					Incidents []mocksnow.MockIncident `json:"incidents"`
				}
				_ = json.Unmarshal(mockRaw, &list)
				for _, inc := range list.Incidents {
					if inc.SysID == flow1SnowSysID {
						for _, note := range inc.WorkNotes {
							if strings.Contains(note, "RESOLVED") {
								noteVerified = true
								break
							}
						}
					}
				}
			}
			if noteVerified {
				stDetail, detailRaw := env.doJSON(t, http.MethodGet, env.apiURL+"/api/v1/alerts/"+flow1AlertID, aliceJWT, nil)
				if stDetail == http.StatusOK {
					var detail model.AlertDetail
					_ = json.Unmarshal(detailRaw, &detail)
					if detail.ServiceNowIncident != nil && detail.ServiceNowIncident.ResolutionNotePostedAt != nil {
						break
					}
				}
			}
			time.Sleep(250 * time.Millisecond)
		}
		if !noteVerified {
			t.Fatalf("timed out waiting for async resolution work note on mocksnow")
		}
	})

	t.Run("Flow4_BiDirectionalSync_WebhookAndPoller", func(t *testing.T) {
		// 4a. Resolve Flow 1 ticket via HMAC webhook
		st, body := env.doJSON(t, http.MethodPost, env.mockSnowURL+"/_mock/incidents/"+flow1SnowSysID+"/transition", "", map[string]any{
			"state":        6,
			"updated_by":   "dba.oncall",
			"close_notes":  "Lag cleared after vacuum",
			"send_webhook": true,
		})
		if st != http.StatusOK {
			t.Fatalf("transition flow1 resolved via webhook failed: %d (%s)", st, string(body))
		}

		st, detailRaw := env.doJSON(t, http.MethodGet, env.apiURL+"/api/v1/alerts/"+flow1AlertID, aliceJWT, nil)
		if st != http.StatusOK {
			t.Fatalf("get flow1 detail failed: %d", st)
		}
		var detail model.AlertDetail
		_ = json.Unmarshal(detailRaw, &detail)
		if detail.IncidentDriftStatus != model.DriftNone || *detail.ServiceNowIncident.SnowState != 6 {
			t.Fatalf("expected Flow 1 drift=none and snow_state=6, got drift=%s inc=%+v", detail.IncidentDriftStatus, detail.ServiceNowIncident)
		}

		// 4b. Resolve Flow 2 alert, then silently resolve Flow 2 ticket (NO webhook) so background poller heals it
		st, _ = env.doJSON(t, http.MethodPost, env.apiURL+"/api/v1/webhooks/alertmanager", env.amToken, model.AlertmanagerWebhookPayload{
			Version:  "4",
			GroupKey: "gk-fallback",
			Receiver: "alert-api",
			Alerts: []model.AlertmanagerAlert{
				{
					Status:      "resolved",
					Fingerprint: flow2Fingerprint,
					StartsAt:    flow2StartsAt,
					EndsAt:      time.Now().UTC(),
					Labels:      map[string]string{"alertname": "NodeMemoryHigh", "severity": "warning"},
				},
			},
		})
		if st != http.StatusOK {
			t.Fatalf("resolve flow2 webhook failed: %d", st)
		}

		st, _ = env.doJSON(t, http.MethodPost, env.mockSnowURL+"/_mock/incidents/"+flow2SnowSysID+"/transition", "", map[string]any{
			"state":        6,
			"send_webhook": false,
		})
		if st != http.StatusOK {
			t.Fatalf("silent transition flow2 failed: %d", st)
		}

		deadline := time.Now().Add(12 * time.Second)
		pollerCleared := false
		for time.Now().Before(deadline) {
			st, raw := env.doJSON(t, http.MethodGet, env.apiURL+"/api/v1/alerts/"+flow2AlertID, aliceJWT, nil)
			if st == http.StatusOK {
				var d model.AlertDetail
				_ = json.Unmarshal(raw, &d)
				if d.IncidentDriftStatus == model.DriftNone && d.ServiceNowIncident != nil && d.ServiceNowIncident.SnowState != nil && *d.ServiceNowIncident.SnowState == 6 {
					pollerCleared = true
					break
				}
			}
			time.Sleep(400 * time.Millisecond)
		}
		if !pollerCleared {
			t.Fatalf("timed out waiting for background poller to sync Flow 2 ticket resolution and clear drift")
		}
	})

	t.Run("Flow5_RefireCreatesFreshUnacknowledgedEpisode", func(t *testing.T) {
		newStartsAt := flow1StartsAt.Add(10 * time.Minute).UTC()
		st, body := env.doJSON(t, http.MethodPost, env.apiURL+"/api/v1/webhooks/alertmanager", env.amToken, model.AlertmanagerWebhookPayload{
			Version:  "4",
			GroupKey: "gk-flow1",
			Receiver: "alert-api",
			Alerts: []model.AlertmanagerAlert{
				{
					Status:      "firing",
					Fingerprint: flow1Fingerprint,
					StartsAt:    newStartsAt,
					EndsAt:      time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC),
					Labels:      map[string]string{"alertname": "HighReplicationLag", "severity": "critical", "cluster": "e2e-eu1"},
				},
			},
		})
		if st != http.StatusOK {
			t.Fatalf("re-fire webhook failed: %d (%s)", st, string(body))
		}

		st, listRaw := env.doJSON(t, http.MethodGet, env.apiURL+"/api/v1/alerts?fingerprint="+flow1Fingerprint+"&state=firing", aliceJWT, nil)
		if st != http.StatusOK {
			t.Fatalf("list refired episode failed: %d", st)
		}
		var listResp model.ListAlertsResponse
		_ = json.Unmarshal(listRaw, &listResp)
		if len(listResp.Items) != 1 {
			t.Fatalf("expected 1 active firing episode, got %d", len(listResp.Items))
		}
		ep2 := listResp.Items[0]
		if ep2.ID == flow1AlertID || ep2.State != model.AlertStateFiring || ep2.AcknowledgedBy != nil || ep2.Version != 1 || ep2.PreviousEpisodeCount < 1 {
			t.Fatalf("re-fired episode did not start fresh: %+v", ep2)
		}

		st, histRaw := env.doJSON(t, http.MethodGet, env.apiURL+"/api/v1/alerts/"+ep2.ID+"/history", aliceJWT, nil)
		if st != http.StatusOK {
			t.Fatalf("history on refired episode failed: %d", st)
		}
		var hist model.AlertHistoryResponse
		_ = json.Unmarshal(histRaw, &hist)
		if len(hist.PreviousEpisodes) == 0 || hist.PreviousEpisodes[0].IncidentNumber == nil || *hist.PreviousEpisodes[0].IncidentNumber != flow1IncidentNum {
			t.Fatalf("expected previous episode linking to %s, got %+v", flow1IncidentNum, hist.PreviousEpisodes)
		}
	})

	t.Run("Flow6_ConcurrencyAndTimeoutCorrelationIdRecovery", func(t *testing.T) {
		const concFP = "fp_conc_e2e_06"
		st, _ := env.doJSON(t, http.MethodPost, env.apiURL+"/api/v1/webhooks/alertmanager", env.amToken, model.AlertmanagerWebhookPayload{
			Version:  "4",
			GroupKey: "gk-conc",
			Receiver: "alert-api",
			Alerts: []model.AlertmanagerAlert{
				{
					Status:      "firing",
					Fingerprint: concFP,
					StartsAt:    time.Now().UTC().Add(-2 * time.Minute),
					EndsAt:      time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC),
					Labels:      map[string]string{"alertname": "ConcurrentAlert", "severity": "critical"},
				},
			},
		})
		if st != http.StatusOK {
			t.Fatalf("ingest conc alert failed: %d", st)
		}
		_, listRaw := env.doJSON(t, http.MethodGet, env.apiURL+"/api/v1/alerts?fingerprint="+concFP, aliceJWT, nil)
		var lr model.ListAlertsResponse
		_ = json.Unmarshal(listRaw, &lr)
		concAlertID := lr.Items[0].ID

		// 10 parallel ServiceNow incident creations directly on firing alert (atomic reserve + single winner)
		const workers = 10
		var wg sync.WaitGroup
		incCodes := make(chan int, workers)
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				jwtTok := env.signJWT(t, fmt.Sprintf("op.%d", idx), "alice.smith@corp.example", "Op", "Editor")
				code, _ := env.doJSON(t, http.MethodPost, env.apiURL+"/api/v1/alerts/"+concAlertID+"/servicenow/incident", jwtTok, map[string]any{
					"short_description": "[CRITICAL] Concurrent SNOW test",
					"impact":            1,
					"urgency":           1,
					"caller_mode":       "sso_user",
				})
				incCodes <- code
			}(i)
		}
		wg.Wait()
		close(incCodes)
		var createdInc, conflictInc int
		for c := range incCodes {
			switch c {
			case http.StatusCreated:
				createdInc++
			case http.StatusConflict:
				conflictInc++
			default:
				t.Fatalf("unexpected concurrent create status %d", c)
			}
		}
		if createdInc != 1 || conflictInc != workers-1 {
			t.Fatalf("expected 1 201 and %d 409 on concurrent SNOW create, got created=%d conflict=%d", workers-1, createdInc, conflictInc)
		}
		st, concDetailRaw := env.doJSON(t, http.MethodGet, env.apiURL+"/api/v1/alerts/"+concAlertID, aliceJWT, nil)
		if st != http.StatusOK {
			t.Fatalf("get concAlert failed: %d", st)
		}
		var concDetail model.AlertDetail
		_ = json.Unmarshal(concDetailRaw, &concDetail)
		if concDetail.State != model.AlertStateAcknowledged {
			t.Fatalf("expected concAlert to transition to acknowledged, got %s", concDetail.State)
		}

		// Post-commit timeout recovery via correlation_id: alert stays firing on failure, acks only on retry adoption
		const timeoutFP = "fp_timeout_e2e_06"
		st, _ = env.doJSON(t, http.MethodPost, env.apiURL+"/api/v1/webhooks/alertmanager", env.amToken, model.AlertmanagerWebhookPayload{
			Version:  "4",
			GroupKey: "gk-timeout",
			Receiver: "alert-api",
			Alerts: []model.AlertmanagerAlert{
				{
					Status:      "firing",
					Fingerprint: timeoutFP,
					StartsAt:    time.Now().UTC().Add(-2 * time.Minute),
					EndsAt:      time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC),
					Labels:      map[string]string{"alertname": "TimeoutRecoveryAlert", "severity": "critical"},
				},
			},
		})
		if st != http.StatusOK {
			t.Fatalf("ingest timeout alert failed: %d", st)
		}
		_, listRaw2 := env.doJSON(t, http.MethodGet, env.apiURL+"/api/v1/alerts?fingerprint="+timeoutFP, aliceJWT, nil)
		var lr2 model.ListAlertsResponse
		_ = json.Unmarshal(listRaw2, &lr2)
		timeoutAlertID := lr2.Items[0].ID

		// Fault: commit in mocksnow then sleep 3500ms (test compose sets SNOW_HTTP_TIMEOUT_SECONDS=2)
		st, _ = env.doJSON(t, http.MethodPost, env.mockSnowURL+"/_mock/faults", "", map[string]any{
			"create_timeout_after_commit_ms": 3500,
		})
		if st != http.StatusOK {
			t.Fatalf("set timeout fault failed: %d", st)
		}

		st, timeoutResp := env.doJSON(t, http.MethodPost, env.apiURL+"/api/v1/alerts/"+timeoutAlertID+"/servicenow/incident", aliceJWT, map[string]any{
			"short_description": "[CRITICAL] Timeout recovery test",
			"impact":            1,
			"urgency":           1,
		})
		if st != http.StatusGatewayTimeout {
			t.Fatalf("expected 504 SNOW_TIMEOUT on slow commit, got %d (%s)", st, string(timeoutResp))
		}
		st, afterFailRaw := env.doJSON(t, http.MethodGet, env.apiURL+"/api/v1/alerts/"+timeoutAlertID, aliceJWT, nil)
		if st != http.StatusOK {
			t.Fatalf("get timeoutAlertID after 504 failed: %d", st)
		}
		var afterFailDetail model.AlertDetail
		_ = json.Unmarshal(afterFailRaw, &afterFailDetail)
		if afterFailDetail.State != model.AlertStateFiring {
			t.Fatalf("expected alert to remain firing after failed SNOW call (no ack without ticket), got %s", afterFailDetail.State)
		}

		// Clear fault and retry -> must adopt the already-committed ticket and acknowledge atomically with zero duplicates
		st, _ = env.doJSON(t, http.MethodPost, env.mockSnowURL+"/_mock/faults", "", map[string]any{})
		if st != http.StatusOK {
			t.Fatalf("clear faults failed: %d", st)
		}
		st, retryResp := env.doJSON(t, http.MethodPost, env.apiURL+"/api/v1/alerts/"+timeoutAlertID+"/servicenow/incident", aliceJWT, map[string]any{
			"short_description": "[CRITICAL] Timeout recovery test",
			"impact":            1,
			"urgency":           1,
		})
		if st != http.StatusCreated {
			t.Fatalf("expected 201 Created on retry adoption, got %d (%s)", st, string(retryResp))
		}
		st, afterRetryRaw := env.doJSON(t, http.MethodGet, env.apiURL+"/api/v1/alerts/"+timeoutAlertID, aliceJWT, nil)
		if st != http.StatusOK {
			t.Fatalf("get timeoutAlertID after retry failed: %d", st)
		}
		var afterRetryDetail model.AlertDetail
		_ = json.Unmarshal(afterRetryRaw, &afterRetryDetail)
		if afterRetryDetail.State != model.AlertStateAcknowledged {
			t.Fatalf("expected timeoutAlertID to transition to acknowledged after ticket adoption, got %s", afterRetryDetail.State)
		}

		// Verify mocksnow has only 1 incident for concAlertID and 1 for timeoutAlertID
		st, mockRaw := env.doJSON(t, http.MethodGet, env.mockSnowURL+"/_mock/incidents", "", nil)
		if st != http.StatusOK {
			t.Fatalf("list mock incidents failed: %d", st)
		}
		var incList struct {
			Incidents []mocksnow.MockIncident `json:"incidents"`
		}
		_ = json.Unmarshal(mockRaw, &incList)
		concMatches, timeoutMatches := 0, 0
		for _, inc := range incList.Incidents {
			if inc.CorrelationID == concAlertID {
				concMatches++
			}
			if inc.CorrelationID == timeoutAlertID {
				timeoutMatches++
			}
		}
		if concMatches != 1 || timeoutMatches != 1 {
			t.Fatalf("expected exactly 1 ticket each for concAlertID and timeoutAlertID, got conc=%d timeout=%d", concMatches, timeoutMatches)
		}
	})

	t.Run("Flow7_AdditionalEndpointsAndMetrics", func(t *testing.T) {
		// Dismiss -> Reopen -> Unacknowledge -> Notes -> Link -> WorkNotes -> Sync -> Metadata -> Metrics
		const fp = "fp_extra_e2e_07"
		t0 := time.Now().UTC().Add(-3 * time.Minute)
		st, _ := env.doJSON(t, http.MethodPost, env.apiURL+"/api/v1/webhooks/alertmanager", env.amToken, model.AlertmanagerWebhookPayload{
			Version:         "4",
			GroupKey:        "gk-extra",
			Receiver:        "alert-api",
			TruncatedAlerts: 1,
			Alerts: []model.AlertmanagerAlert{
				{
					Status:      "firing",
					Fingerprint: fp,
					StartsAt:    t0,
					EndsAt:      time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC),
					Labels:      map[string]string{"alertname": "ExtraAlert", "severity": "warning"},
				},
			},
		})
		if st != http.StatusOK {
			t.Fatalf("ingest extra alert failed: %d", st)
		}
		_, listRaw := env.doJSON(t, http.MethodGet, env.apiURL+"/api/v1/alerts?fingerprint="+fp, aliceJWT, nil)
		var lr model.ListAlertsResponse
		_ = json.Unmarshal(listRaw, &lr)
		id := lr.Items[0].ID

		// Dismiss
		st, _ = env.doJSON(t, http.MethodPost, env.apiURL+"/api/v1/alerts/"+id+"/dismiss", aliceJWT, map[string]any{"version": 1, "reason": "Scheduled maintenance"})
		if st != http.StatusOK {
			t.Fatalf("dismiss failed: %d", st)
		}
		// Reopen
		st, _ = env.doJSON(t, http.MethodPost, env.apiURL+"/api/v1/alerts/"+id+"/reopen", aliceJWT, map[string]any{"version": 2, "comment": "Back in queue"})
		if st != http.StatusOK {
			t.Fatalf("reopen failed: %d", st)
		}
		// Standalone acknowledge must return 409 without a ticket
		st, _ = env.doJSON(t, http.MethodPost, env.apiURL+"/api/v1/alerts/"+id+"/acknowledge", aliceJWT, map[string]any{"version": 3})
		if st != http.StatusConflict {
			t.Fatalf("expected 409 on standalone ack, got %d", st)
		}
		// Notes
		st, _ = env.doJSON(t, http.MethodPost, env.apiURL+"/api/v1/alerts/"+id+"/notes", aliceJWT, map[string]any{"comment": "Checking logs"})
		if st != http.StatusCreated {
			t.Fatalf("add note failed: %d", st)
		}
		// Link existing closed ticket (flow1IncidentNum -> snow_state=6) directly onto firing alert -> atomically acknowledges + active_closed_ticket drift
		st, linkRaw := env.doJSON(t, http.MethodPost, env.apiURL+"/api/v1/alerts/"+id+"/servicenow/link", aliceJWT, map[string]any{
			"incident_number": flow1IncidentNum,
			"post_work_note":  true,
		})
		if st != http.StatusOK {
			t.Fatalf("link incident failed: %d (%s)", st, string(linkRaw))
		}
		var linkResp model.IncidentSyncResponse
		_ = json.Unmarshal(linkRaw, &linkResp)
		if linkResp.IncidentDriftStatus != model.DriftActiveClosedTicket {
			t.Fatalf("expected active_closed_ticket drift after linking closed ticket to firing alert, got %s", linkResp.IncidentDriftStatus)
		}
		// Unacknowledging while a ticket is linked is blocked with 409
		st, _ = env.doJSON(t, http.MethodPost, env.apiURL+"/api/v1/alerts/"+id+"/unacknowledge", aliceJWT, map[string]any{"version": 4, "comment": "Shift handoff"})
		if st != http.StatusConflict {
			t.Fatalf("expected 409 when unacknowledging alert with linked ticket, got %d", st)
		}
		st, activeClosedRaw := env.doJSON(t, http.MethodGet, env.apiURL+"/api/v1/alerts?incident_drift=active_closed_ticket", viewerJWT, nil)
		if st != http.StatusOK {
			t.Fatalf("active_closed_ticket filter failed: %d", st)
		}
		var activeClosedList model.ListAlertsResponse
		_ = json.Unmarshal(activeClosedRaw, &activeClosedList)
		if len(activeClosedList.Items) != 1 || activeClosedList.Items[0].ID != id {
			t.Fatalf("expected alert %s under active_closed_ticket filter, got %+v", id, activeClosedList.Items)
		}

		// Superseded open episode auto-resolved when newer firing episode arrives without prior resolved webhook
		tNew := t0.Add(2 * time.Minute).UTC()
		st, supersedeRaw := env.doJSON(t, http.MethodPost, env.apiURL+"/api/v1/webhooks/alertmanager", env.amToken, model.AlertmanagerWebhookPayload{
			Version:  "4",
			GroupKey: "gk-extra",
			Receiver: "alert-api",
			Alerts: []model.AlertmanagerAlert{
				{
					Status:      "firing",
					Fingerprint: fp,
					StartsAt:    tNew,
					EndsAt:      time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC),
					Labels:      map[string]string{"alertname": "ExtraAlert", "severity": "warning"},
				},
			},
		})
		if st != http.StatusOK {
			t.Fatalf("supersede firing webhook failed: %d (%s)", st, string(supersedeRaw))
		}
		var supResp model.AlertmanagerWebhookResponse
		_ = json.Unmarshal(supersedeRaw, &supResp)
		if supResp.Inserted != 1 {
			t.Fatalf("expected 1 inserted episode on supersede, got %+v", supResp)
		}
		st, oldDetailRaw := env.doJSON(t, http.MethodGet, env.apiURL+"/api/v1/alerts/"+id, viewerJWT, nil)
		if st != http.StatusOK {
			t.Fatalf("get superseded alert failed: %d", st)
		}
		var oldDetail model.AlertDetail
		_ = json.Unmarshal(oldDetailRaw, &oldDetail)
		if oldDetail.State != model.AlertStateResolved {
			t.Fatalf("expected superseded episode %s to be auto-resolved, got %s", id, oldDetail.State)
		}

		// Stale out-of-order firing webhook for already-resolved (fp, t0) must be ignored
		st, staleRaw := env.doJSON(t, http.MethodPost, env.apiURL+"/api/v1/webhooks/alertmanager", env.amToken, model.AlertmanagerWebhookPayload{
			Version:  "4",
			GroupKey: "gk-extra",
			Receiver: "alert-api",
			Alerts: []model.AlertmanagerAlert{
				{
					Status:      "firing",
					Fingerprint: fp,
					StartsAt:    t0,
					EndsAt:      time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC),
					Labels:      map[string]string{"alertname": "ExtraAlert", "severity": "warning"},
				},
			},
		})
		if st != http.StatusOK {
			t.Fatalf("stale firing webhook failed: %d (%s)", st, string(staleRaw))
		}
		var staleResp model.AlertmanagerWebhookResponse
		_ = json.Unmarshal(staleRaw, &staleResp)
		if staleResp.Ignored != 1 {
			t.Fatalf("expected stale firing for resolved episode to be ignored, got %+v", staleResp)
		}

		// Use the newly active episode ID for manual work note & sync assertions
		_, listNewRaw := env.doJSON(t, http.MethodGet, env.apiURL+"/api/v1/alerts?fingerprint="+fp+"&state=firing", aliceJWT, nil)
		var lrNew model.ListAlertsResponse
		_ = json.Unmarshal(listNewRaw, &lrNew)
		if len(lrNew.Items) != 1 || lrNew.Items[0].ID == id || lrNew.Items[0].PreviousEpisodeCount < 1 {
			t.Fatalf("expected 1 new firing episode after supersede, got %+v", lrNew.Items)
		}

		// Post manual work note on the linked incident (id still links to flow1IncidentNum)
		st, _ = env.doJSON(t, http.MethodPost, env.apiURL+"/api/v1/alerts/"+id+"/servicenow/work-notes", aliceJWT, map[string]any{
			"work_notes": "Manual follow-up from Grafana",
		})
		if st != http.StatusOK {
			t.Fatalf("post work note failed: %d", st)
		}
		// Manual sync
		st, _ = env.doJSON(t, http.MethodPost, env.apiURL+"/api/v1/alerts/"+id+"/servicenow/sync", aliceJWT, nil)
		if st != http.StatusOK {
			t.Fatalf("manual sync failed: %d", st)
		}
		// Metadata
		st, metaRaw := env.doJSON(t, http.MethodGet, env.apiURL+"/api/v1/servicenow/metadata", viewerJWT, nil)
		if st != http.StatusOK || !strings.Contains(string(metaRaw), "Database SRE") {
			t.Fatalf("metadata failed: status=%d body=%s", st, string(metaRaw))
		}
		// Healthz & Metrics
		st, _ = env.doJSON(t, http.MethodGet, env.adminURL+"/healthz", "", nil)
		if st != http.StatusOK {
			t.Fatalf("healthz failed: %d", st)
		}
		st, metricsRaw := env.doJSON(t, http.MethodGet, env.adminURL+"/metrics", "", nil)
		if st != http.StatusOK ||
			!strings.Contains(string(metricsRaw), "noc_alertmanager_truncated_alerts_total 1") ||
			!strings.Contains(string(metricsRaw), "noc_db_pool_acquired_conns") {
			t.Fatalf("unexpected /metrics output: status=%d body=%s", st, string(metricsRaw))
		}
	})

	t.Run("Flow8_TeamTagsBulkIncidentsIdempotencyAndReopenSemantics", func(t *testing.T) {
		doJSONWithHeaders := func(method, urlStr, bearer string, headers map[string]string, body any) (int, http.Header, []byte) {
			var reader io.Reader
			if body != nil {
				raw, err := json.Marshal(body)
				if err != nil {
					t.Fatalf("marshal request body: %v", err)
				}
				reader = bytes.NewReader(raw)
			}
			req, err := http.NewRequest(method, urlStr, reader)
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			if bearer != "" {
				req.Header.Set("Authorization", "Bearer "+bearer)
			}
			if body != nil {
				req.Header.Set("Content-Type", "application/json")
			}
			for k, v := range headers {
				req.Header.Set(k, v)
			}
			resp, err := env.httpClient.Do(req)
			if err != nil {
				t.Fatalf("http %s %s failed: %v", method, urlStr, err)
			}
			defer resp.Body.Close()
			respBytes, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read response: %v", err)
			}
			return resp.StatusCode, resp.Header, respBytes
		}

		const labeledFP = "fp_labeled_team_e2e_08"
		const untaggedFP = "fp_untagged_team_e2e_08"
		const dismissFP = "fp_dismiss_reopen_e2e_08"
		tStart := time.Now().UTC().Add(-4 * time.Minute).Truncate(time.Second)

		st, body := env.doJSON(t, http.MethodPost, env.apiURL+"/api/v1/webhooks/alertmanager", env.amToken, model.AlertmanagerWebhookPayload{
			Version:  "4",
			GroupKey: "gk-flow8",
			Receiver: "alert-api",
			Alerts: []model.AlertmanagerAlert{
				{
					Status:      "firing",
					Fingerprint: labeledFP,
					StartsAt:    tStart,
					EndsAt:      time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC),
					Labels:      map[string]string{"alertname": "LabeledServiceAlert", "severity": "critical", "team": "core-db"},
				},
				{
					Status:      "firing",
					Fingerprint: untaggedFP,
					StartsAt:    tStart,
					EndsAt:      time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC),
					Labels:      map[string]string{"alertname": "UntaggedServiceAlert", "severity": "critical"},
				},
				{
					Status:      "firing",
					Fingerprint: dismissFP,
					StartsAt:    tStart,
					EndsAt:      time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC),
					Labels:      map[string]string{"alertname": "DismissThenResolveAlert", "severity": "warning"},
				},
			},
		})
		if st != http.StatusOK {
			t.Fatalf("ingest flow8 alerts failed: %d (%s)", st, string(body))
		}

		// Low/Info severities are excluded from queue listings and summary counts
		st, _ = env.doJSON(t, http.MethodPost, env.apiURL+"/api/v1/webhooks/alertmanager", env.amToken, model.AlertmanagerWebhookPayload{
			Version:  "4",
			GroupKey: "gk-flow8-low",
			Receiver: "alert-api",
			Alerts: []model.AlertmanagerAlert{
				{
					Status:      "firing",
					Fingerprint: "fp_ignored_info_e2e_08",
					StartsAt:    tStart,
					EndsAt:      time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC),
					Labels:      map[string]string{"alertname": "LowNoiseAlert", "severity": "info"},
				},
			},
		})
		if st != http.StatusOK {
			t.Fatalf("ingest low/info alert failed: %d", st)
		}
		_, infoListRaw := env.doJSON(t, http.MethodGet, env.apiURL+"/api/v1/alerts?fingerprint=fp_ignored_info_e2e_08", aliceJWT, nil)
		var infoList model.ListAlertsResponse
		_ = json.Unmarshal(infoListRaw, &infoList)
		if len(infoList.Items) != 0 {
			t.Fatalf("expected info/low severity alerts to be hidden from list, got %+v", infoList.Items)
		}

		// REV-08: severity=critical returns the two critical alerts
		st, critRaw := env.doJSON(t, http.MethodGet, env.apiURL+"/api/v1/alerts?severity=critical", aliceJWT, nil)
		if st != http.StatusOK {
			t.Fatalf("severity filter failed: %d", st)
		}
		var critList model.ListAlertsResponse
		_ = json.Unmarshal(critRaw, &critList)
		var labeledID, untaggedID string
		for _, item := range critList.Items {
			if item.Fingerprint == labeledFP {
				labeledID = item.ID
			} else if item.Fingerprint == untaggedFP {
				untaggedID = item.ID
			}
		}
		if labeledID == "" || untaggedID == "" {
			t.Fatalf("expected both flow8 critical alerts in severity=critical list, got %+v", critList.Items)
		}

		// REV-09: tagging an alert with Prometheus labels["team"] returns 409 TEAM_MANAGED_BY_LABEL
		st, tagErrRaw := env.doJSON(t, http.MethodPost, env.apiURL+"/api/v1/alerts/"+labeledID+"/team", aliceJWT, map[string]any{"team": "override"})
		if st != http.StatusConflict || !strings.Contains(string(tagErrRaw), "TEAM_MANAGED_BY_LABEL") {
			t.Fatalf("expected 409 TEAM_MANAGED_BY_LABEL for labeled alert, got %d (%s)", st, string(tagErrRaw))
		}

		// Tag untagged alert -> sets assigned_team and persists fingerprint inheritance
		st, taggedRaw := env.doJSON(t, http.MethodPost, env.apiURL+"/api/v1/alerts/"+untaggedID+"/team", aliceJWT, map[string]any{"team": "observability"})
		if st != http.StatusOK {
			t.Fatalf("tag untagged alert failed: %d (%s)", st, string(taggedRaw))
		}

		// Resolve untagged episode and re-fire -> future episode inherits assigned_team
		st, _ = env.doJSON(t, http.MethodPost, env.apiURL+"/api/v1/webhooks/alertmanager", env.amToken, model.AlertmanagerWebhookPayload{
			Version:  "4",
			GroupKey: "gk-flow8",
			Receiver: "alert-api",
			Alerts: []model.AlertmanagerAlert{
				{
					Status:      "resolved",
					Fingerprint: untaggedFP,
					StartsAt:    tStart,
					EndsAt:      tStart.Add(1 * time.Minute),
					Labels:      map[string]string{"alertname": "UntaggedServiceAlert", "severity": "critical"},
				},
			},
		})
		if st != http.StatusOK {
			t.Fatalf("resolve untagged episode failed: %d", st)
		}
		tRefire := tStart.Add(2 * time.Minute)
		st, _ = env.doJSON(t, http.MethodPost, env.apiURL+"/api/v1/webhooks/alertmanager", env.amToken, model.AlertmanagerWebhookPayload{
			Version:  "4",
			GroupKey: "gk-flow8",
			Receiver: "alert-api",
			Alerts: []model.AlertmanagerAlert{
				{
					Status:      "firing",
					Fingerprint: untaggedFP,
					StartsAt:    tRefire,
					EndsAt:      time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC),
					Labels:      map[string]string{"alertname": "UntaggedServiceAlert", "severity": "critical"},
				},
			},
		})
		if st != http.StatusOK {
			t.Fatalf("re-fire untagged episode failed: %d", st)
		}
		_, refireListRaw := env.doJSON(t, http.MethodGet, env.apiURL+"/api/v1/alerts?fingerprint="+untaggedFP+"&state=firing", aliceJWT, nil)
		var refireList model.ListAlertsResponse
		_ = json.Unmarshal(refireListRaw, &refireList)
		if len(refireList.Items) != 1 || refireList.Items[0].Team != "observability" || refireList.Items[0].TeamSource != "operator" {
			t.Fatalf("expected re-fired episode to inherit operator team 'observability', got %+v", refireList.Items)
		}
		inheritedID := refireList.Items[0].ID

		// REV-05: link(ack) -> dismiss -> reopen clears ack; link(ack) -> dismiss -> resolve is NOT non_active_acknowledged
		_, dismissListRaw := env.doJSON(t, http.MethodGet, env.apiURL+"/api/v1/alerts?fingerprint="+dismissFP, aliceJWT, nil)
		var dl model.ListAlertsResponse
		_ = json.Unmarshal(dismissListRaw, &dl)
		dismissID := dl.Items[0].ID

		st, _ = env.doJSON(t, http.MethodPost, env.apiURL+"/api/v1/alerts/"+dismissID+"/servicenow/link", aliceJWT, map[string]any{
			"incident_number": flow1IncidentNum,
			"post_work_note":  false,
		})
		if st != http.StatusOK {
			t.Fatalf("link dismissID failed: %d", st)
		}
		st, _ = env.doJSON(t, http.MethodPost, env.apiURL+"/api/v1/alerts/"+dismissID+"/dismiss", aliceJWT, map[string]any{"version": 2, "reason": "testing reopen"})
		if st != http.StatusOK {
			t.Fatalf("dismiss dismissID failed: %d", st)
		}
		st, reopenedRaw := env.doJSON(t, http.MethodPost, env.apiURL+"/api/v1/alerts/"+dismissID+"/reopen", aliceJWT, map[string]any{"version": 3})
		if st != http.StatusOK {
			t.Fatalf("reopen dismissID failed: %d", st)
		}
		var reopened model.Alert
		_ = json.Unmarshal(reopenedRaw, &reopened)
		if reopened.AcknowledgedBy != nil || reopened.AcknowledgedAt != nil {
			t.Fatalf("expected reopen to clear acknowledged_by/acknowledged_at, got %+v", reopened)
		}

		// Create a second firing alert to verify ack (via link) -> dismiss -> resolve stays excluded from non_active_acknowledged
		const dismissResolveFP = "fp_dismiss_resolve_e2e_08"
		st, _ = env.doJSON(t, http.MethodPost, env.apiURL+"/api/v1/webhooks/alertmanager", env.amToken, model.AlertmanagerWebhookPayload{
			Version:  "4",
			GroupKey: "gk-flow8",
			Receiver: "alert-api",
			Alerts: []model.AlertmanagerAlert{
				{
					Status:      "firing",
					Fingerprint: dismissResolveFP,
					StartsAt:    tStart,
					EndsAt:      time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC),
					Labels:      map[string]string{"alertname": "DismissThenResolveAlert2", "severity": "warning"},
				},
			},
		})
		if st != http.StatusOK {
			t.Fatalf("ingest dismissResolveFP failed: %d", st)
		}
		_, drListRaw := env.doJSON(t, http.MethodGet, env.apiURL+"/api/v1/alerts?fingerprint="+dismissResolveFP, aliceJWT, nil)
		var drList model.ListAlertsResponse
		_ = json.Unmarshal(drListRaw, &drList)
		drID := drList.Items[0].ID

		st, _ = env.doJSON(t, http.MethodPost, env.apiURL+"/api/v1/alerts/"+drID+"/servicenow/link", aliceJWT, map[string]any{
			"incident_number": flow1IncidentNum,
			"post_work_note":  false,
		})
		if st != http.StatusOK {
			t.Fatalf("link drID failed: %d", st)
		}
		st, _ = env.doJSON(t, http.MethodPost, env.apiURL+"/api/v1/alerts/"+drID+"/dismiss", aliceJWT, map[string]any{"version": 2, "reason": "noise"})
		if st != http.StatusOK {
			t.Fatalf("dismiss drID failed: %d", st)
		}
		st, _ = env.doJSON(t, http.MethodPost, env.apiURL+"/api/v1/webhooks/alertmanager", env.amToken, model.AlertmanagerWebhookPayload{
			Version:  "4",
			GroupKey: "gk-flow8",
			Receiver: "alert-api",
			Alerts: []model.AlertmanagerAlert{
				{
					Status:      "resolved",
					Fingerprint: dismissResolveFP,
					StartsAt:    tStart,
					EndsAt:      tStart.Add(3 * time.Minute),
					Labels:      map[string]string{"alertname": "DismissThenResolveAlert2", "severity": "warning"},
				},
			},
		})
		if st != http.StatusOK {
			t.Fatalf("resolve dismissed alert failed: %d", st)
		}
		_, naaRaw := env.doJSON(t, http.MethodGet, env.apiURL+"/api/v1/alerts?state=non_active_acknowledged&fingerprint="+dismissResolveFP, aliceJWT, nil)
		var naaList model.ListAlertsResponse
		_ = json.Unmarshal(naaRaw, &naaList)
		if len(naaList.Items) != 0 {
			t.Fatalf("dismissed-then-resolved alert must NOT appear in non_active_acknowledged, got %+v", naaList.Items)
		}

		// REV-03 + REV-02 + REV-11: Bulk create with Idempotency-Key replay, sibling sync audit, and sibling-aware resolution notes
		bulkPayload := map[string]any{
			"alert_ids":         []string{labeledID, inheritedID},
			"short_description": "[CRITICAL] Bulk flow8 incident",
			"impact":            1,
			"urgency":           1,
			"caller_mode":       "sso_user",
		}
		st, _, bulkRaw1 := doJSONWithHeaders(http.MethodPost, env.apiURL+"/api/v1/servicenow/incidents/bulk", aliceJWT, map[string]string{
			"Idempotency-Key": "e2e-bulk-idem-001",
		}, bulkPayload)
		if st != http.StatusCreated {
			t.Fatalf("bulk create failed: %d (%s)", st, string(bulkRaw1))
		}
		var bulkResp1 model.CreateBulkIncidentResponse
		_ = json.Unmarshal(bulkRaw1, &bulkResp1)
		bulkSysID := *bulkResp1.ServiceNowIncident.SnowSysID

		// Replay with identical Idempotency-Key must return 201 + Idempotency-Replayed: true
		st, hdrs2, bulkRaw2 := doJSONWithHeaders(http.MethodPost, env.apiURL+"/api/v1/servicenow/incidents/bulk", aliceJWT, map[string]string{
			"Idempotency-Key": "e2e-bulk-idem-001",
		}, bulkPayload)
		if st != http.StatusCreated || hdrs2.Get("Idempotency-Replayed") != "true" || !bytes.Contains(bulkRaw2, []byte(*bulkResp1.ServiceNowIncident.IncidentNumber)) {
			t.Fatalf("expected 201 replay with Idempotency-Replayed: true, got st=%d hdr=%q body=%s", st, hdrs2.Get("Idempotency-Replayed"), string(bulkRaw2))
		}

		// Transition in mocksnow silently -> manual /sync on labeledID updates sibling inheritedID and logs audit on both
		st, _ = env.doJSON(t, http.MethodPost, env.mockSnowURL+"/_mock/incidents/"+bulkSysID+"/transition", "", map[string]any{
			"state":        2,
			"send_webhook": false,
		})
		if st != http.StatusOK {
			t.Fatalf("mocksnow transition bulkSysID failed: %d", st)
		}
		st, _ = env.doJSON(t, http.MethodPost, env.apiURL+"/api/v1/alerts/"+labeledID+"/servicenow/sync", aliceJWT, nil)
		if st != http.StatusOK {
			t.Fatalf("manual sync on bulk alert failed: %d", st)
		}
		_, sibHistRaw := env.doJSON(t, http.MethodGet, env.apiURL+"/api/v1/alerts/"+inheritedID+"/history", aliceJWT, nil)
		var sibHist model.AlertHistoryResponse
		_ = json.Unmarshal(sibHistRaw, &sibHist)
		foundSibSync := false
		for _, ev := range sibHist.AuditEvents {
			if ev.EventType == model.AuditSnowStateSynced {
				foundSibSync = true
				break
			}
		}
		if !foundSibSync {
			t.Fatalf("expected snow_state_synced audit event on sibling alert %s", inheritedID)
		}

		// Resolve first bulk sibling -> partial recovery note posted (NOT resolve/close)
		st, _ = env.doJSON(t, http.MethodPost, env.apiURL+"/api/v1/webhooks/alertmanager", env.amToken, model.AlertmanagerWebhookPayload{
			Version:  "4",
			GroupKey: "gk-flow8",
			Receiver: "alert-api",
			Alerts: []model.AlertmanagerAlert{
				{
					Status:      "resolved",
					Fingerprint: labeledFP,
					StartsAt:    tStart,
					EndsAt:      tStart.Add(4 * time.Minute),
					Labels:      map[string]string{"alertname": "LabeledServiceAlert", "severity": "critical", "team": "core-db"},
				},
			},
		})
		if st != http.StatusOK {
			t.Fatalf("resolve first bulk sibling failed: %d", st)
		}

		deadlinePartial := time.Now().Add(10 * time.Second)
		gotPartial := false
		for time.Now().Before(deadlinePartial) {
			st, mockRaw := env.doJSON(t, http.MethodGet, env.mockSnowURL+"/_mock/incidents", "", nil)
			if st == http.StatusOK {
				var list struct {
					Incidents []mocksnow.MockIncident `json:"incidents"`
				}
				_ = json.Unmarshal(mockRaw, &list)
				for _, inc := range list.Incidents {
					if inc.SysID == bulkSysID {
						for _, note := range inc.WorkNotes {
							if strings.Contains(note, "remain active") {
								gotPartial = true
								break
							}
						}
					}
				}
			}
			if gotPartial {
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
		if !gotPartial {
			t.Fatalf("timed out waiting for partial sibling resolution note on bulk ticket")
		}
	})

	t.Run("Flow9_InstanceFilterSortAndConfluenceDocs", func(t *testing.T) {
		tStart := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Second)

		st, body := env.doJSON(t, http.MethodPost, env.apiURL+"/api/v1/webhooks/alertmanager", env.amToken, model.AlertmanagerWebhookPayload{
			Version:  "4",
			GroupKey: "gk-flow9",
			Receiver: "alert-api",
			Alerts: []model.AlertmanagerAlert{
				{
					Status:      "firing",
					Fingerprint: "fp_flow9_zeta",
					StartsAt:    tStart,
					EndsAt:      time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC),
					Labels: map[string]string{
						"alertname": "ZetaDiskAlert",
						"severity":  "critical",
						"instance":  "zeta-01.prod:9100",
					},
					Annotations: map[string]string{
						"runbook_url": "https://runbooks.example.com/zeta",
					},
				},
				{
					Status:      "firing",
					Fingerprint: "fp_flow9_alpha",
					StartsAt:    tStart,
					EndsAt:      time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC),
					Labels: map[string]string{
						"alertname": "AlphaCpuAlert",
						"severity":  "warning",
						"host":      "alpha-host.prod",
					},
				},
				{
					Status:      "firing",
					Fingerprint: "fp_flow9_empty",
					StartsAt:    tStart,
					EndsAt:      time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC),
					Labels: map[string]string{
						"alertname": "EmptyInstAlert",
						"severity":  "warning",
					},
				},
			},
		})
		if st != http.StatusOK {
			t.Fatalf("ingest flow9 alerts failed: %d (%s)", st, string(body))
		}

		// Filter by extracted host->instance fallback and search by instance
		st, alphaRaw := env.doJSON(t, http.MethodGet, env.apiURL+"/api/v1/alerts?instance=alpha-host.prod", aliceJWT, nil)
		if st != http.StatusOK {
			t.Fatalf("instance filter request failed: %d (%s)", st, string(alphaRaw))
		}
		var alphaList model.ListAlertsResponse
		if err := json.Unmarshal(alphaRaw, &alphaList); err != nil {
			t.Fatalf("unmarshal alpha instance list: %v", err)
		}
		if len(alphaList.Items) != 1 || alphaList.Items[0].AlertName != "AlphaCpuAlert" || alphaList.Items[0].Instance != "alpha-host.prod" {
			t.Fatalf("expected AlphaCpuAlert with instance=alpha-host.prod, got %+v", alphaList.Items)
		}
		alphaID := alphaList.Items[0].ID

		st, searchInstRaw := env.doJSON(t, http.MethodGet, env.apiURL+"/api/v1/alerts?search=zeta-01.prod", aliceJWT, nil)
		if st != http.StatusOK {
			t.Fatalf("instance search failed: %d (%s)", st, string(searchInstRaw))
		}
		var searchInstList model.ListAlertsResponse
		_ = json.Unmarshal(searchInstRaw, &searchInstList)
		if len(searchInstList.Items) != 1 || searchInstList.Items[0].Instance != "zeta-01.prod:9100" {
			t.Fatalf("expected instance search match zeta-01.prod:9100, got %+v", searchInstList.Items)
		}

		// Instance sorting ASC & DESC with NULLS LAST (empty instance stays last in both orders) and keyset pagination
		st, ascP1Raw := env.doJSON(t, http.MethodGet, env.apiURL+"/api/v1/alerts?state=firing&sort=instance_asc&limit=2", aliceJWT, nil)
		if st != http.StatusOK {
			t.Fatalf("instance_asc p1 failed: %d (%s)", st, string(ascP1Raw))
		}
		var ascP1 model.ListAlertsResponse
		_ = json.Unmarshal(ascP1Raw, &ascP1)
		if len(ascP1.Items) != 2 || ascP1.NextCursor == nil {
			t.Fatalf("expected 2 items and non-nil next_cursor on instance_asc p1, got %+v", ascP1)
		}
		if ascP1.Items[0].Instance != "alpha-host.prod" {
			t.Fatalf("expected alpha-host.prod first in instance_asc, got %+v", ascP1.Items)
		}

		st, ascP2Raw := env.doJSON(t, http.MethodGet, env.apiURL+"/api/v1/alerts?state=firing&sort=instance_asc&limit=20&cursor="+*ascP1.NextCursor, aliceJWT, nil)
		if st != http.StatusOK {
			t.Fatalf("instance_asc p2 failed: %d (%s)", st, string(ascP2Raw))
		}
		var ascP2 model.ListAlertsResponse
		_ = json.Unmarshal(ascP2Raw, &ascP2)
		if len(ascP2.Items) == 0 || ascP2.Items[len(ascP2.Items)-1].Instance != "" {
			t.Fatalf("expected empty instance NULLS LAST on final page in instance_asc, got %+v", ascP2.Items)
		}

		st, descRaw := env.doJSON(t, http.MethodGet, env.apiURL+"/api/v1/alerts?state=firing&sort=instance_desc&limit=20", aliceJWT, nil)
		if st != http.StatusOK {
			t.Fatalf("instance_desc failed: %d (%s)", st, string(descRaw))
		}
		var descList model.ListAlertsResponse
		_ = json.Unmarshal(descRaw, &descList)
		if len(descList.Items) < 3 || descList.Items[0].Instance != "zeta-01.prod:9100" || descList.Items[len(descList.Items)-1].Instance != "" {
			t.Fatalf("expected zeta-01.prod:9100 first and empty instance last in instance_desc, got %+v", descList.Items)
		}

		// Confluence doc URL validation (400 on http://), Viewer RBAC (403), and Editor persistence + audit (200)
		st, _ = env.doJSON(t, http.MethodPost, env.apiURL+"/api/v1/alerts/"+alphaID+"/confluence", aliceJWT, map[string]any{
			"confluence_url": "http://insecure.example.com/display/NOC/Alpha",
		})
		if st != http.StatusBadRequest {
			t.Fatalf("expected 400 INVALID_REQUEST on non-HTTPS confluence_url, got %d", st)
		}

		st, _ = env.doJSON(t, http.MethodPost, env.apiURL+"/api/v1/alerts/"+alphaID+"/confluence", viewerJWT, map[string]any{
			"confluence_url": "https://confluence.example.com/display/NOC/AlphaCpuAlert",
		})
		if st != http.StatusForbidden {
			t.Fatalf("expected 403 FORBIDDEN_RBAC for Viewer confluence update, got %d", st)
		}

		const targetDocURL = "https://confluence.example.com/display/NOC/AlphaCpuAlert"
		st, confRaw := env.doJSON(t, http.MethodPost, env.apiURL+"/api/v1/alerts/"+alphaID+"/confluence", aliceJWT, map[string]any{
			"confluence_url": targetDocURL,
		})
		if st != http.StatusOK {
			t.Fatalf("expected 200 updating confluence_url, got %d (%s)", st, string(confRaw))
		}
		var updatedAlpha model.Alert
		_ = json.Unmarshal(confRaw, &updatedAlpha)
		if updatedAlpha.ConfluenceURL == nil || *updatedAlpha.ConfluenceURL != targetDocURL || updatedAlpha.ConfluenceURLSource != model.ConfluenceSourceOverride {
			t.Fatalf("unexpected updated confluence fields: %+v", updatedAlpha)
		}

		// Verify detail endpoint and immutable audit log event
		st, detailRaw := env.doJSON(t, http.MethodGet, env.apiURL+"/api/v1/alerts/"+alphaID, viewerJWT, nil)
		if st != http.StatusOK {
			t.Fatalf("get alert detail failed: %d (%s)", st, string(detailRaw))
		}
		var detailAlpha model.AlertDetail
		_ = json.Unmarshal(detailRaw, &detailAlpha)
		if detailAlpha.ConfluenceURL == nil || *detailAlpha.ConfluenceURL != targetDocURL {
			t.Fatalf("expected confluence_url on alert detail, got %+v", detailAlpha)
		}

		st, histRaw := env.doJSON(t, http.MethodGet, env.apiURL+"/api/v1/alerts/"+alphaID+"/history", viewerJWT, nil)
		if st != http.StatusOK {
			t.Fatalf("get alert history failed: %d (%s)", st, string(histRaw))
		}
		var hist model.AlertHistoryResponse
		_ = json.Unmarshal(histRaw, &hist)
		foundConfAudit := false
		for _, ev := range hist.AuditEvents {
			if ev.EventType == model.AuditAlertConfluenceURLUpdated {
				foundConfAudit = true
				break
			}
		}
		if !foundConfAudit {
			t.Fatalf("expected %s audit event, got %+v", model.AuditAlertConfluenceURLUpdated, hist.AuditEvents)
		}

		// Resolve & re-fire a new episode with the same alertname -> inherits alert_confluence_docs mapping
		st, _ = env.doJSON(t, http.MethodPost, env.apiURL+"/api/v1/webhooks/alertmanager", env.amToken, model.AlertmanagerWebhookPayload{
			Version:  "4",
			GroupKey: "gk-flow9",
			Receiver: "alert-api",
			Alerts: []model.AlertmanagerAlert{
				{
					Status:      "resolved",
					Fingerprint: "fp_flow9_alpha",
					StartsAt:    tStart,
					EndsAt:      tStart.Add(30 * time.Second),
					Labels: map[string]string{
						"alertname": "AlphaCpuAlert",
						"severity":  "warning",
						"host":      "alpha-host.prod",
					},
				},
			},
		})
		if st != http.StatusOK {
			t.Fatalf("resolve flow9 alpha failed: %d", st)
		}
		st, _ = env.doJSON(t, http.MethodPost, env.apiURL+"/api/v1/webhooks/alertmanager", env.amToken, model.AlertmanagerWebhookPayload{
			Version:  "4",
			GroupKey: "gk-flow9-refire",
			Receiver: "alert-api",
			Alerts: []model.AlertmanagerAlert{
				{
					Status:      "firing",
					Fingerprint: "fp_flow9_alpha_refire",
					StartsAt:    tStart.Add(1 * time.Minute),
					EndsAt:      time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC),
					Labels: map[string]string{
						"alertname": "AlphaCpuAlert",
						"severity":  "warning",
						"instance":  "alpha-host-2.prod",
					},
				},
			},
		})
		if st != http.StatusOK {
			t.Fatalf("re-fire flow9 alpha failed: %d", st)
		}
		_, refireRaw := env.doJSON(t, http.MethodGet, env.apiURL+"/api/v1/alerts?fingerprint=fp_flow9_alpha_refire&state=firing", viewerJWT, nil)
		var refireResp model.ListAlertsResponse
		_ = json.Unmarshal(refireRaw, &refireResp)
		if len(refireResp.Items) != 1 || refireResp.Items[0].ConfluenceURL == nil || *refireResp.Items[0].ConfluenceURL != targetDocURL || refireResp.Items[0].ConfluenceURLSource != model.ConfluenceSourceOverride {
			t.Fatalf("expected new episode with same alert_name to inherit confluence_url, got %+v", refireResp.Items)
		}
	})
}
