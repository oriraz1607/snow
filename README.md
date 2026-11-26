# NOC Alert Manager (`grafana-snow-integration`)

A NOC alert-management platform that ingests Prometheus Alertmanager v4 webhooks into PostgreSQL, provides a 4-bucket triage queue, instance filtering/sorting, Confluence runbook linking, and team-tagging console inside Grafana (`noc-alert-manager-app`), and creates or links single and bulk ServiceNow incidents with SSO caller attribution (plus generic `noc` fallback) and bi-directional drift tracking.

See the master architecture in [docs/design/LLD.md](docs/design/LLD.md) and production hardening details in [docs/operations/PRODUCTION_SPEC.md](docs/operations/PRODUCTION_SPEC.md).

---

## Repository Layout

| Path | Description |
|---|---|
| `backend/` | Standalone Go 1.26 API service (`cmd/api`), SQL migration runner (`cmd/migrate`), mock ServiceNow server (`cmd/mocksnow`), and E2E integration tests (`tests/integration/`). |
| `plugin/` | Grafana 13.x (`>=13.2.0`, React 19) App Plugin `noc-alert-manager-app` (`v1.1.0`): TypeScript/React frontend (`src/`), thin Go `CallResource` identity-signing proxy (`pkg/`), and multi-arch bundler (`scripts/package-plugin.sh`). |
| `migrations/` | Versioned PostgreSQL 15+ SQL migrations (`0001_initial_schema`, `0002_team_tags`, `0003_idempotency_keys`, `0004_severity_sort_index`, `0005_team_assignment_group_mappings`, `0006_alert_instance`, `0007_alert_confluence_docs` `.up.sql` / `.down.sql`). |
| `deploy/` | Local Compose stack (`docker-compose.yml`), DB least-privilege init (`postgres-init/`), Alertmanager/Prometheus configs, Kustomize production manifests (`deploy/k8s/`), and ArgoCD GitOps specs (`deploy/argocd/`). |
| `docs/` | Architecture & component designs (`docs/design/`), ServiceNow Business Rule (`docs/servicenow/`), and Production Operations Specification ([PRODUCTION_SPEC.md](docs/operations/PRODUCTION_SPEC.md)). |
| `.github/workflows/` | Automated lint/test matrix for PG 15 & PG 16 (`ci.yml`) and container/multi-arch plugin build workflows (`release.yml`). |
| `.env.example` | Template environment variable file for local development (placeholders only — zero real secrets). |

---

## Prerequisites

- **Podman Compose** or **Docker Compose** (for running the local stack)
- **Go 1.26+** (aligned across `backend/` and `plugin/`)
- **Node.js 22+ & npm** (for building, linting, and testing the Grafana 13 plugin)
- **PostgreSQL 15+ or 16** (if running migrations/integration tests externally)

---

## Running the Local Stack

1. Copy `.env.example` to `.env` in the repository root and replace placeholder values with local dev credentials:

   ```bash
   cp .env.example .env
   ```

2. Build the Grafana plugin (if `plugin/dist/` has not been built yet — see [Building & Packaging the Plugin](#building--packaging-the-plugin)).

3. Start the stack (`postgres`, `migrate`, `mocksnow`, `api`, `alertmanager`, `grafana`) from the repository root:

   ```bash
   # Podman Compose
   podman compose -f deploy/docker-compose.yml --env-file .env up -d --build

   # Or Docker Compose
   docker compose -f deploy/docker-compose.yml --env-file .env up -d --build
   ```

   Set `POSTGRES_IMAGE=docker.io/library/postgres:15-alpine` in `.env` or your shell environment to validate against PostgreSQL 15.

| Service | Port(s) | Purpose |
|---|---|---|
| `postgres` | `5432` | PostgreSQL 16 (or `15-alpine`) database with least-privilege app role (`alert_api_app`). |
| `migrate` | — | One-shot container running `/bin/migrate -dir /migrations up` and applying app role grants. |
| `mocksnow` | `8085` | Mock ServiceNow Table API + OAuth/Basic auth + inbound webhook simulator. |
| `api` | `8080`, `9090` | Non-root (`10001:10001`) Go Alert-Management API (`8080`) and internal `/healthz`, `/readyz`, `/metrics` (`9090`). |
| `alertmanager` | `9093` | Prometheus Alertmanager v0.27.0 forwarding webhooks to `api`. |
| `grafana` | `3000` | Grafana 13.3.2 with `noc-alert-manager-app` mounted from `plugin/dist/`. |

Stop the stack with:

```bash
podman compose -f deploy/docker-compose.yml --env-file .env down
```

---

## Building & Packaging the Plugin

Build both the React 19 / TypeScript bundle and multi-arch Linux Go backend binaries (`linux/amd64` + `linux/arm64`) into `plugin/dist/`:

```bash
# Produce distributable zip bundle + sha256 checksum
./plugin/scripts/package-plugin.sh
```

Or build manually for local Compose (`linux/amd64`):

```bash
cd plugin
npm ci
npm run build
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o dist/gpx_noc_alert_manager_linux_amd64 ./pkg
npx -y @grafana/react-detect@latest
```

For frontend watch mode during development:

```bash
cd plugin && npm run dev
```

---

## Production Deployment

Full operations, secret schemas, database privilege isolation, and Kubernetes/OpenShift security policies are documented in [docs/operations/PRODUCTION_SPEC.md](docs/operations/PRODUCTION_SPEC.md). Key production controls:

- **GitOps Manifests (Kustomize & ArgoCD):** Base and production overlays for single-site (`deploy/k8s/overlays/single-site`) and multi-site (`deploy/k8s/overlays/multi-site`) topologies render cleanly via `kubectl kustomize` and deploy with ArgoCD (`deploy/argocd/`), running DB migrations via a `PreSync` Hook Job.
- **TLS & Reverse Proxy Isolation:** Terminate external TLS at OpenShift `Route` / Ingress forwarding only to port `8080` (never expose internal `ADMIN_PORT` `9090`). Set `TRUST_PROXY_HEADERS=true` when behind a trusted ingress controller so per-IP rate limiting extracts the caller IP accurately, configure `CORS_ALLOWED_ORIGINS` strictly, and enforce `ALLOW_LEGACY_PROXY_HEADER_AUTH=false`.
- **Secrets & Role Separation:** Inject runtime credentials via `ExternalSecret` (`noc-alert-api-secrets` for `alert_api_app`, `noc-alert-migrate-secrets` for `alert_owner`) and use `GRAFANA_PLUGIN_JWT_SECRET_PREVIOUS` for zero-downtime key rotation.
- **Plugin Multi-Arch Builds & Signing:** Package `linux_amd64` + `linux_arm64` bundles using `plugin/scripts/package-plugin.sh` or `.github/workflows/release.yml`. Set `GRAFANA_ACCESS_POLICY_TOKEN` (and `GRAFANA_PLUGIN_ROOT_URLS` for private plugins) so `@grafana/sign-plugin` generates `MANIFEST.txt`. Only use `GF_PLUGINS_ALLOW_LOADING_UNSIGNED_PLUGINS=noc-alert-manager-app` in isolated dev/air-gapped test environments where signing is unavailable.

---

## Running Tests & Linters

### Backend Lint, Vet & Unit Tests (`backend/`)

```bash
cd backend
go vet ./...
go test -race ./...
# Lint with golangci-lint v2 (or container image):
golangci-lint run --config ../.golangci.yml ./...
```

### Backend Integration Tests (`-tags integration`)

Requires the Compose stack (`postgres`, `migrate`, `mocksnow`, `api`, `alertmanager`) to be running with the `INTEGRATION_*` test environment variables set:

```bash
cd backend
go test -tags integration -v ./tests/integration/...
```

### Plugin Go Backend & Frontend Checks (`plugin/`)

```bash
cd plugin
go vet ./pkg/...
go test -race ./pkg/...
npm run lint        # runs: eslint "src/**/*.{ts,tsx}"
npm run typecheck   # runs: tsc --noEmit
npm test            # runs: jest
```

---

## Main Environment Variables

All secrets are supplied via environment variables (see `.env.example` and [docs/operations/PRODUCTION_SPEC.md](docs/operations/PRODUCTION_SPEC.md)):

| Category | Variable Names |
|---|---|
| **PostgreSQL & Migrations** | `POSTGRES_IMAGE`, `POSTGRES_DB`, `POSTGRES_USER`, `POSTGRES_PASSWORD`, `POSTGRES_APP_USER`, `POSTGRES_APP_PASSWORD`, `DATABASE_URL`, `MIGRATE_DATABASE_URL`, `PG_MAX_CONNS`, `PG_MIN_CONNS` |
| **HTTP Listeners, TLS & Hardening** | `PORT`, `ADMIN_PORT`, `LOG_LEVEL`, `TLS_CERT_FILE`, `TLS_KEY_FILE`, `RATE_LIMIT_ENABLED`, `RATE_LIMIT_API_RPS`, `RATE_LIMIT_API_BURST`, `RATE_LIMIT_WEBHOOK_RPS`, `RATE_LIMIT_WEBHOOK_BURST`, `TRUST_PROXY_HEADERS`, `CORS_ALLOWED_ORIGINS`, `CONFLUENCE_ALLOWED_HOSTS` |
| **Grafana Plugin Auth & URL** | `GRAFANA_PLUGIN_JWT_SECRET`, `GRAFANA_PLUGIN_JWT_SECRET_PREVIOUS`, `GRAFANA_PLUGIN_JWT_ISSUER`, `GRAFANA_PUBLIC_URL`, `ALLOW_LEGACY_PROXY_HEADER_AUTH`, `GRAFANA_PROXY_SHARED_TOKEN`, `GF_SECURITY_ADMIN_USER`, `GF_SECURITY_ADMIN_PASSWORD` |
| **Alertmanager Webhook** | `ALERTMANAGER_WEBHOOK_TOKEN`, `ALERTMANAGER_MAX_PAYLOAD_BYTES` |
| **ServiceNow Integration** | `SNOW_INSTANCE_URL`, `SNOW_AUTH_MODE`, `SNOW_TOKEN_URL`, `SNOW_CLIENT_ID`, `SNOW_CLIENT_SECRET`, `SNOW_OAUTH_SCOPE`, `SNOW_USERNAME`, `SNOW_PASSWORD`, `SNOW_DEFAULT_NOC_CALLER_SYS_ID`, `SNOW_DEFAULT_NOC_CALLER_USERNAME`, `SNOW_DEFAULT_ASSIGNMENT_GROUP`, `SNOW_DEFAULT_CATEGORY`, `SNOW_INBOUND_WEBHOOK_SECRET`, `SNOW_HTTP_TIMEOUT_SECONDS`, `SNOW_POLL_INTERVAL_SECONDS`, `SNOW_AUTO_POST_RESOLVED_NOTE`, `SNOW_NOTE_WORKER_INTERVAL_SECONDS`, `SNOW_AUTO_REOPEN_COOLDOWN_SECONDS`, `SNOW_AUTO_REOPEN_MAX_ATTEMPTS`, `ALLOW_RETROSPECTIVE_INCIDENT_CREATE` |
