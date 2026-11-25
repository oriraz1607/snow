package httpapi

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/oriraz/grafana-snow-integration/backend/internal/apierror"
	"github.com/oriraz/grafana-snow-integration/backend/internal/migrate"
	"github.com/oriraz/grafana-snow-integration/backend/internal/store"
)

// HealthHandler serves /healthz and /readyz on the internal admin listener.
type HealthHandler struct {
	pool *pgxpool.Pool
}

// NewHealthHandler constructs a HealthHandler.
func NewHealthHandler(pool *pgxpool.Pool) *HealthHandler {
	return &HealthHandler{pool: pool}
}

// Healthz handles GET /healthz (liveness). Never checks external dependencies.
func (h *HealthHandler) Healthz(w http.ResponseWriter, _ *http.Request) {
	apierror.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Readyz handles GET /readyz (readiness). Verifies PostgreSQL SELECT 1 + schema version within 2s.
// Intentionally excludes ServiceNow so ServiceNow maintenance never removes healthy API pods.
func (h *HealthHandler) Readyz(w http.ResponseWriter, r *http.Request) {
	if err := store.CheckReadiness(r.Context(), h.pool, migrate.ExpectedSchemaVersion); err != nil {
		apierror.Write(w, r, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "PostgreSQL readiness check failed.").WithCause(err))
		return
	}
	apierror.WriteJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}
