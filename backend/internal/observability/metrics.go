package observability

import (
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics owns the dedicated Prometheus registry and all application metrics from LLD §6.3.
type Metrics struct {
	Registry *prometheus.Registry

	HTTPRequestsTotal            *prometheus.CounterVec
	HTTPRequestDurationSeconds   *prometheus.HistogramVec
	AlertmanagerWebhooksTotal    *prometheus.CounterVec
	AlertmanagerAlertsProcessed  *prometheus.CounterVec
	AlertmanagerTruncatedAlerts  prometheus.Counter
	AlertsStateCurrent           *prometheus.GaugeVec
	AlertsResolvedOpenTicket     prometheus.Gauge
	AlertsActiveClosedTicket     prometheus.Gauge
	SnowRequestsTotal            *prometheus.CounterVec
	SnowRequestDurationSeconds   *prometheus.HistogramVec
	SnowCallerResolutionTotal    *prometheus.CounterVec
	SnowCircuitBreakerState      prometheus.Gauge
}

// NewMetrics constructs and registers all Prometheus collectors.
func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()

	m := &Metrics{
		Registry: reg,
		HTTPRequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "noc_http_requests_total",
			Help: "Total HTTP requests processed by method, route pattern, and status code.",
		}, []string{"method", "route", "status"}),
		HTTPRequestDurationSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "noc_http_request_duration_seconds",
			Help:    "HTTP request latency distribution in seconds.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "route"}),
		AlertmanagerWebhooksTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "noc_alertmanager_webhooks_total",
			Help: "Total Alertmanager webhook requests handled by HTTP status code.",
		}, []string{"status"}),
		AlertmanagerAlertsProcessed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "noc_alertmanager_alerts_processed_total",
			Help: "Total Alertmanager alerts processed by reconciliation action.",
		}, []string{"action"}),
		AlertmanagerTruncatedAlerts: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "noc_alertmanager_truncated_alerts_total",
			Help: "Total truncated alerts reported by Alertmanager webhooks.",
		}),
		AlertsStateCurrent: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "noc_alerts_state_current",
			Help: "Current count of active/open alerts by state.",
		}, []string{"state"}),
		AlertsResolvedOpenTicket: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "noc_alerts_resolved_with_open_snow_incident",
			Help: "Current count of resolved alerts with an open ServiceNow incident.",
		}),
		AlertsActiveClosedTicket: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "noc_alerts_active_with_closed_snow_incident",
			Help: "Current count of active firing/acknowledged alerts with a closed ServiceNow incident.",
		}),
		SnowRequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "noc_snow_requests_total",
			Help: "Total ServiceNow Table API requests by operation and status.",
		}, []string{"operation", "status"}),
		SnowRequestDurationSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "noc_snow_request_duration_seconds",
			Help:    "ServiceNow Table API request latency distribution in seconds.",
			Buckets: prometheus.DefBuckets,
		}, []string{"operation"}),
		SnowCallerResolutionTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "noc_snow_caller_resolution_total",
			Help: "Total ServiceNow caller resolutions by outcome status.",
		}, []string{"status"}),
		SnowCircuitBreakerState: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "noc_snow_circuit_breaker_state",
			Help: "ServiceNow circuit breaker state (0=closed, 1=open, 2=half_open).",
		}),
	}

	reg.MustRegister(
		m.HTTPRequestsTotal,
		m.HTTPRequestDurationSeconds,
		m.AlertmanagerWebhooksTotal,
		m.AlertmanagerAlertsProcessed,
		m.AlertmanagerTruncatedAlerts,
		m.AlertsStateCurrent,
		m.AlertsResolvedOpenTicket,
		m.AlertsActiveClosedTicket,
		m.SnowRequestsTotal,
		m.SnowRequestDurationSeconds,
		m.SnowCallerResolutionTotal,
		m.SnowCircuitBreakerState,
	)

	// Initialize label combinations so metrics exist immediately on /metrics.
	for _, state := range []string{"firing", "acknowledged", "dismissed"} {
		m.AlertsStateCurrent.WithLabelValues(state).Set(0)
	}
	for _, action := range []string{"inserted", "updated", "resolved", "ignored"} {
		m.AlertmanagerAlertsProcessed.WithLabelValues(action).Add(0)
	}
	for _, status := range []string{"sso_matched", "generic_noc_selected", "fallback_noc"} {
		m.SnowCallerResolutionTotal.WithLabelValues(status).Add(0)
	}
	m.SnowCircuitBreakerState.Set(0)

	return m
}

// ObserveHTTP records HTTP request count and duration.
func (m *Metrics) ObserveHTTP(method, route string, statusCode int, durationSeconds float64) {
	m.HTTPRequestsTotal.WithLabelValues(method, route, strconv.Itoa(statusCode)).Inc()
	m.HTTPRequestDurationSeconds.WithLabelValues(method, route).Observe(durationSeconds)
}

// RegisterPoolCollector registers a live collector reading pgxpool.Stat().
func (m *Metrics) RegisterPoolCollector(pool *pgxpool.Pool) {
	m.Registry.MustRegister(&pgxPoolCollector{
		pool: pool,
		acquiredDesc: prometheus.NewDesc(
			"noc_db_pool_acquired_conns",
			"Number of currently acquired connections in the PostgreSQL pool.",
			nil, nil,
		),
		idleDesc: prometheus.NewDesc(
			"noc_db_pool_idle_conns",
			"Number of currently idle connections in the PostgreSQL pool.",
			nil, nil,
		),
	})
}

type pgxPoolCollector struct {
	pool         *pgxpool.Pool
	acquiredDesc *prometheus.Desc
	idleDesc     *prometheus.Desc
}

func (c *pgxPoolCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.acquiredDesc
	ch <- c.idleDesc
}

func (c *pgxPoolCollector) Collect(ch chan<- prometheus.Metric) {
	if c.pool == nil {
		return
	}
	stat := c.pool.Stat()
	ch <- prometheus.MustNewConstMetric(c.acquiredDesc, prometheus.GaugeValue, float64(stat.AcquiredConns()))
	ch <- prometheus.MustNewConstMetric(c.idleDesc, prometheus.GaugeValue, float64(stat.IdleConns()))
}
