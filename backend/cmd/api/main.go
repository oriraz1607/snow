package main

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/oriraz/grafana-snow-integration/backend/internal/config"
	"github.com/oriraz/grafana-snow-integration/backend/internal/httpapi"
	"github.com/oriraz/grafana-snow-integration/backend/internal/model"
	"github.com/oriraz/grafana-snow-integration/backend/internal/observability"
	"github.com/oriraz/grafana-snow-integration/backend/internal/service"
	"github.com/oriraz/grafana-snow-integration/backend/internal/servicenow"
	"github.com/oriraz/grafana-snow-integration/backend/internal/store"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).Error("configuration error", slog.String("error", err.Error()))
		os.Exit(1)
	}

	logger := observability.NewLogger(cfg.LogLevel)
	metrics := observability.NewMetrics()
	logger.Info("starting alert-management api", slog.Any("config", *cfg))

	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := store.NewPool(rootCtx, cfg.DatabaseURL, cfg.PGMaxConns, cfg.PGMinConns)
	if err != nil {
		logger.Error("failed initializing postgresql pool", slog.String("component", "startup"), slog.String("error", err.Error()))
		os.Exit(1)
	}
	defer pool.Close()
	metrics.RegisterPoolCollector(pool)

	var authProvider servicenow.AuthProvider
	if cfg.SnowAuthMode == config.SnowAuthBasic {
		authProvider = servicenow.NewBasicAuthProvider(cfg.SnowUsername, cfg.SnowPassword)
	} else {
		authProvider = servicenow.NewOAuthTokenProvider(
			cfg.SnowTokenURL,
			cfg.SnowClientID,
			cfg.SnowClientSecret,
			cfg.SnowOAuthScope,
			&http.Client{Timeout: cfg.SnowHTTPTimeout},
		)
	}

	breaker := servicenow.NewCircuitBreaker(func(st servicenow.BreakerState) {
		metrics.SnowCircuitBreakerState.Set(float64(st))
		logger.Warn("servicenow circuit breaker transitioned", slog.String("component", "servicenow_client"), slog.Int("state", int(st)))
	})
	snowClient := servicenow.NewClient(cfg.SnowInstanceURL, cfg.SnowHTTPTimeout, authProvider, breaker, metrics)

	alertRepo := store.NewAlertRepository(pool)
	alertRepo.SetConfluenceAllowedHosts(cfg.ConfluenceAllowedHosts)
	snowRepo := store.NewServiceNowRepository(pool)
	callerResolver := servicenow.NewCallerResolver(snowRepo, snowClient, cfg.DefaultCallerSysID, metrics, logger)

	logWriter := func(ctx context.Context, entry model.WebhookIngestionLogEntry) error {
		return store.InsertWebhookIngestionLog(ctx, pool, entry)
	}

	incSvc := service.NewIncidentService(snowRepo, snowClient, callerResolver, cfg, logWriter, logger)
	confSvc := service.NewConfluenceService(alertRepo, cfg.ConfluenceAllowedHosts)
	workers := service.NewBackgroundWorkers(pool, snowRepo, snowClient, incSvc, cfg, metrics, logger)
	workers.Start(rootCtx)

	amSvc := service.NewAlertmanagerService(alertRepo, logWriter, workers, metrics, logger)

	var shuttingDown atomic.Bool

	alertsHandler := httpapi.NewAlertsHandler(alertRepo, confSvc)
	snowHandler := httpapi.NewServiceNowHandler(incSvc)
	webhooksHandler := httpapi.NewWebhooksHandler(amSvc, incSvc, cfg.AlertmanagerMaxPayloadBytes, &shuttingDown)
	healthHandler := httpapi.NewHealthHandler(pool)

	apiSrv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           httpapi.NewAPIRouter(cfg, logger, metrics, alertsHandler, snowHandler, webhooksHandler),
		ReadHeaderTimeout: 10 * time.Second,
	}
	if cfg.TLSCertFile != "" && cfg.TLSKeyFile != "" {
		apiSrv.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	adminSrv := &http.Server{
		Addr:              ":" + cfg.AdminPort,
		Handler:           httpapi.NewAdminRouter(healthHandler, metrics),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 2)
	go func() {
		tlsEnabled := cfg.TLSCertFile != "" && cfg.TLSKeyFile != ""
		logger.Info("api listener started", slog.String("component", "http_server"), slog.String("addr", apiSrv.Addr), slog.Bool("tls", tlsEnabled))
		var listenErr error
		if tlsEnabled {
			listenErr = apiSrv.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile)
		} else {
			listenErr = apiSrv.ListenAndServe()
		}
		if listenErr != nil && !errors.Is(listenErr, http.ErrServerClosed) {
			errCh <- listenErr
		}
	}()
	go func() {
		logger.Info("admin listener started", slog.String("component", "admin_server"), slog.String("addr", adminSrv.Addr))
		if err := adminSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-rootCtx.Done():
		logger.Info("shutdown signal received", slog.String("component", "shutdown"))
	case srvErr := <-errCh:
		logger.Error("http listener exited unexpectedly", slog.String("component", "http_server"), slog.String("error", srvErr.Error()))
	}

	shuttingDown.Store(true)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = apiSrv.Shutdown(shutdownCtx)
	_ = adminSrv.Shutdown(shutdownCtx)
}
