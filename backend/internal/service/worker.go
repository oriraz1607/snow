package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/oriraz/grafana-snow-integration/backend/internal/config"
	"github.com/oriraz/grafana-snow-integration/backend/internal/model"
	"github.com/oriraz/grafana-snow-integration/backend/internal/observability"
	"github.com/oriraz/grafana-snow-integration/backend/internal/servicenow"
	"github.com/oriraz/grafana-snow-integration/backend/internal/store"
)

const (
	pollerAdvisoryLockID      int64 = 728394101
	noteWorkerAdvisoryLockID  int64 = 728394102
	maintenanceAdvisoryLockID int64 = 728394103
)

// BackgroundWorkers coordinates the PostgreSQL-advisory-locked drift poller,
// async resolution-note worker, cache cleanup, and Prometheus gauge updates.
type BackgroundWorkers struct {
	pool       *pgxpool.Pool
	repo       *store.ServiceNowRepository
	snowClient *servicenow.Client
	cfg        *config.Config
	metrics    *observability.Metrics
	logger     *slog.Logger

	noteSignal chan struct{}
}

// NewBackgroundWorkers constructs BackgroundWorkers.
func NewBackgroundWorkers(
	pool *pgxpool.Pool,
	repo *store.ServiceNowRepository,
	snowClient *servicenow.Client,
	cfg *config.Config,
	metrics *observability.Metrics,
	logger *slog.Logger,
) *BackgroundWorkers {
	return &BackgroundWorkers{
		pool:       pool,
		repo:       repo,
		snowClient: snowClient,
		cfg:        cfg,
		metrics:    metrics,
		logger:     logger,
		noteSignal: make(chan struct{}, 1),
	}
}

// Trigger requests an immediate non-blocking pass of the resolution-note worker.
func (w *BackgroundWorkers) Trigger() {
	select {
	case w.noteSignal <- struct{}{}:
	default:
	}
}

// Start runs all background loops until ctx is cancelled.
func (w *BackgroundWorkers) Start(ctx context.Context) {
	go w.runPollLoop(ctx)
	go w.runNoteWorkerLoop(ctx)
	go w.runMaintenanceLoop(ctx)
}

func (w *BackgroundWorkers) runPollLoop(ctx context.Context) {
	w.refreshGauges(ctx)

	ticker := time.NewTicker(w.cfg.SnowPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.runPollOnce(ctx)
		}
	}
}

func (w *BackgroundWorkers) runPollOnce(ctx context.Context) {
	if w.snowClient.CircuitState() == servicenow.BreakerOpen {
		w.refreshGauges(ctx)
		return
	}

	_, err := store.WithAdvisoryLock(ctx, w.pool, pollerAdvisoryLockID, func(ctx context.Context) error {
		sysIDs, err := w.repo.FetchOpenIncidentSysIDsForPoll(ctx, 50)
		if err != nil {
			return err
		}
		if len(sysIDs) > 0 {
			records, err := w.snowClient.GetIncidentsBySysIDs(ctx, "poll", sysIDs)
			if err != nil {
				return err
			}
			for _, rec := range records {
				if _, err := w.repo.UpdateIncidentsBySysID(ctx, rec.SysID, rec.ToSyncUpdate(), model.ActorSystem, "poller", true); err != nil {
					if w.logger != nil {
						w.logger.Warn("poller failed to update incident",
							slog.String("component", "drift_poller"),
							slog.String("snow_sys_id", rec.SysID),
							slog.String("error", err.Error()),
						)
					}
				}
			}
		}
		w.refreshGauges(ctx)
		return nil
	})
	if err != nil && ctx.Err() == nil && w.logger != nil {
		w.logger.Warn("drift poller cycle failed",
			slog.String("component", "drift_poller"),
			slog.String("error", err.Error()),
		)
	}
}

func (w *BackgroundWorkers) runNoteWorkerLoop(ctx context.Context) {
	ticker := time.NewTicker(w.cfg.SnowNoteWorkerInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-w.noteSignal:
			w.runNoteWorkerOnce(ctx)
		case <-ticker.C:
			w.runNoteWorkerOnce(ctx)
		}
	}
}

func (w *BackgroundWorkers) runNoteWorkerOnce(ctx context.Context) {
	if !w.cfg.SnowAutoPostResolvedNote || w.snowClient.CircuitState() == servicenow.BreakerOpen {
		return
	}

	_, err := store.WithAdvisoryLock(ctx, w.pool, noteWorkerAdvisoryLockID, func(ctx context.Context) error {
		pending, err := w.repo.FetchPendingResolutionNotes(ctx, 50)
		if err != nil {
			return err
		}
		for _, item := range pending {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			note := fmt.Sprintf(
				"[Grafana NOC] Prometheus Alertmanager reported alert %s RESOLVED at %s. This incident is still open in ServiceNow — please verify recovery and resolve/close the ticket.",
				item.AlertName,
				item.ResolvedAt.UTC().Format(time.RFC3339),
			)
			if err := w.snowClient.PostWorkNote(ctx, item.SnowSysID, note); err != nil {
				if w.logger != nil {
					w.logger.Warn("async resolution note post failed",
						slog.String("component", "resolution_note_worker"),
						slog.String("alert_id", item.AlertID),
						slog.String("incident_number", item.IncidentNumber),
						slog.String("error", err.Error()),
					)
				}
				continue
			}
			if err := w.repo.MarkResolutionNotePosted(ctx, item.IncidentID, item.AlertID, note); err != nil && w.logger != nil {
				w.logger.Warn("failed marking resolution_note_posted_at",
					slog.String("component", "resolution_note_worker"),
					slog.String("alert_id", item.AlertID),
					slog.String("error", err.Error()),
				)
			}
		}
		return nil
	})
	if err != nil && ctx.Err() == nil && w.logger != nil {
		w.logger.Warn("resolution note worker cycle failed",
			slog.String("component", "resolution_note_worker"),
			slog.String("error", err.Error()),
		)
	}
}

func (w *BackgroundWorkers) runMaintenanceLoop(ctx context.Context) {
	gaugeTicker := time.NewTicker(15 * time.Second)
	cleanupTicker := time.NewTicker(1 * time.Hour)
	defer gaugeTicker.Stop()
	defer cleanupTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-gaugeTicker.C:
			w.refreshGauges(ctx)
		case <-cleanupTicker.C:
			_, err := store.WithAdvisoryLock(ctx, w.pool, maintenanceAdvisoryLockID, func(ctx context.Context) error {
				return w.repo.PurgeExpiredUserCache(ctx)
			})
			if err != nil && ctx.Err() == nil && w.logger != nil {
				w.logger.Warn("user cache cleanup failed",
					slog.String("component", "maintenance_worker"),
					slog.String("error", err.Error()),
				)
			}
		}
	}
}

func (w *BackgroundWorkers) refreshGauges(ctx context.Context) {
	if w.metrics == nil {
		return
	}
	counts, err := w.repo.FetchGaugeCounts(ctx)
	if err != nil {
		return
	}
	w.metrics.AlertsStateCurrent.WithLabelValues("firing").Set(float64(counts.Firing))
	w.metrics.AlertsStateCurrent.WithLabelValues("acknowledged").Set(float64(counts.Acknowledged))
	w.metrics.AlertsStateCurrent.WithLabelValues("dismissed").Set(float64(counts.Dismissed))
	w.metrics.AlertsResolvedOpenTicket.Set(float64(counts.ResolvedWithOpenTicket))
	w.metrics.AlertsActiveClosedTicket.Set(float64(counts.ActiveWithClosedTicket))
}
