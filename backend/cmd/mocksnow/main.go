package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/oriraz/grafana-snow-integration/backend/internal/mocksnow"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	port := os.Getenv("MOCKSNOW_PORT")
	if port == "" {
		port = "8085"
	}

	srv := mocksnow.NewServer(mocksnow.Config{
		ClientID:              os.Getenv("SNOW_CLIENT_ID"),
		ClientSecret:          os.Getenv("SNOW_CLIENT_SECRET"),
		Username:              os.Getenv("SNOW_USERNAME"),
		Password:              os.Getenv("SNOW_PASSWORD"),
		DefaultCallerSysID:    os.Getenv("SNOW_DEFAULT_NOC_CALLER_SYS_ID"),
		DefaultCallerUsername: os.Getenv("SNOW_DEFAULT_NOC_CALLER_USERNAME"),
		WebhookURL:            os.Getenv("MOCKSNOW_WEBHOOK_URL"),
		WebhookSecret:         os.Getenv("SNOW_INBOUND_WEBHOOK_SECRET"),
	})

	httpSrv := &http.Server{
		Addr:              ":" + port,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("mocksnow server listening", slog.String("addr", httpSrv.Addr))
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("mocksnow listener exited", slog.String("error", err.Error()))
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
}
