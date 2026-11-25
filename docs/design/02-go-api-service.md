# 02 — Go Backend API Service Specification

## 1. Service Overview & Responsibilities

The **Go Alert-Management API** is a standalone, stateless HTTP microservice written in Go (standard `net/http` / `chi` router, `pgxpool` for PostgreSQL, structured `log/slog` logging, Prometheus metrics). It acts as the single source of truth orchestrator between:
1. **Prometheus Alertmanager** (inbound v4 webhooks).
2. **Grafana App Plugin** (`noc-alert-manager-app`, via thin `CallResource` proxy or optional proxy routes).
3. **PostgreSQL** (persistent alert state machine, ServiceNow linkage, audit trail, SSO user cache).
4. **ServiceNow** (outbound Table API incident creation/sync + optional inbound Business Rule webhook + background drift reconciler).

Running this service independently from Grafana ensures that **Alertmanager webhook ingestion, state reconciliation, and ServiceNow drift polling continue uninterrupted** even during Grafana upgrades or restarts.

---

## 2. Authentication & Grafana RBAC Passthrough

The Go API exposes three distinct security zones, each with its own middleware stack:

| Endpoint Prefix | Caller | Authentication Mechanism | Authorization / RBAC |
|---|---|---|---|
| `/api/v1/alerts/*`, `/api/v1/servicenow/*` | Grafana App Plugin (`gpx_noc_alert_manager`) | **Primary:** Short-lived internal JWT (`Authorization: Bearer <jwt>`, HS256 signed with `GRAFANA_PLUGIN_JWT_SECRET`, `exp <= 60s`).<br>**Fallback (Zero-Binary Proxy Mode):** Shared token `X-Proxy-Token` matching `GRAFANA_PROXY_SHARED_TOKEN` + `X-Grafana-User` (and optional `X-Grafana-Id` JWT). | JWT claims carry `login`, `email`, `name`, `org_id`, `role`, and `permissions[]` extracted by `CallResource` from `backend.PluginContext.User`. Go API middleware enforces action-level checks before hitting handlers. |
| `/api/v1/webhooks/alertmanager` | Prometheus Alertmanager | Static Bearer Token (`Authorization: Bearer <token>`) verified with constant-time comparison (`subtle.ConstantTimeCompare`) against `ALERTMANAGER_WEBHOOK_TOKEN`. | Scoped strictly to webhook ingestion (`actor_type = 'alertmanager'`). |
| `/api/v1/webhooks/servicenow` | ServiceNow Business Rule | HMAC-SHA256 signature header `X-SNOW-Signature-256` (or Bearer token) verified against `SNOW_INBOUND_WEBHOOK_SECRET`. | Scoped strictly to updating existing `servicenow_incidents` records (`actor_type = 'servicenow'`). |
| `/healthz`, `/readyz`, `/metrics` | Cluster probes / Prometheus scraper | Bound to internal admin port or network-policy restricted (no public ingress). | Read-only operational telemetry. |

### 2.1 Internal JWT Claims Contract (Primary `CallResource` Mode)

Signed by the thin Grafana App Plugin backend using `secureJsonData.apiJwtSecret` (matching `GRAFANA_PLUGIN_JWT_SECRET` in the Go API):

```json
{
  "iss": "noc-alert-manager-app",
  "aud": "noc-alert-api",
  "sub": "alice.smith",
  "email": "alice.smith@corp.example",
  "name": "Alice Smith",
  "org_id": 1,
  "role": "Editor",
  "permissions": [
    "noc-alert-manager-app.alerts:read",
    "noc-alert-manager-app.alerts:triage",
    "noc-alert-manager-app.incidents:write"
  ],
  "iat": 1795628000,
  "exp": 1795628060,
  "jti": "9b1deb4d-3b7d-4bad-9bdd-2b0d7b3dcb6d"
}
```

**Verification Rules in Go API Auth Middleware:**
1. Verify HS256 signature against `GRAFANA_PLUGIN_JWT_SECRET` (supporting dual-key rotation via `GRAFANA_PLUGIN_JWT_SECRET_PREVIOUS`).
2. Reject tokens if `iss != GRAFANA_PLUGIN_JWT_ISSUER`, `aud != "noc-alert-api"`, `exp` is expired (max 5s clock skew), or `exp - iat > 120s`.
3. Extract `OperatorContext{Login, Email, Name, Role, Permissions, OrgID}` into `request.Context()`.
4. Enforce route-specific permission middleware:
   - Read routes → require `noc-alert-manager-app.alerts:read` (or `role IN ('Viewer', 'Editor', 'Admin')`).
   - Triage mutations → require `noc-alert-manager-app.alerts:triage` (or `role IN ('Editor', 'Admin')`).
   - ServiceNow mutations → require `noc-alert-manager-app.incidents:write` (or `role IN ('Editor', 'Admin')`).
5. **Never trust client-supplied actor fields** in JSON request bodies; `acknowledged_by`, `dismissed_by`, `created_by_login`, `created_by_email`, and `alert_audit_log.actor_*` are always derived from the verified `OperatorContext`.

---

## 3. Common Conventions & Error Contract

- **Content-Type:** `application/json; charset=utf-8` for all request and response bodies.
- **Timestamps:** RFC 3339 UTC strings (e.g., `"2026-11-25T17:30:00Z"`).
- **Identifiers:** UUIDv4 strings for `alert_id` and `incident_record_id`.
- **Correlation:** Accept/generate `X-Request-Id` on every request and echo it in response headers and structured logs.

### 3.1 Standard Error Envelope (RFC 7807-Inspired)

```json
{
  "error": {
    "code": "CONFLICT_STALE_VERSION",
    "message": "Alert state has changed since version 2; refresh and retry.",
    "request_id": "req_01JDF8K2X9M7N4P6Q8R0S2T4",
    "details": {
      "current_version": 3,
      "current_state": "acknowledged",
      "acknowledged_by": "bob.jones"
    }
  }
}
```

### 3.2 Canonical HTTP Status Code Matrix

| Status Code | Error Code(s) | Meaning |
|---|---|---|
| `200 OK` | — | Query succeeded or idempotent/state mutation completed. |
| `201 Created` | — | New ServiceNow incident created and linked. |
| `400 Bad Request` | `INVALID_REQUEST`, `VALIDATION_ERROR` | Malformed JSON, invalid query params, missing required fields. |
| `401 Unauthorized` | `UNAUTHORIZED`, `TOKEN_EXPIRED` | Missing, invalid, or expired JWT / webhook token. |
| `403 Forbidden` | `FORBIDDEN_RBAC` | Authenticated user lacks required RBAC action. |
| `404 Not Found` | `ALERT_NOT_FOUND`, `INCIDENT_NOT_FOUND` | Target `alert_id` or ServiceNow `incident_number` does not exist. |
| `409 Conflict` | `CONFLICT_STALE_VERSION`, `INVALID_STATE_TRANSITION`, `INCIDENT_ALREADY_LINKED`, `INCIDENT_CREATION_IN_PROGRESS` | Optimistic lock mismatch, disallowed state transition (e.g., acking a `resolved` alert), or concurrent duplicate SNOW creation. |
| `422 Unprocessable Entity` | `SNOW_VALIDATION_REJECTED` | ServiceNow rejected business fields (e.g., invalid `assignment_group` or mandatory field policy). |
| `429 Too Many Requests` | `RATE_LIMITED` | Per-operator or ServiceNow outbound rate limit exceeded. |
| `502 Bad Gateway` | `SNOW_UPSTREAM_ERROR` | ServiceNow returned an unexpected 5xx error. |
| `503 Service Unavailable` | `DB_UNAVAILABLE`, `SNOW_UNAVAILABLE` | PostgreSQL down (triggers Alertmanager retry) or SNOW circuit breaker open. |
| `504 Gateway Timeout` | `SNOW_TIMEOUT` | ServiceNow API did not respond within `SNOW_HTTP_TIMEOUT_SECONDS`. |

---

## 4. OpenAPI-Style Endpoint Specifications

### 4.1 Alert Query & Triage Endpoints

#### `GET /api/v1/alerts`
Lists alert episodes with filtering, cursor/offset pagination, and live queue counters.
- **Required Permission:** `noc-alert-manager-app.alerts:read`
- **Query Parameters:**
  - `state` (optional, repeatable CSV): `firing`, `acknowledged`, `dismissed`, `resolved`
  - `severity` (optional, repeatable CSV): `critical`, `warning`, `info`, `unknown`
  - `incident_drift` (optional): `all` (default) | `resolved_open_ticket` | `active_closed_ticket` | `none`
  - `has_incident` (optional, boolean): `true` | `false`
  - `fingerprint` (optional, string): exact match for viewing all episodes of one alert series
  - `label` (optional, repeatable `key=value`): JSONB containment filter on `alerts.labels`
  - `search` (optional, string): case-insensitive search on `alert_name`, `annotations->>'summary'`, `fingerprint`, or linked `incident_number`
  - `from` / `to` (optional, RFC 3339): filter by `starts_at` range
  - `sort` (optional): `starts_at_desc` (default), `starts_at_asc`, `severity_desc`, `resolved_at_desc`
  - `limit` (optional, int): default `50`, max `200`
  - `cursor` (optional, opaque base64): keyset pagination cursor
- **Response `200 OK`:**
```json
{
  "summary": {
    "firing": 12,
    "acknowledged": 7,
    "acknowledged_without_incident": 2,
    "dismissed": 4,
    "resolved_open_ticket": 3
  },
  "items": [
    {
      "id": "c7a2d5e8-9f31-4b2c-8a11-7e5f2c4d9a01",
      "fingerprint": "1a2b3c4d5e6f7a8b",
      "starts_at": "2026-11-25T16:10:00Z",
      "ends_at": "2026-11-25T16:52:00Z",
      "state": "resolved",
      "version": 4,
      "alert_name": "HighDatabaseReplicationLag",
      "severity": "critical",
      "labels": {
        "alertname": "HighDatabaseReplicationLag",
        "severity": "critical",
        "cluster": "prod-eu1",
        "service": "orders-db"
      },
      "annotations": {
        "summary": "Replication lag > 30s on orders-db-replica-2",
        "description": "Lag reached 47.2s for > 5m.",
        "runbook_url": "https://runbooks.internal.example.com/db-lag"
      },
      "generator_url": "https://prometheus.internal.example.com/graph?g0.expr=...",
      "acknowledged_by": "alice.smith",
      "acknowledged_by_email": "alice.smith@corp.example",
      "acknowledged_at": "2026-11-25T16:12:15Z",
      "dismissed_by": null,
      "dismissed_at": null,
      "dismiss_reason": null,
      "resolved_at": "2026-11-25T16:52:00Z",
      "previous_episode_count": 2,
      "incident_drift_status": "resolved_open_ticket",
      "servicenow_incident": {
        "id": "e4f8a1b2-3c4d-4e5f-8a6b-9c0d1e2f3a4b",
        "sync_status": "created",
        "snow_sys_id": "51615243c0a801640139e875df691a31",
        "incident_number": "INC0012345",
        "incident_url": "https://corp.service-now.com/nav_to.do?uri=incident.do?sys_id=51615243c0a801640139e875df691a31",
        "snow_state": 2,
        "snow_state_label": "In Progress",
        "priority": 1,
        "assignment_group": "Database SRE",
        "caller_mode": "sso_user",
        "caller_resolution_status": "sso_matched",
        "snow_state_synced_at": "2026-11-25T17:32:00Z"
      }
    }
  ],
  "next_cursor": "eyJzdGFydHNfYXQiOiIyMDI2LTExLTI1VDE2OjEwOjAwWiIsImlkIjoiYzdhMmQ1ZTgtLi4uIn0="
}
```

---

#### `GET /api/v1/alerts/{alert_id}`
Returns the full alert episode object (same schema as an item above, plus `external_url`, `group_key`, `receiver`, `delivery_count`, `first_received_at`, `last_received_at`, and `recent_audit_events` — latest 20 entries).
- **Required Permission:** `noc-alert-manager-app.alerts:read`
- **Responses:** `200 OK`, `404 Not Found`

---

#### `GET /api/v1/alerts/{alert_id}/history`
Returns the full chronological audit trail (`alert_audit_log`) for `alert_id` plus a summary list of prior episodes sharing the same `fingerprint`.
- **Required Permission:** `noc-alert-manager-app.alerts:read`
- **Response `200 OK`:**
```json
{
  "alert_id": "c7a2d5e8-9f31-4b2c-8a11-7e5f2c4d9a01",
  "fingerprint": "1a2b3c4d5e6f7a8b",
  "audit_events": [
    {
      "id": 1042,
      "event_type": "alert_fired",
      "actor_type": "alertmanager",
      "actor_login": "alertmanager",
      "actor_email": null,
      "previous_state": null,
      "new_state": "firing",
      "comment": null,
      "metadata": { "group_key": "{}:{alertname=\"HighDatabaseReplicationLag\"}" },
      "created_at": "2026-11-25T16:10:02Z"
    }
  ],
  "previous_episodes": [
    {
      "id": "a1b2c3d4-0000-4000-8000-000000000001",
      "starts_at": "2026-11-22T03:15:00Z",
      "resolved_at": "2026-11-22T03:48:00Z",
      "state": "resolved",
      "incident_number": "INC0011902",
      "snow_state_label": "Closed"
    }
  ]
}
```

---

#### `POST /api/v1/alerts/{alert_id}/acknowledge`
Transitions an alert from `firing` → `acknowledged`.
- **Required Permission:** `noc-alert-manager-app.alerts:triage`
- **Request Body:**
```json
{
  "version": 1,
  "comment": "Investigating replica lag, checking WAL receiver metrics."
}
```
- **Behavior:**
  - Acquires `SELECT ... FROM alerts WHERE id = $1 FOR UPDATE`.
  - If `state == 'acknowledged'` and `acknowledged_by == operator.Login` and `version == req.Version + 1`: idempotent retry check (or returns `409` with fresh snapshot if acked by another user / stale version).
  - Validates `version == alert.version` and `state == 'firing'`; updates `state = 'acknowledged'`, `version = version + 1`, `acknowledged_by`, `acknowledged_by_email`, `acknowledged_at = NOW()`.
  - Inserts `alert_audit_log` row (`alert_acknowledged`) in the **same DB transaction**.
- **Responses:** `200 OK` (updated alert object), `404 Not Found`, `409 Conflict` (includes latest alert snapshot in `details`).

---

#### `POST /api/v1/alerts/{alert_id}/unacknowledge`
Transitions an alert from `acknowledged` → `firing` (releases ownership back to the NOC queue).
- **Required Permission:** `noc-alert-manager-app.alerts:triage`
- **Request Body:** `{ "version": 2, "comment": "Handing back to shift queue." }`
- **Behavior:** Clears `acknowledged_by`, `acknowledged_by_email`, `acknowledged_at`; increments `version`; logs `alert_unacknowledged`. Does **not** delete any already-linked ServiceNow incident.
- **Responses:** `200 OK`, `404 Not Found`, `409 Conflict`.

---

#### `POST /api/v1/alerts/{alert_id}/dismiss`
Transitions an alert from `firing` or `acknowledged` → `dismissed`.
- **Required Permission:** `noc-alert-manager-app.alerts:triage`
- **Request Body:**
```json
{
  "version": 2,
  "reason": "Known transient spike during scheduled backup window."
}
```
- **Validation:** `reason` is required (3–500 chars) to enforce auditability.
- **Behavior:** Sets `state = 'dismissed'`, `dismissed_by`, `dismissed_by_email`, `dismissed_at = NOW()`, `dismiss_reason`; increments `version`; logs `alert_dismissed`. Repeat Alertmanager webhooks for this exact `(fingerprint, starts_at)` will keep it `dismissed`, while a future re-fire after resolution creates a fresh `firing` episode.
- **Responses:** `200 OK`, `400 Bad Request`, `404 Not Found`, `409 Conflict`.

---

#### `POST /api/v1/alerts/{alert_id}/reopen`
Transitions a `dismissed` alert back to `firing` (while the episode is not yet resolved).
- **Required Permission:** `noc-alert-manager-app.alerts:triage`
- **Request Body:** `{ "version": 3, "comment": "Issue is not related to backup window; re-triaging." }`
- **Responses:** `200 OK`, `404 Not Found`, `409 Conflict`.

---

#### `POST /api/v1/alerts/{alert_id}/notes`
Appends an operator triage comment to `alert_audit_log` without mutating alert state.
- **Required Permission:** `noc-alert-manager-app.alerts:triage`
- **Request Body:** `{ "comment": "Paged DBA on-call via Slack #db-ops." }`
- **Responses:** `201 Created` (returns created audit event), `400 Bad Request`, `404 Not Found`.

---

### 4.2 ServiceNow Incident Endpoints

#### `POST /api/v1/alerts/{alert_id}/servicenow/incident`
Creates a new ServiceNow incident via the Table API and links it to the alert episode.
- **Required Permission:** `noc-alert-manager-app.incidents:write`
- **Headers:** `Idempotency-Key: <uuid>` (recommended; additionally guarded server-side by `servicenow_incidents.alert_id` unique constraint + `correlation_id = alert_id`).
- **Preconditions:** Alert must exist and be in `acknowledged` state (or `resolved` if operator creates a retrospective incident — configurable; default allows `acknowledged` only, returning `409 INVALID_STATE_TRANSITION` if `firing` or `dismissed`).
- **Request Body:**
```json
{
  "short_description": "[CRITICAL] HighDatabaseReplicationLag: Replication lag > 30s on orders-db-replica-2",
  "description": "Alert: HighDatabaseReplicationLag\nCluster: prod-eu1\n...",
  "impact": 1,
  "urgency": 1,
  "assignment_group": "Database SRE",
  "category": "Database",
  "caller_mode": "sso_user",
  "work_notes": "Acknowledged by NOC; escalating to Database SRE."
}
```
- **Field Validation:**
  - `short_description`: required, 5–160 chars.
  - `impact`, `urgency`: integers in `{1, 2, 3}`.
  - `caller_mode`: `"sso_user"` (default) or `"generic_noc"`.
- **Two-Phase Reservation & Execution Pattern (No DB Locks Held During External HTTP!):**
  1. **Tx 1 (Reservation):** `SELECT ... FROM alerts WHERE id = $1 FOR UPDATE`. Verify `state == 'acknowledged'`. Insert into `servicenow_incidents` with `sync_status = 'pending'`, `correlation_id = alert_id` (using `ON CONFLICT (alert_id)`).
     - If an existing row has `sync_status IN ('created', 'linked')` → return `409 INCIDENT_ALREADY_LINKED` with existing incident details.
     - If an existing row has `sync_status = 'pending'` and `updated_at > NOW() - INTERVAL '60 seconds'` → return `409 INCIDENT_CREATION_IN_PROGRESS`.
     - If an existing row has `sync_status = 'failed'` (or stale `pending` > 60s) → transition back to `pending`, increment `retry_count`, and proceed.
     - Log `snow_incident_requested` in `alert_audit_log`. Commit Tx 1.
  2. **External Call (Outside DB Transaction):**
     - Resolve caller (`sso_user` → lookup `operator.Email` in `snow_user_cache` / SNOW `sys_user`; on miss/error fall back to `SNOW_DEFAULT_NOC_CALLER_SYS_ID` and record `fallback_reason`).
     - If retrying a stale `pending` record, first query ServiceNow `GET /api/now/table/incident?sysparm_query=correlation_id=<alert_id>` to recover any incident that succeeded on the previous timed-out attempt (prevents duplicate tickets!).
     - Call `POST /api/now/table/incident` with `correlation_id = alert_id`.
  3. **Tx 2 (Finalization):**
     - On SNOW `201 Created` (or `200 OK`): Update `servicenow_incidents` (`sync_status = 'created'`, `snow_sys_id`, `incident_number`, `incident_url`, `snow_state`, `priority`, `caller_resolution_status`, `caller_sys_id`, `caller_fallback_reason`, `snow_state_synced_at = NOW()`). Insert `snow_incident_created` (and `snow_caller_fallback` if fallback occurred) into `alert_audit_log`.
     - On SNOW error/timeout: Update `servicenow_incidents` (`sync_status = 'failed'`, `last_error`). Insert `snow_incident_failed` into `alert_audit_log`. **Alert remains `acknowledged`.**
- **Response `201 Created`:**
```json
{
  "alert_id": "c7a2d5e8-9f31-4b2c-8a11-7e5f2c4d9a01",
  "servicenow_incident": {
    "id": "e4f8a1b2-3c4d-4e5f-8a6b-9c0d1e2f3a4b",
    "sync_status": "created",
    "snow_sys_id": "51615243c0a801640139e875df691a31",
    "incident_number": "INC0012345",
    "incident_url": "https://corp.service-now.com/nav_to.do?uri=incident.do?sys_id=51615243c0a801640139e875df691a31",
    "snow_state": 1,
    "snow_state_label": "New",
    "priority": 1,
    "assignment_group": "Database SRE",
    "caller_mode": "sso_user",
    "caller_resolution_status": "fallback_noc",
    "caller_fallback_reason": "No active sys_user found for alice.smith@corp.example; fell back to generic NOC user.",
    "snow_state_synced_at": "2026-11-25T16:13:05Z"
  },
  "warnings": [
    "Caller fell back to generic NOC user because alice.smith@corp.example was not found in ServiceNow."
  ]
}
```

---

#### `POST /api/v1/alerts/{alert_id}/servicenow/link`
Links an existing ServiceNow incident (e.g., `INC0012345`) to an `acknowledged` alert without creating a new ticket.
- **Required Permission:** `noc-alert-manager-app.incidents:write`
- **Request Body:** `{ "incident_number": "INC0012345", "post_work_note": true }`
- **Behavior:** Verifies `INC0012345` via `GET /api/now/table/incident?sysparm_query=number=INC0012345`, stores `sync_status = 'linked'`, optionally appends a work note in SNOW linking the Grafana alert, and records `snow_incident_linked` in `alert_audit_log`.
- **Responses:** `200 OK`, `404 INCIDENT_NOT_FOUND`, `409 INCIDENT_ALREADY_LINKED`, `502/503/504`.

---

#### `POST /api/v1/alerts/{alert_id}/servicenow/sync`
Forces an immediate live fetch of the linked incident's current `state`, `priority`, `assignment_group`, and resolution timestamps from ServiceNow. Used by operators verifying a `resolved_open_ticket` alert.
- **Required Permission:** `noc-alert-manager-app.incidents:write`
- **Responses:** `200 OK` (returns updated `servicenow_incident` and `incident_drift_status`), `404 Not Found`, `502/503/504`.

---

#### `POST /api/v1/alerts/{alert_id}/servicenow/work-notes`
Posts a work note to the linked ServiceNow incident (e.g., nudging the team when an alert has resolved while the ticket remains open).
- **Required Permission:** `noc-alert-manager-app.incidents:write`
- **Request Body:** `{ "work_notes": "Prometheus alert resolved at 2026-11-25T16:52:00Z. Please verify and close this incident." }`
- **Behavior:** Prepends `[Grafana NOC — <operator.Login> (<operator.Email>)]` and calls `PATCH /api/now/table/incident/{snow_sys_id}`; logs `snow_work_note_posted` in `alert_audit_log`.
- **Responses:** `200 OK`, `400 Bad Request`, `404 Not Found`, `502/503/504`.

---

#### `GET /api/v1/servicenow/metadata`
Returns cached dropdown metadata for the incident creation modal (configured default assignment group, category list, default caller mode, and cached SNOW assignment groups).
- **Required Permission:** `noc-alert-manager-app.alerts:read`
- **Responses:** `200 OK`.

---

### 4.3 Webhook & Operational Endpoints

| Method & Path | Summary | Detailed Contract |
|---|---|---|
| `POST /api/v1/webhooks/alertmanager` | Ingests Alertmanager v4 webhook batches (`alerts[]`), upserting/reconciling episodes in PostgreSQL. | `docs/design/04-alertmanager-ingestion.md` |
| `POST /api/v1/webhooks/servicenow` | Receives ServiceNow Business Rule push updates when an incident's `state` or `work_notes` changes. | `docs/design/05-servicenow-integration.md` |
| `GET /healthz` | Liveness probe — returns `200 {"status":"ok"}` whenever the Go HTTP server is alive. | `docs/design/LLD.md` §6 |
| `GET /readyz` | Readiness probe — verifies PostgreSQL `SELECT 1` within 2s. Intentionally does **not** fail when ServiceNow is unreachable. | `docs/design/LLD.md` §6 |
| `GET /metrics` | Exposes Prometheus counters/histograms/gauges for HTTP latency, DB pool, webhook ingestion, SNOW calls, and open drift alerts. | `docs/design/LLD.md` §6 |

---

## 5. Concurrency & Locking Strategy

Because multiple NOC operators and Alertmanager webhook deliveries can target the same alert simultaneously, the Go API enforces a **three-layer concurrency model**:

1. **Row-Level Serialization in PostgreSQL (`SELECT ... FOR UPDATE`):**
   - Every state mutation (both operator actions and Alertmanager reconciliation) locks the target alert row inside a short (<50 ms) `READ COMMITTED` transaction before reading state, applying transition rules, incrementing `version`, and appending to `alert_audit_log`.
   - For Alertmanager batch payloads containing multiple alerts, alerts are **sorted deterministically by `(fingerprint, starts_at)`** before acquiring row locks to make PostgreSQL deadlocks impossible.
2. **Optimistic Concurrency Control (`version` Column) for Human Actions:**
   - Operator mutations require the `version` they last saw in the UI. Even after acquiring the row lock, if `db.version != req.version`, the transaction aborts cleanly and returns `409 Conflict` with the current row state. This prevents "lost updates" (e.g., Operator B dismissing an alert 2 seconds after Operator A acknowledged it and opened a ticket).
3. **Never Hold DB Transactions Open Across External HTTP Calls:**
   - ServiceNow API calls can take 500 ms – 15 s. Therefore, incident creation uses the **Two-Phase Reservation pattern** (Tx 1 inserts `sync_status='pending'` and commits → HTTP call to ServiceNow → Tx 2 updates to `'created'` or `'failed'`).
   - Database connection pools (`PG_MAX_CONNS`) cannot be starved by a slow ServiceNow instance.

---

## 6. Zero-Binary Proxy Fallback Verification Contract

If an environment opts out of the thin `CallResource` Go backend and uses Grafana `plugin.json` `routes` (`/api/plugin-proxy/noc-alert-manager-app/api/*`):
- Grafana server must enable `[dataproxy] send_user_header = true` in `grafana.ini`.
- `plugin.json` defines separate routes with `reqRole` (`Viewer` for `GET`, `Editor` for `POST`) and injects `X-Proxy-Token: {{ .SecureJsonData.proxySharedToken }}`.
- The Go API accepts `X-Proxy-Token` + `X-Grafana-User` only when `ALLOW_LEGACY_PROXY_HEADER_AUTH=true`, sets `OperatorContext.Login = X-Grafana-User`, and looks up ServiceNow `sys_user` by `user_name` instead of `email` (falling back to generic `noc` if unmatched).
- **Trade-off Note:** Because `X-Grafana-User` does not include Grafana role/permissions or email, `CallResource` with signed internal JWTs remains the **recommended primary mode**.

---

## 7. Service Configuration (Environment Variables Only)

| Environment Variable | Required | Default | Description |
|---|---|---|---|
| `PORT` | No | `8080` | HTTP listen port for API & webhooks. |
| `ADMIN_PORT` | No | `9090` | Internal port for `/healthz`, `/readyz`, `/metrics`. |
| `LOG_LEVEL` | No | `info` | Structured log level (`debug`, `info`, `warn`, `error`). |
| `DATABASE_URL` | Yes | — | PostgreSQL DSN (e.g., `postgres://${PG_USER}:${PG_PASSWORD}@${PG_HOST}:5432/${PG_DB}?sslmode=require`). |
| `PG_MAX_CONNS` | No | `25` | Maximum `pgxpool` connections. |
| `PG_MIN_CONNS` | No | `5` | Minimum idle `pgxpool` connections. |
| `GRAFANA_PLUGIN_JWT_SECRET` | Yes* | — | HMAC-SHA256 secret shared with `gpx_noc_alert_manager` (*required in primary mode). |
| `GRAFANA_PLUGIN_JWT_SECRET_PREVIOUS` | No | — | Optional secondary secret for zero-downtime key rotation. |
| `GRAFANA_PLUGIN_JWT_ISSUER` | No | `noc-alert-manager-app` | Expected JWT `iss` claim. |
| `GRAFANA_PUBLIC_URL` | Yes | — | Base Grafana URL used to generate deep links inside ServiceNow incident descriptions. |
| `ALLOW_LEGACY_PROXY_HEADER_AUTH` | No | `false` | Enables `X-Proxy-Token` + `X-Grafana-User` fallback mode. |
| `GRAFANA_PROXY_SHARED_TOKEN` | No | — | Required only if `ALLOW_LEGACY_PROXY_HEADER_AUTH=true`. |
| `ALERTMANAGER_WEBHOOK_TOKEN` | Yes | — | Bearer token authenticating Alertmanager webhook requests. |
| `ALERTMANAGER_MAX_PAYLOAD_BYTES` | No | `5242880` | Max request body size (`5 MiB`) for Alertmanager webhooks. |
| `SNOW_*` | Yes | — | ServiceNow integration env vars (fully specified in `docs/design/05-servicenow-integration.md`). |
