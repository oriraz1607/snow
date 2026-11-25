# 05 — ServiceNow Integration Specification

## 1. Purpose & Scope

This document specifies how the Go backend API integrates with **ServiceNow (SNOW)** via the **ServiceNow Table API** (`/api/now/table/...`) to support:
1. On-demand incident creation from an acknowledged alert in Grafana (modeled after the Grafana Cloud IRM ServiceNow workflow).
2. **SSO User Caller Attribution with Graceful Generic `noc` Fallback** (leveraging the shared corporate SSO identity between Grafana Generic OAuth and ServiceNow `sys_user`).
3. Idempotent ticket creation (`correlation_id`) preventing duplicate incidents on retries or timeouts.
4. **Bi-directional State & Link-Back Tracking** (ServiceNow Business Rule inbound webhook + background reconciler poller) to detect **alert-resolved / ticket-still-open drift (`resolved_open_ticket`)** and **alert-firing / ticket-closed drift (`active_closed_ticket`)**.
5. Environment-variable-only secret management and least-privilege ServiceNow service account permissions.

---

## 2. ServiceNow Authentication & Secret Handling

### 2.1 Transport Authentication Modes

All credentials are supplied strictly via environment variables (injected at runtime from Kubernetes Secrets, Vault, or AWS/GCP Secret Manager). **No credentials are ever stored in code, config files, or Grafana `jsonData`.**

#### Primary Mode: OAuth 2.0 Client Credentials (`SNOW_AUTH_MODE=oauth_client_credentials`)
- The Go API requests a bearer access token from `SNOW_TOKEN_URL` (typically `https://<instance>.service-now.com/oauth_token.do`) using `grant_type=client_credentials` with `SNOW_CLIENT_ID` and `SNOW_CLIENT_SECRET` (and optional `SNOW_OAUTH_SCOPE`).
- Tokens are cached in memory by the Go HTTP client and proactively refreshed `60 seconds` before `expires_in` (guarded by `singleflight` so concurrent requests trigger only one token refresh).
- On `401 Unauthorized` from ServiceNow, the client invalidates the cached token once, refreshes, and retries the request once.

#### Fallback Mode: Dedicated Service Account Basic Auth (`SNOW_AUTH_MODE=basic`)
- Uses HTTP Basic Auth with `SNOW_USERNAME` and `SNOW_PASSWORD` for a dedicated, non-interactive ServiceNow integration user (e.g., `svc_grafana_noc`).
- Mirrors the Grafana Cloud IRM recommendation: **never use a ServiceNow `admin` account**; assign only least-privilege roles.

### 2.2 Required ServiceNow Service Account Roles
- `itil` (standard role granting create/read/update on `incident` table and `work_notes`) — or a custom scoped ACL granting `create`/`read`/`write` on `incident` and `read` on `sys_user` and `sys_user_group`.
- `rest_service` / `web_service_admin` *(instance-dependent — verify whether your ServiceNow instance restricts Table API access via the `snc_platform_rest_api_access` role `[UNVERIFIED]`)*.

### 2.3 Environment Variable Reference (Placeholders Only)

| Environment Variable | Required | Default | Description |
|---|---|---|---|
| `SNOW_INSTANCE_URL` | Yes | — | Base URL of ServiceNow instance (e.g., `https://${SNOW_INSTANCE}.service-now.com`). |
| `SNOW_AUTH_MODE` | No | `oauth_client_credentials` | `oauth_client_credentials` or `basic`. |
| `SNOW_TOKEN_URL` | If OAuth | `${SNOW_INSTANCE_URL}/oauth_token.do` | OAuth 2.0 token endpoint. |
| `SNOW_CLIENT_ID` | If OAuth | — | OAuth Application Registry client ID. |
| `SNOW_CLIENT_SECRET` | If OAuth | — | OAuth Application Registry client secret. |
| `SNOW_OAUTH_SCOPE` | No | `""` | Optional OAuth scope string. |
| `SNOW_USERNAME` | If Basic | — | Dedicated service account username. |
| `SNOW_PASSWORD` | If Basic | — | Dedicated service account password. |
| `SNOW_DEFAULT_NOC_CALLER_SYS_ID` | Yes | — | ServiceNow `sys_user.sys_id` for the generic `noc` fallback user. |
| `SNOW_DEFAULT_NOC_CALLER_USERNAME` | No | `noc` | Display/fallback username for logs and UI labels. |
| `SNOW_DEFAULT_ASSIGNMENT_GROUP` | No | `""` | Fallback `assignment_group` name or `sys_id` when alert labels do not specify one. |
| `SNOW_DEFAULT_CATEGORY` | No | `Infrastructure` | Default `incident.category` choice value (`[UNVERIFIED]` — confirm choice list on target instance). |
| `SNOW_INBOUND_WEBHOOK_SECRET` | If push sync | — | Shared HMAC-SHA256 secret authenticating `POST /api/v1/webhooks/servicenow`. |
| `SNOW_HTTP_TIMEOUT_SECONDS` | No | `15` | Per-request HTTP timeout to ServiceNow Table API. |
| `SNOW_POLL_INTERVAL_SECONDS` | No | `120` | Background poller interval for syncing open linked incidents (`snow_state IN (1, 2, 3)`). |
| `SNOW_AUTO_POST_RESOLVED_NOTE` | No | `true` | If `true`, posts an informational work note to ServiceNow when a linked alert resolves while the ticket is still open. |

---

## 3. SSO Caller Attribution & Generic `noc` Fallback

Because Grafana uses **Generic OAuth** against the same corporate SSO Identity Provider as ServiceNow, NOC operators can attribute created tickets to their personal SSO user with automatic, non-blocking fallback to the generic `noc` user.

### 3.1 Why Service-Account Transport + `sys_user` Lookup (Instead of Forwarding User OAuth Tokens)
1. Grafana App Plugins (`type: "app"`) do **not** have a native `oauthPassThru` toggle like Data Source plugins; Grafana keeps the user's raw IdP OAuth access token inside its core session store.
2. Even when forwarding an external OIDC token to ServiceNow (which requires registering the corporate IdP under *System OAuth → Application Registry → Configure an OIDC provider to verify ID tokens*), individual NOC operators frequently lack direct ServiceNow Table API write ACLs or may be logged in under a shared account — causing ticket creation to fail mid-outage.
3. By authenticating the HTTP transport with the dedicated service account and resolving `caller_id` via `sys_user` lookup from the operator's verified SSO email (`PluginContext.User.Email`), **ticket creation never fails due to per-user ACL gaps**, while still cleanly attributing the incident to the operator's SSO user whenever matched!

### 3.2 Caller Resolution Algorithm

```mermaid
flowchart TD
    Start["Create Incident Request\n(caller_mode, operator.Email, operator.Login)"] --> Mode{"caller_mode ==\n'generic_noc'?"}
    Mode -->|Yes| UseNocExplicit["caller_sys_id = SNOW_DEFAULT_NOC_CALLER_SYS_ID\nstatus = 'generic_noc_selected'"]

    Mode -->|No: sso_user| HasEmail{"operator.Email\nnon-empty?"}
    HasEmail -->|No| FallbackNoEmail["Fallback to SNOW_DEFAULT_NOC_CALLER_SYS_ID\nstatus = 'fallback_noc'\nreason = 'Grafana session has no email'"]

    HasEmail -->|Yes| CacheCheck["Check snow_user_cache\nWHERE email = lower(operator.Email)\nAND expires_at > NOW()"]
    CacheCheck --> CacheHit{"Valid cache hit?"}
    CacheHit -->|Yes & snow_sys_id != NULL| UseMatched["caller_sys_id = cached.snow_sys_id\nstatus = 'sso_matched'"]
    CacheHit -->|Yes & snow_sys_id == NULL| FallbackCachedMiss["Fallback to SNOW_DEFAULT_NOC_CALLER_SYS_ID\nstatus = 'fallback_noc'\nreason = 'Cached negative lookup'"]

    CacheHit -->|No| QuerySnow["GET /api/now/table/sys_user?\nsysparm_query=email=<email>^active=true\n&sysparm_fields=sys_id,user_name,email&sysparm_limit=1\n(timeout: 3s)"]
    QuerySnow --> QueryOK{"200 OK &\nlen(result) == 1?"}
    QueryOK -->|Yes| CachePos["Upsert snow_user_cache (TTL 6h)\ncaller_sys_id = result[0].sys_id\nstatus = 'sso_matched'"]
    QueryOK -->|200 OK & len == 0| CacheNeg["Upsert snow_user_cache (NULL, TTL 5m)\nFallback to SNOW_DEFAULT_NOC_CALLER_SYS_ID\nstatus = 'fallback_noc'"]
    QueryOK -->|HTTP Error / Timeout| FallbackErr["Do NOT cache error\nFallback to SNOW_DEFAULT_NOC_CALLER_SYS_ID\nstatus = 'fallback_noc'\nreason = 'sys_user lookup failed/timed out'"]
```

- **Secondary Safety Net:** If `POST /api/now/table/incident` with the matched `caller_sys_id` is rejected by a ServiceNow Business Rule/ACL with `400`/`403` referencing `caller_id`, the client retries **once** with `SNOW_DEFAULT_NOC_CALLER_SYS_ID`, sets `caller_resolution_status = 'fallback_noc'`, and appends the operator's SSO identity to `work_notes`.
- **Auditability:** Regardless of whether `sso_matched`, `generic_noc_selected`, or `fallback_noc` is used, `work_notes` always records `Initiated in Grafana by: <operator.Name> (<operator.Email>)`.
- **Note on `opened_by` vs `caller_id`:** By default, ServiceNow sets `opened_by` to the authenticated API service account unless an instance ACL/script permits overriding `opened_by` (`[UNVERIFIED]` — confirm on target ServiceNow instance whether `opened_by` is writable by the service account; this design sets `caller_id` unconditionally and treats overriding `opened_by` as optional).

---

## 4. ServiceNow Table API Contracts & Field Mapping

### 4.1 Create Incident (`POST /api/now/table/incident`)

**Request URL:**
```http
POST ${SNOW_INSTANCE_URL}/api/now/table/incident?sysparm_fields=sys_id,number,state,impact,urgency,priority,assignment_group,caller_id,opened_at,sys_updated_on&sysparm_display_value=all
Authorization: Bearer <access_token>
Accept: application/json
Content-Type: application/json
```

**Request Body:**
```json
{
  "short_description": "[CRITICAL] HighDatabaseReplicationLag: Replication lag > 30s on orders-db-replica-2",
  "description": "Grafana NOC Alert Details\n-------------------------\nAlert Name: HighDatabaseReplicationLag\nSeverity: critical\nStarts At: 2026-11-25T16:10:00Z\nFingerprint: 1a2b3c4d5e6f7a8b\nGrafana Link: https://grafana.corp.example/a/noc-alert-manager-app/alerts/c7a2d5e8-9f31-4b2c-8a11-7e5f2c4d9a01\nPrometheus Query: https://prometheus.internal.example.com/graph?g0.expr=...\nRunbook: https://runbooks.internal.example.com/db-lag\n\nLabels:\n- cluster: prod-eu1\n- instance: orders-db-replica-2:9187\n- service: orders-db\n- severity: critical",
  "impact": "1",
  "urgency": "1",
  "assignment_group": "Database SRE",
  "category": "Database",
  "caller_id": "<resolved_sys_id_or_SNOW_DEFAULT_NOC_CALLER_SYS_ID>",
  "correlation_id": "c7a2d5e8-9f31-4b2c-8a11-7e5f2c4d9a01",
  "correlation_display": "Grafana NOC Alert Manager",
  "contact_type": "monitoring",
  "work_notes": "[Grafana NOC] Incident created by Alice Smith (alice.smith@corp.example). Acknowledged at 2026-11-25T16:12:15Z."
}
```

### 4.2 Alert-to-ServiceNow Field Mapping Table

| ServiceNow `incident` Field | Source / Transformation | Notes |
|---|---|---|
| `short_description` | Operator-edited modal input; default `"[<UPPER(severity)>] <alert_name>: <annotations.summary>"`, truncated to 160 chars. | Required by OOB ServiceNow incident table. |
| `description` | Operator-edited modal input; default structured summary of alert timestamps, labels, annotations, `generator_url`, `runbook_url`, and Grafana deep link. | Gives responders full context without switching tools. |
| `impact` | `"1"` (High), `"2"` (Medium), `"3"` (Low). Default derived from `alerts.severity` (`critical`→`1`, `warning`→`2`, `info`/`unknown`→`3`). | OOB ServiceNow choice integers (serialized as strings or ints). |
| `urgency` | `"1"` (High), `"2"` (Medium), `"3"` (Low). Default matches `impact`. | ServiceNow Data Lookup Rules auto-calculate read-only `priority` (`1`–`5`). |
| `assignment_group` | Operator-selected group; pre-filled from `labels["assignment_group"]` → `labels["team"]` → `SNOW_DEFAULT_ASSIGNMENT_GROUP`. | Accepts group `sys_id` or exact display name (when `sysparm_input_display_value=true` or resolved `sys_id`). |
| `category` | Operator-selected category; default `SNOW_DEFAULT_CATEGORY`. | `[UNVERIFIED]` — confirm exact valid choice values in target SNOW instance (`sys_choice` table for `incident.category`). |
| `caller_id` | Resolved `sys_user.sys_id` (from SSO email) or `SNOW_DEFAULT_NOC_CALLER_SYS_ID`. | Always populated with a valid `sys_id`. |
| `correlation_id` | `alerts.id` (UUID string of this specific alert episode). | Primary idempotency & deduplication key in ServiceNow. |
| `correlation_display` | Static `"Grafana NOC Alert Manager"`. | Identifies the originating integration in ServiceNow UI. |
| `contact_type` | Static `"monitoring"` (or `"event"` — `[UNVERIFIED]` confirm allowed `contact_type` choice value on target SNOW instance). | Optional; omit if restricted by instance choice policy. |
| `work_notes` | Operator note + Grafana operator identity + fallback explanation (if `fallback_noc`). | Written to internal Activity Stream (not customer-visible `comments`). |

### 4.3 Verified ServiceNow Response Parsing (`201 Created` / `200 OK`)

```json
{
  "result": {
    "sys_id": { "display_value": "51615243c0a801640139e875df691a31", "value": "51615243c0a801640139e875df691a31" },
    "number": { "display_value": "INC0012345", "value": "INC0012345" },
    "state": { "display_value": "New", "value": "1" },
    "priority": { "display_value": "1 - Critical", "value": "1" },
    "assignment_group": { "display_value": "Database SRE", "value": "d625dccec0a8016700a222a0f7900d06" },
    "opened_at": { "display_value": "2026-11-25 16:13:04", "value": "2026-11-25 16:13:04" }
  }
}
```

- **Deep Link Construction:** `${SNOW_INSTANCE_URL}/nav_to.do?uri=incident.do?sys_id=${result.sys_id.value}`.
- **OOB ServiceNow State Mapping:**

| `snow_state` (DB Value) | `snow_state_label` | Considered Open in Drift Detection? |
|---|---|---|
| `1` | `New` | **Yes (Open)** |
| `2` | `In Progress` | **Yes (Open)** |
| `3` | `On Hold` | **Yes (Open)** |
| `6` | `Resolved` | No (Terminal/Resolved) |
| `7` | `Closed` | No (Terminal/Closed) |
| `8` | `Canceled` | No (Terminal/Canceled) |

*(Note: `[UNVERIFIED]` — If your ServiceNow instance has customized incident state integers beyond OOB values 1/2/3/6/7/8, update the state map and DB `CHECK` constraint accordingly.)*

---

### 4.4 Idempotency Recovery Before Retrying a Timed-Out Creation

If an initial `POST /api/now/table/incident` times out at the Go HTTP client (`504 Gateway Timeout`), ServiceNow may still have committed the record. Before issuing a retry `POST`, the Go API executes:

```http
GET ${SNOW_INSTANCE_URL}/api/now/table/incident?sysparm_query=correlation_id=${alert_id}&sysparm_fields=sys_id,number,state,priority,assignment_group,opened_at&sysparm_display_value=all&sysparm_limit=1
```

- If a record is returned → adopt it (`sync_status = 'created'`, store `sys_id` and `number`) without creating a duplicate ticket!
- If empty → safely issue `POST /api/now/table/incident`.

---

### 4.5 Updating Work Notes (`PATCH /api/now/table/incident/{sys_id}`)

Used for both manual NOC nudges (`POST /api/v1/alerts/{id}/servicenow/work-notes`) and optional automated resolution notes (`SNOW_AUTO_POST_RESOLVED_NOTE=true`):

```http
PATCH ${SNOW_INSTANCE_URL}/api/now/table/incident/${snow_sys_id}
Authorization: Bearer <access_token>
Content-Type: application/json

{
  "work_notes": "[Grafana NOC] Prometheus Alertmanager reported alert HighDatabaseReplicationLag RESOLVED at 2026-11-25T16:52:00Z. This incident is still open in ServiceNow — please verify recovery and resolve/close the ticket."
}
```

> **Important Policy Decision:** When an alert stops firing (`alerts.state = 'resolved'`), the Go API **does NOT automatically close or resolve the ServiceNow incident**. Instead, it posts an informational work note (if `SNOW_AUTO_POST_RESOLVED_NOTE=true`) and flags the episode under **`Resolved — Open Ticket`** (`incident_drift_status = "resolved_open_ticket"`) so the NOC and owning engineering team have explicit visibility that the alert cleared while the ticket remains unresolved.

---

## 5. Bi-Directional & Link-Back State Synchronization

To guarantee the NOC console always knows whether the engineering team has resolved a ServiceNow ticket — especially after an alert stops firing — the design combines **real-time push (webhook)** with a **self-healing background poller**.

### 5.1 Push Channel: ServiceNow Inbound Webhook (`POST /api/v1/webhooks/servicenow`)

Configure an asynchronous **ServiceNow Business Rule** (after update on `incident` when `correlation_display == 'Grafana NOC Alert Manager'` OR `correlation_id IS NOT EMPTY`, triggering when `state` changes):
- **Auth:** Computes `HMAC-SHA256(raw_body, SNOW_INBOUND_WEBHOOK_SECRET)` sent in `X-SNOW-Signature-256: sha256=<hex>`.
- **Inbound Payload Contract:**
```json
{
  "sys_id": "51615243c0a801640139e875df691a31",
  "number": "INC0012345",
  "correlation_id": "c7a2d5e8-9f31-4b2c-8a11-7e5f2c4d9a01",
  "state": 6,
  "state_label": "Resolved",
  "priority": 1,
  "assignment_group": "Database SRE",
  "updated_by": "jane.dba",
  "resolved_at": "2026-11-25T17:10:00Z",
  "closed_at": null,
  "close_notes": "Caught up replica WAL lag after index rebuild completed."
}
```
- **Go API Processing:**
  1. Verifies HMAC signature with `hmac.Equal`.
  2. Finds all `servicenow_incidents` matching `snow_sys_id = $1` (handles both newly created and manually linked alerts).
  3. Updates `snow_state`, `snow_state_label`, `priority`, `assignment_group`, `snow_resolved_at`, `snow_closed_at`, `snow_state_synced_at = NOW()`.
  4. Inserts `snow_state_synced` into `alert_audit_log` (recording previous → new SNOW state and `updated_by` / `close_notes`).
  5. **Drift Reconciliation:**
     - If `alerts.state == 'resolved'` and `snow_state IN (6, 7, 8)` → drift clears automatically (`resolved_open_ticket` → `none`)!
     - If `alerts.state IN ('firing', 'acknowledged')` and `snow_state IN (6, 7, 8)` → surfaces `incident_drift_status = "active_closed_ticket"` in the NOC UI (warning that the ticket was closed while the alert is still active; does **not** forcibly resolve the Prometheus alert).

### 5.2 Pull Channel: Background Reconciler Poller (Self-Healing Guarantee)

ServiceNow Business Rules can be skipped, misconfigured, or fail during transient network blips. Therefore, a lightweight background goroutine in the Go API runs every `SNOW_POLL_INTERVAL_SECONDS` (default `120s`, using a PostgreSQL advisory lock `pg_try_advisory_lock` so only one API replica polls at a time):
1. Queries up to `50` oldest-synced open tickets using `idx_snow_incidents_open_sync`:
   ```sql
   SELECT DISTINCT snow_sys_id
   FROM servicenow_incidents
   WHERE sync_status IN ('created', 'linked')
     AND snow_state IN (1, 2, 3)
   ORDER BY snow_state_synced_at ASC NULLS FIRST
   LIMIT 50;
   ```
2. Fetches them in a **single batched Table API request**:
   ```http
   GET ${SNOW_INSTANCE_URL}/api/now/table/incident?sysparm_query=sys_idIN<id1,id2,...>&sysparm_fields=sys_id,number,state,priority,assignment_group,resolved_at,closed_at,sys_updated_on&sysparm_display_value=all
   ```
3. Updates `servicenow_incidents` rows and appends `snow_state_synced` audit events only when `snow_state` or `assignment_group` actually changed.
4. Updates Prometheus gauge `noc_alerts_resolved_with_open_snow_incident` so NOC leads can even alert on stale unclosed tickets!

---

## 6. Resilience, Rate Limiting & Circuit Breaking

- **Circuit Breaker:** If ServiceNow returns `>= 5` consecutive `5xx`/timeout errors within `60s`, the outbound SNOW client opens its circuit breaker for `30s` (half-open probe after `30s`), returning immediate `503 SNOW_UNAVAILABLE` to UI creation calls and pausing the background poller — protecting both ServiceNow and Go API worker threads.
- **Per-Request Timeouts:** `3s` for `sys_user` SSO lookups (fast fallback to `noc`), `15s` (`SNOW_HTTP_TIMEOUT_SECONDS`) for `POST/PATCH/GET` incident calls.
- **No Unbounded Retries on `POST`:** Automatic retries apply only to idempotent `GET` calls and `401` token refreshes; `POST` creation failures return an error to the operator with a safe manual **Retry** button (which runs the `correlation_id` pre-check first).
