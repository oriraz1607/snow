package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
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

type resolutionNoteStore interface {
	FetchPendingResolutionNotes(ctx context.Context, limit int) ([]model.PendingResolutionNote, error)
	MarkResolutionNotesPostedBatch(ctx context.Context, items []model.PendingResolutionNote, noteText, reason string) error
}

type resolutionNotePoster interface {
	PostWorkNote(ctx context.Context, snowSysID, workNotes string) error
}

// PrematureClosureHandler reconciles ServiceNow incidents prematurely moved to Resolved (6).
type PrematureClosureHandler interface {
	HandlePrematureResolvedState(ctx context.Context, snowSysID, triggerSource string)
}

// BackgroundWorkers coordinates the PostgreSQL-advisory-locked drift poller,
// async resolution-note worker, cache cleanup, and Prometheus gauge updates.
type BackgroundWorkers struct {
	pool       *pgxpool.Pool
	repo       *store.ServiceNowRepository
	snowClient *servicenow.Client
	reopener   PrematureClosureHandler
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
	reopener PrematureClosureHandler,
	cfg *config.Config,
	metrics *observability.Metrics,
	logger *slog.Logger,
) *BackgroundWorkers {
	return &BackgroundWorkers{
		pool:       pool,
		repo:       repo,
		snowClient: snowClient,
		reopener:   reopener,
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
				updated, err := w.repo.UpdateIncidentsBySysID(ctx, rec.SysID, rec.ToSyncUpdate(), model.ActorSystem, "poller", true)
				if err != nil {
					if w.logger != nil {
						w.logger.Warn("poller failed to update incident",
							slog.String("component", "drift_poller"),
							slog.String("snow_sys_id", rec.SysID),
							slog.String("error", err.Error()),
						)
					}
					continue
				}
				if updated > 0 && rec.State == 6 && w.reopener != nil {
					w.reopener.HandlePrematureResolvedState(ctx, rec.SysID, "poller")
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
		return processPendingResolutionNotes(ctx, w.repo, w.snowClient, w.logger)
	})
	if err != nil && ctx.Err() == nil && w.logger != nil {
		w.logger.Warn("resolution note worker cycle failed",
			slog.String("component", "resolution_note_worker"),
			slog.String("error", err.Error()),
		)
	}
}

func processPendingResolutionNotes(
	ctx context.Context,
	noteStore resolutionNoteStore,
	poster resolutionNotePoster,
	logger *slog.Logger,
) error {
	pending, err := noteStore.FetchPendingResolutionNotes(ctx, 50)
	if err != nil {
		return err
	}

	readyOrder := make([]string, 0)
	readyBySysID := make(map[string][]model.PendingResolutionNote)

	for _, item := range pending {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if item.RemainingActiveSiblings > 0 {
			note := formatPartialResolutionNote(item)
			if err := poster.PostWorkNote(ctx, item.SnowSysID, note); err != nil {
				if logger != nil {
					logger.Warn("async partial resolution note post failed",
						slog.String("component", "resolution_note_worker"),
						slog.String("alert_id", item.AlertID),
						slog.String("incident_number", item.IncidentNumber),
						slog.String("error", err.Error()),
					)
				}
				continue
			}
			if err := noteStore.MarkResolutionNotesPostedBatch(ctx, []model.PendingResolutionNote{item}, note, "alert_resolved_siblings_active"); err != nil && logger != nil {
				logger.Warn("failed marking partial resolution_note_posted_at",
					slog.String("component", "resolution_note_worker"),
					slog.String("alert_id", item.AlertID),
					slog.String("error", err.Error()),
				)
			}
			continue
		}

		if _, exists := readyBySysID[item.SnowSysID]; !exists {
			readyOrder = append(readyOrder, item.SnowSysID)
		}
		readyBySysID[item.SnowSysID] = append(readyBySysID[item.SnowSysID], item)
	}

	for _, sysID := range readyOrder {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		grp := readyBySysID[sysID]
		note := formatFinalResolutionNote(grp)
		if err := poster.PostWorkNote(ctx, sysID, note); err != nil {
			if logger != nil {
				logger.Warn("async resolution note post failed",
					slog.String("component", "resolution_note_worker"),
					slog.String("snow_sys_id", sysID),
					slog.String("incident_number", grp[0].IncidentNumber),
					slog.String("error", err.Error()),
				)
			}
			continue
		}
		if err := noteStore.MarkResolutionNotesPostedBatch(ctx, grp, note, "alert_resolved_open_ticket"); err != nil && logger != nil {
			logger.Warn("failed marking resolution_note_posted_at",
				slog.String("component", "resolution_note_worker"),
				slog.String("snow_sys_id", sysID),
				slog.String("error", err.Error()),
			)
		}
	}
	return nil
}

func formatPartialResolutionNote(item model.PendingResolutionNote) string {
	return fmt.Sprintf(
		"[Grafana NOC] Prometheus Alertmanager reported linked alert %s RESOLVED at %s. %d other linked alert(s) on this incident remain active — keep this incident open until all linked alerts recover.",
		item.AlertName,
		item.ResolvedAt.UTC().Format(time.RFC3339),
		item.RemainingActiveSiblings,
	)
}

func formatFinalResolutionNote(items []model.PendingResolutionNote) string {
	first := items[0]
	if first.TotalLinkedAlerts > 1 || len(items) > 1 {
		names := make([]string, 0, len(items))
		seen := make(map[string]struct{}, len(items))
		latestResolved := first.ResolvedAt
		for _, it := range items {
			if _, ok := seen[it.AlertName]; !ok {
				seen[it.AlertName] = struct{}{}
				names = append(names, it.AlertName)
			}
			if it.ResolvedAt.After(latestResolved) {
				latestResolved = it.ResolvedAt
			}
		}
		total := first.TotalLinkedAlerts
		if total < len(items) {
			total = len(items)
		}
		return fmt.Sprintf(
			"[Grafana NOC] All %d linked Prometheus Alertmanager alerts (including %s) have now RESOLVED as of %s. This incident is still open in ServiceNow — please verify recovery and resolve/close the ticket.",
			total,
			strings.Join(names, ", "),
			latestResolved.UTC().Format(time.RFC3339),
		)
	}
	return fmt.Sprintf(
		"[Grafana NOC] Prometheus Alertmanager reported alert %s RESOLVED at %s. This incident is still open in ServiceNow — please verify recovery and resolve/close the ticket.",
		first.AlertName,
		first.ResolvedAt.UTC().Format(time.RFC3339),
	)
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
				if err := w.repo.PurgeExpiredUserCache(ctx); err != nil {
					return err
				}
				return w.repo.PurgeExpiredIdempotencyKeys(ctx)
			})
			if err != nil && ctx.Err() == nil && w.logger != nil {
				w.logger.Warn("maintenance cleanup failed",
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
