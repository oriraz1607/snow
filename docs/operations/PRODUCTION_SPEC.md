# Production Operations Specification

This document defines the runtime dependencies, environment/secret schema, database privilege boundaries, external IAM permissions, and Kubernetes/OpenShift RBAC/NetworkPolicy controls for operating **NOC Alert Manager** (`noc-alert-manager-app` + standalone Go API) in production.

> **Security Policy:** Zero real credentials, API tokens, passwords, or private hostnames belong in Git. All sensitive values must be supplied via external secret managers (`ExternalSecret`) and mounted into environment variables or Kubernetes `Secret` objects at runtime.

---

## 1. Runtime Dependencies

| Component | Supported Version(s) | Purpose & Production Notes |
|---|---|---|
| **Go Toolchain** | Go `1.26` | Builds `cmd/api`, `cmd/migrate`, `cmd/mocksnow`, and `plugin/pkg` (`CGO_ENABLED=0` static binaries for `linux/amd64` and `linux/arm64`). |
| **PostgreSQL** | PostgreSQL `15+` / `16` | Primary relational store (`pgx/v5`), using separate owner (`alert_owner`) and least-privilege runtime (`alert_api_app`) roles. Enforce TLS (`sslmode=require` or `verify-full`). |
| **Grafana** | Grafana `13.x` (`>= 13.2.0`, verified on `13.3.2`) | Hosts the `noc-alert-manager-app` React 19 App Plugin and Go `CallResource` backend proxy. Signed plugin bundle recommended in production. |
| **Prometheus Alertmanager** | `v0.27+` | Sends v4 alert webhooks (`POST /api/v1/webhooks/alertmanager`) with `max_alerts: 0` (to avoid silent truncation) and Bearer token auth. |
| **ServiceNow** | Table API (`/api/now/table/...`) + OAuth2 `/oauth_token.do` | Target ITSM system for incident creation, linking, work notes, state polling, and inbound Business Rule state-change webhooks. |
| **Confluence (Optional / Recommended)** | Any HTTPS Confluence Cloud or Data Center instance | Stores NOC alert documentation / runbook pages linked by `alert_name` (`https://` enforced; optionally restricted via `CONFLUENCE_ALLOWED_HOSTS`). |

---

## 2. Environment Variable & Secret Schema

Variables are split cleanly across runtime API pods (`noc-alert-api-secrets` + `noc-alert-api-config`), PreSync schema migration jobs (`noc-alert-migrate-secrets`), and Grafana plugin configuration.

### 2.1 Core HTTP, Database & TLS Variables

| Variable | Req. | Type / Format | Default | Sensitivity & Source | Description & Validation Rules |
|---|---|---|---|---|---|
| `DATABASE_URL` | **Yes** | PostgreSQL DSN URL | — | **Secret** (`noc-alert-api-secrets`) | DSN for the least-privilege `alert_api_app` role. Production must enforce `sslmode=require` or `verify-full`. |
| `MIGRATE_DATABASE_URL` | **Migrate Only** | PostgreSQL DSN URL | `DATABASE_URL` | **Secret** (`noc-alert-migrate-secrets`) | DSN for the schema-owner `alert_owner` role used strictly by `/bin/migrate` PreSync jobs. Never mount in API pods. |
| `POSTGRES_APP_USER` | Opt | String | `alert_api_app` | ConfigMap | Target DB role granted table/sequence privileges by `migrate up`. |
| `PG_MAX_CONNS` | Opt | Int32 (`>= 1`) | `25` | ConfigMap | Maximum `pgxpool` connections per API replica. |
| `PG_MIN_CONNS` | Opt | Int32 (`>= 0`, `<= PG_MAX_CONNS`) | `5` | ConfigMap | Minimum idle `pgxpool` connections per API replica. |
| `PORT` | Opt | Port string | `8080` | ConfigMap | Public/Grafana/Alertmanager HTTP API listener port (`Service` + `Route`). |
| `ADMIN_PORT` | Opt | Port string | `9090` | ConfigMap | Cluster-internal operational listener serving `/healthz`, `/readyz`, and `/metrics`. **Must never be exposed externally.** |
| `LOG_LEVEL` | Opt | `debug`\|`info`\|`warn`\|`error` | `info` | ConfigMap | Structured JSON logger verbosity (`slog`). Config secrets are redacted via `Config.LogValue()`. |
| `TLS_CERT_FILE` | Opt | File path | `""` | Volume / Secret | Path to PEM TLS certificate for in-process HTTPS on `PORT` when edge TLS termination is not used. Both cert and key must be set together. |
| `TLS_KEY_FILE` | Opt | File path | `""` | Volume / Secret | Path to PEM TLS private key paired with `TLS_CERT_FILE`. |

### 2.2 Authentication, Hardening & Confluence Allowlist Variables

| Variable | Req. | Type / Format | Default | Sensitivity & Source | Description & Validation Rules |
|---|---|---|---|---|---|
| `GRAFANA_PLUGIN_JWT_SECRET` | **Yes** (when legacy auth false) | String (`>= 32` random bytes) | — | **Secret** (`noc-alert-api-secrets` + Grafana `secureJsonData.apiJwtSecret`) | HS256 shared secret used by the Grafana backend proxy to sign short-lived operator JWTs. |
| `GRAFANA_PLUGIN_JWT_SECRET_PREVIOUS` | Opt | String | `""` | **Secret** (`noc-alert-api-secrets`) | Secondary HS256 verification key accepted during zero-downtime key rotation windows. |
| `GRAFANA_PLUGIN_JWT_ISSUER` | Opt | String | `noc-alert-manager-app` | ConfigMap | Expected `iss` claim in operator JWTs. |
| `GRAFANA_PUBLIC_URL` | **Yes** | HTTPS URL | — | ConfigMap | Public Grafana base URL (used to construct `Grafana Alert Link` deep URLs in ServiceNow work notes). |
| `ALLOW_LEGACY_PROXY_HEADER_AUTH` | Opt | Bool | `false` | ConfigMap | **Must be `false` in production.** Enables legacy `X-Grafana-*` shared-token proxy fallback and emits a startup warning if `true`. |
| `GRAFANA_PROXY_SHARED_TOKEN` | Cond. | String | `""` | **Secret** | Required only when `ALLOW_LEGACY_PROXY_HEADER_AUTH=true`. Keep unset in production. |
| `RATE_LIMIT_ENABLED` | Opt | Bool | `true` | ConfigMap | Enables per-IP token-bucket rate limiting on API and webhook routes (`429 RATE_LIMITED` + `Retry-After`). |
| `RATE_LIMIT_API_RPS` / `RATE_LIMIT_API_BURST` | Opt | Int (`>= 1`) | `60` / `120` | ConfigMap | Per-client steady rate (req/s) and burst capacity for `/api/v1/*` operator endpoints. |
| `RATE_LIMIT_WEBHOOK_RPS` / `RATE_LIMIT_WEBHOOK_BURST` | Opt | Int (`>= 1`) | `30` / `60` | ConfigMap | Per-client steady rate (req/s) and burst capacity for `/api/v1/webhooks/*`. |
| `TRUST_PROXY_HEADERS` | Opt | Bool | `false` (`true` behind Ingress/Route) | ConfigMap | Whether to trust `X-Forwarded-For` / `X-Real-IP` for client IP extraction in rate limiting. Set `true` behind OpenShift Router / trusted Ingress. |
| `CORS_ALLOWED_ORIGINS` | Opt | CSV of origins | `""` | ConfigMap | Exact allowed browser origins (e.g., `https://grafana.prod.example.com`). When empty, CORS headers are omitted (recommended when requests proxy via Grafana). |
| `CONFLUENCE_ALLOWED_HOSTS` | Opt | CSV of hostnames / `.domain` suffixes | `""` | ConfigMap | Restricts allowed Confluence documentation hosts (e.g., `confluence.example.com,.confluence.internal`). Empty allows any valid `https://` hostname. |

### 2.3 Alertmanager & ServiceNow Integration Variables

| Variable | Req. | Type / Format | Default | Sensitivity & Source | Description & Validation Rules |
|---|---|---|---|---|---|
| `ALERTMANAGER_WEBHOOK_TOKEN` | **Yes** | High-entropy bearer string | — | **Secret** (`noc-alert-api-secrets` + Alertmanager secret) | Shared Bearer token protecting `POST /api/v1/webhooks/alertmanager`. |
| `ALERTMANAGER_MAX_PAYLOAD_BYTES` | Opt | Int64 (`>= 1024`) | `5242880` (5 MiB) | ConfigMap | Maximum body size accepted for inbound Alertmanager and ServiceNow webhooks. |
| `SNOW_INSTANCE_URL` | **Yes** | HTTPS URL | — | ConfigMap | Base URL of the production ServiceNow instance (e.g., `https://prod.service-now.com`). |
| `SNOW_AUTH_MODE` | Opt | `oauth_client_credentials` \| `basic` | `oauth_client_credentials` | ConfigMap | ServiceNow Table API authentication mode. OAuth client credentials is recommended for production. |
| `SNOW_TOKEN_URL` | Cond. | HTTPS URL | `<SNOW_INSTANCE_URL>/oauth_token.do` | ConfigMap | OAuth2 token endpoint used when `SNOW_AUTH_MODE=oauth_client_credentials`. |
| `SNOW_CLIENT_ID` / `SNOW_CLIENT_SECRET` | Cond. | String | — | **Secret** (`noc-alert-api-secrets`) | Required when `SNOW_AUTH_MODE=oauth_client_credentials`. |
| `SNOW_OAUTH_SCOPE` | Opt | String | `""` | ConfigMap | Optional OAuth2 scope requested from ServiceNow. |
| `SNOW_USERNAME` / `SNOW_PASSWORD` | Cond. | String | — | **Secret** | Required only when `SNOW_AUTH_MODE=basic`. |
| `SNOW_DEFAULT_NOC_CALLER_SYS_ID` | **Yes** | 32-char `sys_id` | — | **Secret** / Config | Fallback generic `noc` user `sys_id` in ServiceNow `sys_user` when operator email lookup misses or `generic_noc` mode is chosen. |
| `SNOW_DEFAULT_NOC_CALLER_USERNAME` | Opt | String | `noc` | ConfigMap | Display username stored on incident records when generic fallback caller is used. |
| `SNOW_DEFAULT_ASSIGNMENT_GROUP` | Opt | String | `""` (`Infrastructure` in manifests) | ConfigMap | Default ServiceNow assignment group prefilled in Create Incident modal. |
| `SNOW_DEFAULT_CATEGORY` | Opt | String | `Infrastructure` | ConfigMap | Default ServiceNow incident category. |
| `SNOW_INBOUND_WEBHOOK_SECRET` | **Rec.** | High-entropy HMAC string | `""` | **Secret** (`noc-alert-api-secrets` + ServiceNow Business Rule) | Shared HMAC-SHA256 secret (`X-ServiceNow-Signature`, timestamp/nonce replay protection) for inbound state updates & active-status checks. |
| `SNOW_HTTP_TIMEOUT_SECONDS` | Opt | Int64 (`>= 1`) | `15` | ConfigMap | Timeout per outbound HTTP call to ServiceNow. |
| `SNOW_POLL_INTERVAL_SECONDS` | Opt | Int64 (`>= 1`) | `120` | ConfigMap | Interval for background ServiceNow open-incident reconciliation worker. |
| `SNOW_AUTO_POST_RESOLVED_NOTE` | Opt | Bool | `true` | ConfigMap | Automatically posts recovery work notes when all linked alert episodes resolve. |
| `SNOW_NOTE_WORKER_INTERVAL_SECONDS` | Opt | Int64 (`>= 1`) | `15` | ConfigMap | Poll interval for the pending resolved-note delivery worker. |
| `SNOW_AUTO_REOPEN_COOLDOWN_SECONDS` | Opt | Int64 (`>= 1`) | `300` | ConfigMap | Cooldown between automatic ServiceNow reopens when an alert re-fires on a closed ticket. |
| `SNOW_AUTO_REOPEN_MAX_ATTEMPTS` | Opt | Int32 (`>= 0`) | `3` | ConfigMap | Maximum auto-reopen attempts per linked incident before halting. |
| `ALLOW_RETROSPECTIVE_INCIDENT_CREATE` | Opt | Bool | `false` | ConfigMap | Disallows creating new incidents on already-resolved alert episodes unless explicitly enabled. |

### 2.4 Secret Rotation Procedures

1. **Zero-Downtime Operator JWT Secret Rotation (`GRAFANA_PLUGIN_JWT_SECRET`):**
   1. Copy the active secret into `GRAFANA_PLUGIN_JWT_SECRET_PREVIOUS` in Vault / Secrets Manager and wait for `ExternalSecret` sync + API rollout.
   2. Set the newly generated >=32-byte key in `GRAFANA_PLUGIN_JWT_SECRET` on both the API secret store and Grafana plugin `secureJsonData.apiJwtSecret`.
   3. Restart / reload Grafana plugin settings (minting with new key; API verifies both current and previous keys during 60s token lifetime).
   4. Clear `GRAFANA_PLUGIN_JWT_SECRET_PREVIOUS` after rollover completes.
2. **Webhook & ServiceNow Credentials (`ALERTMANAGER_WEBHOOK_TOKEN`, `SNOW_CLIENT_SECRET`, `SNOW_INBOUND_WEBHOOK_SECRET`):**
   - Rotate in ServiceNow/Alertmanager alongside external secret store updates, wait for `ExternalSecret` refresh (`1h` default or manual annotation sync), and trigger rolling restart of `noc-alert-api`.
3. **Database Passwords (`POSTGRES_APP_PASSWORD`, owner password):**
   - Rotate password in PostgreSQL (`ALTER ROLE alert_api_app WITH PASSWORD ...`), update Secret store, and roll API pods. Never reuse the `alert_owner` password inside `DATABASE_URL`.

---

## 3. Database Role Separation (`alert_owner` vs. `alert_api_app`)

Database privileges are strictly partitioned to prevent runtime SQL injection or application bugs from altering schema DDL or tampering with audit logs:

| Database Role | Used By | Privileges Granted | Explicitly Denied |
|---|---|---|---|
| `alert_owner` | ArgoCD `PreSync` Hook Job (`/bin/migrate up`) | Database & `public` schema ownership; `CREATE`/`ALTER`/`DROP` tables, enums, indexes, triggers; executes `GrantAppRole()` after applying migrations `0001`–`0007`. | Never mounted in `noc-alert-api` runtime Deployment (`MIGRATE_DATABASE_URL` lives in separate `noc-alert-migrate-secrets`). |
| `alert_api_app` | Long-running `noc-alert-api` pods | `CONNECT` on DB, `USAGE` on `public`, `SELECT`/`INSERT`/`UPDATE`/`DELETE` on operational tables (`alerts`, `servicenow_incidents`, `snow_user_cache`, `webhook_ingestion_log`, `alert_fingerprint_teams`, `idempotency_keys`, `team_assignment_group_mappings`, `alert_confluence_docs`), `USAGE`/`SELECT` on sequences, **and `SELECT, INSERT` ONLY on `alert_audit_log`**. | `CREATE`, `ALTER`, `DROP`, **`UPDATE` on `alert_audit_log`**, **`DELETE` on `alert_audit_log`**, and read/write on `schema_migrations` (`42501 permission denied`). Enforced by `backend/internal/migrate/runner.go` and verified in integration tests on both PG 15 & PG 16. |

---

## 4. External IAM & Integration Permissions

| System | Identity / Credential | Least-Privilege Permissions Required |
|---|---|---|
| **ServiceNow** | Dedicated OAuth Client / Integration User | Read-only access to `sys_user` (lookup by active `email`), `sys_user_group` (metadata dropdowns), and `sys_choice` (priority/category metadata). Read/Write (`create`, `query`, `write` `state` & `work_notes`) restricted strictly to `incident` table. Zero `admin` role required. Inbound Business Rule (`docs/servicenow/prevent-close-active-alert-br.js`) holds only `SNOW_INBOUND_WEBHOOK_SECRET` for HMAC signing. |
| **Grafana** | Built-in Org Roles (`Viewer`, `Editor`, `Admin`) + `plugin.json` Role Grants | Plugin backend proxy (`pkg/plugin/rbac.go`) maps Grafana roles to granular actions: `Viewer` (`alerts:read`), `Editor` (`alerts:read`, `alerts:triage`, `incidents:write`), `Admin` (+ `settings:write`). Proxy enforces route allowlist and blocks non-`v1` paths (e.g., `/webhooks/*`). |
| **Alertmanager** | Bearer Token Secret | Can invoke `POST /api/v1/webhooks/alertmanager` only. Cannot invoke operator endpoints (`/api/v1/alerts*`) or ServiceNow webhook endpoints. |
| **Secrets Manager (Vault / AWS / Azure)** | External Secrets Operator Identity | Read-only `ClusterSecretStore` access scoped strictly to `noc-alert-manager/<env>/*` secret paths, with `noc-alert-api-secrets` and `noc-alert-migrate-secrets` split cleanly. |

---

## 5. Kubernetes & OpenShift Security / RBAC Policies

Production manifests reside in `deploy/k8s/` and ArgoCD definitions in `deploy/argocd/`:

1. **Pod Security & ServiceAccount (`deploy/k8s/base/serviceaccount.yaml`, `deployment.yaml`, `migrate-job.yaml`):**
   - Dedicated `ServiceAccount` (`noc-alert-api`) with `automountServiceAccountToken: false` (repeated on pod specs).
   - Zero Kubernetes `Role` / `ClusterRole` bindings required — the Go API never talks to the Kubernetes API server.
   - Namespace labeled with `pod-security.kubernetes.io/enforce: restricted`.
   - Container runs as non-root `USER 10001:10001` with `runAsNonRoot: true`, `allowPrivilegeEscalation: false`, `readOnlyRootFilesystem: true`, `capabilities.drop: ["ALL"]`, `seccompProfile.type: RuntimeDefault`, and a small bounded `/tmp` `emptyDir`. *(Note: On OpenShift `restricted-v2` clusters that assign arbitrary namespace UID ranges, remove explicit `runAsUser`/`fsGroup` in an overlay patch; container binaries and `/migrations` are world-readable/executable `0755`/`a+rX`.)*
2. **Network & Port Isolation (`service.yaml`, `service-admin.yaml`, `route.yaml`, `networkpolicy.yaml`):**
   - External TLS terminates at OpenShift `Route` (`termination: edge`, `insecureEdgeTerminationPolicy: Redirect`) forwarding strictly to `Service/noc-alert-api` port `8080` (`http`).
   - Operational `ADMIN_PORT` (`9090`: `/healthz`, `/readyz`, `/metrics`) is exposed **only** on internal `ClusterIP` `Service/noc-alert-api-admin` for Kubernetes probes and Prometheus scraping — **never** referenced by `Route` or public `Service`.
   - `NetworkPolicy` restricts ingress on `8080` to ingress router, `grafana`, and `monitoring` namespaces, and restricts ingress on `9090` strictly to cluster monitoring namespaces.
3. **Authentication Enforcement:**
   - Base and all production overlays (`single-site`, `multi-site/site-a`, `multi-site/site-b`) explicitly enforce `ALLOW_LEGACY_PROXY_HEADER_AUTH: "false"`.
