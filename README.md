# NOC Alert Manager (`grafana-snow-integration`)

A NOC alert-management platform that ingests Prometheus Alertmanager v4 webhooks into PostgreSQL, provides a 4-bucket triage queue and team-tagging console inside Grafana (`noc-alert-manager-app`), and creates or links single and bulk ServiceNow incidents with SSO caller attribution (plus generic `noc` fallback) and bi-directional drift tracking.

See the master architecture and component specifications in [docs/design/LLD.md](docs/design/LLD.md).

---

## Repository Layout

| Path | Description |
|---|---|
| `backend/` | Standalone Go API service (`cmd/api`), SQL migration runner (`cmd/migrate`), mock ServiceNow server (`cmd/mocksnow`), and integration tests (`tests/integration/`). |
| `plugin/` | Grafana 11.6 App Plugin (`noc-alert-manager-app`): React/TypeScript frontend (`src/`) and thin Go `CallResource` identity-signing proxy (`pkg/`). |
| `migrations/` | Versioned PostgreSQL SQL migrations (`0001_initial_schema`, `0002_team_tags`, `.up.sql` / `.down.sql`). |
| `deploy/` | Local Compose stack (`docker-compose.yml`), PostgreSQL least-privilege role init script (`postgres-init/`), Alertmanager config (`alertmanager.yml`), and Grafana provisioning (`grafana/provisioning/`). |
| `docs/design/` | Low-level design ([LLD.md](docs/design/LLD.md)) and component specifications (`01`–`05`). |
| `.env.example` | Template environment variable file for local development (placeholders only). |

---

## Prerequisites

- **Podman Compose** or **Docker Compose** (for running the local stack)
- **Go 1.24+** (`backend/` targets Go 1.23+, `plugin/` targets Go 1.24+)
- **Node.js & npm** (for building and testing the Grafana plugin frontend)

---

## Running the Local Stack

1. Copy `.env.example` to `.env` in the repository root and replace placeholder values with local dev credentials:

   ```bash
   cp .env.example .env
   ```

2. Build the Grafana plugin (if `plugin/dist/` has not been built yet — see [Building the Plugin](#building-the-plugin)).

3. Start the stack (`postgres`, `migrate`, `mocksnow`, `api`, `alertmanager`, `grafana`) from the repository root:

   ```bash
   # Podman Compose
   podman compose -f deploy/docker-compose.yml --env-file .env up -d --build

   # Or Docker Compose
   docker compose -f deploy/docker-compose.yml --env-file .env up -d --build
   ```

| Service | Port(s) | Purpose |
|---|---|---|
| `postgres` | `5432` | PostgreSQL 16 database with least-privilege app role (`alert_api_app`). |
| `migrate` | — | One-shot container running `/bin/migrate -dir /migrations up` and applying app role grants. |
| `mocksnow` | `8085` | Mock ServiceNow Table API + OAuth/Basic auth + inbound webhook simulator. |
| `api` | `8080`, `9090` | Go Alert-Management API (`8080`) and admin `/healthz`, `/readyz`, `/metrics` (`9090`). |
| `alertmanager` | `9093` | Prometheus Alertmanager v0.27.0 forwarding webhooks to `api`. |
| `grafana` | `3000` | Grafana 11.6.0 with `noc-alert-manager-app` mounted from `plugin/dist/`. |

Stop the stack with:

```bash
podman compose -f deploy/docker-compose.yml --env-file .env down
```

---

## Building the Plugin

Build both the React/TypeScript bundle and the Linux `CallResource` Go backend binary into `plugin/dist/`:

```bash
cd plugin
npm ci
npm run build

# Build the Grafana Go plugin executable (Linux amd64 for the Grafana container)
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o dist/gpx_noc_alert_manager_linux_amd64 ./pkg
```

For frontend watch mode during development:

```bash
cd plugin && npm run dev
```

---

## Running Tests

### Backend Unit Tests (`backend/`)

```bash
cd backend
go vet ./...
go test ./...
```

### Backend Integration Tests (`-tags integration`)

Requires the Compose stack (`postgres`, `migrate`, `mocksnow`, `api`, `alertmanager`) to be running and the test environment variables (`GRAFANA_PLUGIN_JWT_SECRET`, `ALERTMANAGER_WEBHOOK_TOKEN`, `SNOW_DEFAULT_NOC_CALLER_SYS_ID`, `INTEGRATION_APP_DATABASE_URL`, `INTEGRATION_OWNER_DATABASE_URL`) to be exported:

```bash
cd backend
go test -tags integration -v ./tests/integration/...
```

### Plugin Go Backend Tests (`plugin/`)

```bash
cd plugin
go vet ./...
go test ./...
```

### Plugin Frontend Typecheck & Unit Tests (`plugin/`)

```bash
cd plugin
npm run typecheck   # runs: tsc --noEmit
npm test            # runs: jest
```

---

## Main Environment Variables

All secrets are supplied via environment variables (see `.env.example` for defaults and local placeholders):

| Category | Variable Names |
|---|---|
| **PostgreSQL & Migrations** | `POSTGRES_DB`, `POSTGRES_USER`, `POSTGRES_PASSWORD`, `POSTGRES_APP_USER`, `POSTGRES_APP_PASSWORD`, `DATABASE_URL`, `MIGRATE_DATABASE_URL`, `PG_MAX_CONNS`, `PG_MIN_CONNS` |
| **HTTP Listeners & Logging** | `PORT`, `ADMIN_PORT`, `LOG_LEVEL` |
| **Grafana Plugin Auth & URL** | `GRAFANA_PLUGIN_JWT_SECRET`, `GRAFANA_PLUGIN_JWT_SECRET_PREVIOUS`, `GRAFANA_PLUGIN_JWT_ISSUER`, `GRAFANA_PUBLIC_URL`, `ALLOW_LEGACY_PROXY_HEADER_AUTH`, `GRAFANA_PROXY_SHARED_TOKEN`, `GF_SECURITY_ADMIN_USER`, `GF_SECURITY_ADMIN_PASSWORD` |
| **Alertmanager Webhook** | `ALERTMANAGER_WEBHOOK_TOKEN`, `ALERTMANAGER_MAX_PAYLOAD_BYTES` |
| **ServiceNow Integration** | `SNOW_INSTANCE_URL`, `SNOW_AUTH_MODE`, `SNOW_TOKEN_URL`, `SNOW_CLIENT_ID`, `SNOW_CLIENT_SECRET`, `SNOW_OAUTH_SCOPE`, `SNOW_USERNAME`, `SNOW_PASSWORD`, `SNOW_DEFAULT_NOC_CALLER_SYS_ID`, `SNOW_DEFAULT_NOC_CALLER_USERNAME`, `SNOW_DEFAULT_ASSIGNMENT_GROUP`, `SNOW_DEFAULT_CATEGORY`, `SNOW_INBOUND_WEBHOOK_SECRET`, `SNOW_HTTP_TIMEOUT_SECONDS`, `SNOW_POLL_INTERVAL_SECONDS`, `SNOW_AUTO_POST_RESOLVED_NOTE`, `SNOW_NOTE_WORKER_INTERVAL_SECONDS`, `ALLOW_RETROSPECTIVE_INCIDENT_CREATE` |
