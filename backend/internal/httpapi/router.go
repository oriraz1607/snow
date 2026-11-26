package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/oriraz/grafana-snow-integration/backend/internal/auth"
	"github.com/oriraz/grafana-snow-integration/backend/internal/config"
	"github.com/oriraz/grafana-snow-integration/backend/internal/observability"
)

// NewAPIRouter builds the public HTTP API & webhook router (served on PORT).
func NewAPIRouter(
	cfg *config.Config,
	logger *slog.Logger,
	metrics *observability.Metrics,
	alerts *AlertsHandler,
	snow *ServiceNowHandler,
	webhooks *WebhooksHandler,
) http.Handler {
	r := chi.NewRouter()
	r.Use(RequestIDMiddleware())
	r.Use(RecoverMiddleware(logger))
	r.Use(SecurityHeadersMiddleware(cfg.TLSCertFile != "" && cfg.TLSKeyFile != ""))
	r.Use(CORSMiddleware(cfg.CORSAllowedOrigins))
	r.Use(AccessLogAndMetricsMiddleware(logger, metrics))

	if cfg.AllowLegacyProxyHeaderAuth && logger != nil {
		logger.Warn(
			"ALLOW_LEGACY_PROXY_HEADER_AUTH is enabled; legacy proxy header authentication is deprecated and must be disabled in production",
			slog.String("component", "http_server"),
		)
	}

	var apiRateLimit func(http.Handler) http.Handler
	var webhookRateLimit func(http.Handler) http.Handler
	if cfg.RateLimitEnabled {
		apiRPS := cfg.RateLimitAPIRPS
		if apiRPS <= 0 {
			apiRPS = 60
		}
		apiBurst := cfg.RateLimitAPIBurst
		if apiBurst <= 0 {
			apiBurst = 120
		}
		whRPS := cfg.RateLimitWebhookRPS
		if whRPS <= 0 {
			whRPS = 30
		}
		whBurst := cfg.RateLimitWebhookBurst
		if whBurst <= 0 {
			whBurst = 60
		}
		apiRateLimit = RateLimitMiddleware(float64(apiRPS), apiBurst, cfg.TrustProxyHeaders)
		webhookRateLimit = RateLimitMiddleware(float64(whRPS), whBurst, cfg.TrustProxyHeaders)
	}

	operatorAuth := auth.OperatorMiddleware(auth.OperatorAuthConfig{
		JWTSecret:                cfg.JWTSecret,
		JWTSecretPrevious:        cfg.JWTSecretPrevious,
		JWTIssuer:                cfg.JWTIssuer,
		AllowLegacyProxyFallback: cfg.AllowLegacyProxyHeaderAuth,
		ProxySharedToken:         cfg.ProxySharedToken,
	})

	r.Route("/api/v1", func(v1 chi.Router) {
		// Operator-facing alert & ServiceNow routes
		v1.Group(func(op chi.Router) {
			if apiRateLimit != nil {
				op.Use(apiRateLimit)
			}
			op.Use(operatorAuth)

			op.With(auth.RequirePermission(auth.PermAlertsRead)).Get("/alerts", alerts.List)
			op.With(auth.RequirePermission(auth.PermAlertsRead)).Get("/alerts/{alert_id}", alerts.Get)
			op.With(auth.RequirePermission(auth.PermAlertsRead)).Get("/alerts/{alert_id}/history", alerts.History)
			op.With(auth.RequirePermission(auth.PermAlertsRead)).Get("/servicenow/metadata", snow.Metadata)

			op.With(auth.RequirePermission(auth.PermAlertsTriage)).Post("/alerts/{alert_id}/acknowledge", alerts.Acknowledge)
			op.With(auth.RequirePermission(auth.PermAlertsTriage)).Post("/alerts/{alert_id}/unacknowledge", alerts.Unacknowledge)
			op.With(auth.RequirePermission(auth.PermAlertsTriage)).Post("/alerts/bulk/dismiss", alerts.BulkDismiss)
			op.With(auth.RequirePermission(auth.PermAlertsTriage)).Post("/alerts/{alert_id}/dismiss", alerts.Dismiss)
			op.With(auth.RequirePermission(auth.PermAlertsTriage)).Post("/alerts/{alert_id}/reopen", alerts.Reopen)
			op.With(auth.RequirePermission(auth.PermAlertsTriage)).Post("/alerts/{alert_id}/notes", alerts.AddNote)
			op.With(auth.RequirePermission(auth.PermAlertsTriage)).Post("/alerts/{alert_id}/team", alerts.TagTeam)
			op.With(auth.RequirePermission(auth.PermAlertsTriage)).Post("/alerts/{alert_id}/confluence", alerts.UpdateConfluenceURL)

			op.With(auth.RequirePermission(auth.PermIncidentsWrite)).Post("/servicenow/incidents/bulk", snow.CreateBulkIncident)
			op.With(auth.RequirePermission(auth.PermIncidentsWrite)).Post("/alerts/{alert_id}/servicenow/incident", snow.CreateIncident)
			op.With(auth.RequirePermission(auth.PermIncidentsWrite)).Post("/alerts/{alert_id}/servicenow/link", snow.LinkIncident)
			op.With(auth.RequirePermission(auth.PermIncidentsWrite)).Post("/alerts/{alert_id}/servicenow/sync", snow.SyncIncident)
			op.With(auth.RequirePermission(auth.PermIncidentsWrite)).Post("/alerts/{alert_id}/servicenow/work-notes", snow.PostWorkNote)
		})

		// Machine-to-machine webhooks
		v1.Group(func(wh chi.Router) {
			if webhookRateLimit != nil {
				wh.Use(webhookRateLimit)
			}
			wh.With(auth.AlertmanagerBearerMiddleware(cfg.AlertmanagerWebhookToken)).
				Post("/webhooks/alertmanager", webhooks.Alertmanager)

			wh.With(auth.ServiceNowWebhookMiddleware(cfg.SnowInboundWebhookSecret, cfg.AlertmanagerMaxPayloadBytes)).
				Post("/webhooks/servicenow", webhooks.ServiceNow)
		})

		v1.With(auth.ServiceNowWebhookMiddleware(cfg.SnowInboundWebhookSecret, cfg.AlertmanagerMaxPayloadBytes)).
			Get("/integrations/servicenow/incidents/{snow_sys_id}/active-status", snow.IncidentActiveStatus)
	})

	return r
}

// NewAdminRouter builds the internal operational probe & metrics router (served on ADMIN_PORT).
func NewAdminRouter(health *HealthHandler, metrics *observability.Metrics) http.Handler {
	r := chi.NewRouter()
	r.Get("/healthz", health.Healthz)
	r.Get("/readyz", health.Readyz)
	r.Handle("/metrics", promhttp.HandlerFor(metrics.Registry, promhttp.HandlerOpts{}))
	return r
}
