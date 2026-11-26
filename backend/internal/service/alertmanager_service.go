package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/oriraz/grafana-snow-integration/backend/internal/apierror"
	"github.com/oriraz/grafana-snow-integration/backend/internal/model"
	"github.com/oriraz/grafana-snow-integration/backend/internal/observability"
	"github.com/oriraz/grafana-snow-integration/backend/internal/store"
)

// AlertmanagerStore abstracts PostgreSQL batch reconciliation and webhook logging.
type AlertmanagerStore interface {
	ReconcileAlertmanagerBatch(ctx context.Context, logger *slog.Logger, alerts []model.NormalizedAlert) (store.ReconcileOutcome, error)
}

// ResolutionNoteNotifier signals the background worker when a resolved alert has an open ticket.
type ResolutionNoteNotifier interface {
	Trigger()
}

// AlertmanagerService validates, deduplicates, sorts, and reconciles Alertmanager v4 payloads.
type AlertmanagerService struct {
	repo         AlertmanagerStore
	logWriter    func(context.Context, model.WebhookIngestionLogEntry) error
	noteNotifier ResolutionNoteNotifier
	metrics      *observability.Metrics
	logger       *slog.Logger
}

// NewAlertmanagerService constructs an AlertmanagerService.
func NewAlertmanagerService(
	repo AlertmanagerStore,
	logWriter func(context.Context, model.WebhookIngestionLogEntry) error,
	noteNotifier ResolutionNoteNotifier,
	metrics *observability.Metrics,
	logger *slog.Logger,
) *AlertmanagerService {
	return &AlertmanagerService{
		repo:         repo,
		logWriter:    logWriter,
		noteNotifier: noteNotifier,
		metrics:      metrics,
		logger:       logger,
	}
}

// Ingest validates and reconciles a raw Alertmanager v4 JSON webhook payload without touching ServiceNow synchronously.
func (s *AlertmanagerService) Ingest(ctx context.Context, rawBody []byte) (*model.AlertmanagerWebhookResponse, error) {
	started := time.Now()
	hash := sha256Hex(rawBody)

	var payload model.AlertmanagerWebhookPayload
	if err := json.Unmarshal(rawBody, &payload); err != nil {
		errMsg := "malformed JSON payload"
		s.recordWebhookOutcome(ctx, "", hash, 0, 0, http.StatusBadRequest, started, &errMsg)
		return nil, apierror.New(http.StatusBadRequest, apierror.CodeInvalidRequest, "Malformed Alertmanager JSON payload.").WithCause(err)
	}

	normalized, err := ValidateAndNormalizeAlertmanagerPayload(&payload)
	if err != nil {
		errMsg := err.Error()
		s.recordWebhookOutcome(ctx, payload.GroupKey, hash, len(payload.Alerts), payload.TruncatedAlerts, http.StatusBadRequest, started, &errMsg)
		return nil, err
	}

	if payload.TruncatedAlerts > 0 {
		if s.logger != nil {
			s.logger.Warn("alertmanager webhook reported truncatedAlerts > 0; check max_alerts setting",
				slog.String("component", "alertmanager_ingestion"),
				slog.Int("truncated_alerts", payload.TruncatedAlerts),
				slog.String("group_key", payload.GroupKey),
			)
		}
		if s.metrics != nil {
			s.metrics.AlertmanagerTruncatedAlerts.Add(float64(payload.TruncatedAlerts))
		}
	}

	outcome, err := s.repo.ReconcileAlertmanagerBatch(ctx, s.logger, normalized)
	if err != nil {
		errMsg := err.Error()
		s.recordWebhookOutcome(ctx, payload.GroupKey, hash, len(payload.Alerts), payload.TruncatedAlerts, http.StatusServiceUnavailable, started, &errMsg)
		return nil, err
	}

	if s.metrics != nil {
		s.metrics.AlertmanagerAlertsProcessed.WithLabelValues("inserted").Add(float64(outcome.Inserted))
		s.metrics.AlertmanagerAlertsProcessed.WithLabelValues("updated").Add(float64(outcome.Updated))
		s.metrics.AlertmanagerAlertsProcessed.WithLabelValues("resolved").Add(float64(outcome.Resolved))
		s.metrics.AlertmanagerAlertsProcessed.WithLabelValues("ignored").Add(float64(outcome.Ignored))
	}

	s.recordWebhookOutcome(ctx, payload.GroupKey, hash, len(payload.Alerts), payload.TruncatedAlerts, http.StatusOK, started, nil)

	if outcome.HasOpenTicketResolved && s.noteNotifier != nil {
		s.noteNotifier.Trigger()
	}

	return &model.AlertmanagerWebhookResponse{
		Status:                 "ok",
		Processed:              outcome.Processed,
		Inserted:               outcome.Inserted,
		Updated:                outcome.Updated,
		Resolved:               outcome.Resolved,
		Ignored:                outcome.Ignored,
		TruncatedAlertsWarning: payload.TruncatedAlerts > 0,
	}, nil
}

// ValidateAndNormalizeAlertmanagerPayload enforces the Alertmanager v4 schema,
// deduplicates identical (fingerprint, startsAt) pairs (resolved wins), and sorts deterministically.
func ValidateAndNormalizeAlertmanagerPayload(payload *model.AlertmanagerWebhookPayload) ([]model.NormalizedAlert, error) {
	if payload == nil {
		return nil, apierror.New(http.StatusBadRequest, apierror.CodeInvalidRequest, "Empty Alertmanager payload.")
	}
	if strings.TrimSpace(payload.Version) != "4" {
		return nil, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "Unsupported Alertmanager webhook version; expected version \"4\".")
	}
	if strings.TrimSpace(payload.GroupKey) == "" {
		return nil, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "groupKey is required.")
	}
	if strings.TrimSpace(payload.Receiver) == "" {
		return nil, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "receiver is required.")
	}
	if payload.TruncatedAlerts < 0 {
		return nil, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "truncatedAlerts cannot be negative.")
	}

	extURL := model.StringPtrOrNil(payload.ExternalURL)

	type dedupKey struct {
		fp       string
		startsAt int64
	}
	deduped := make(map[dedupKey]model.NormalizedAlert, len(payload.Alerts))

	for i, raw := range payload.Alerts {
		fp := strings.TrimSpace(raw.Fingerprint)
		if fp == "" || len(fp) > 64 {
			return nil, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "Each alert requires a valid non-empty fingerprint (max 64 chars).")
		}
		if raw.StartsAt.IsZero() {
			return nil, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "Each alert requires a valid startsAt timestamp.")
		}
		if raw.Labels == nil {
			return nil, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "Each alert requires a labels object.")
		}

		statusStr := strings.ToLower(strings.TrimSpace(raw.Status))
		var status model.AlertState
		switch statusStr {
		case string(model.AlertStateFiring):
			status = model.AlertStateFiring
		case string(model.AlertStateResolved):
			status = model.AlertStateResolved
		default:
			return nil, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "Each alert status must be \"firing\" or \"resolved\".")
		}

		var endsAt *time.Time
		if !raw.EndsAt.IsZero() && raw.EndsAt.Year() > 1 {
			t := raw.EndsAt.UTC()
			endsAt = &t
		}

		labels := model.NormalizeStringMap(raw.Labels)
		annotations := model.NormalizeStringMap(raw.Annotations)

		alertName := strings.TrimSpace(labels["alertname"])
		if alertName == "" {
			alertName = "UnnamedAlert"
		}
		instance := model.ExtractInstance(labels)
		severity := NormalizeSeverity(labels["severity"])

		norm := model.NormalizedAlert{
			Fingerprint:  fp,
			StartsAt:     raw.StartsAt.UTC(),
			EndsAt:       endsAt,
			Status:       status,
			AlertName:    alertName,
			Instance:     instance,
			Severity:     severity,
			GroupKey:     payload.GroupKey,
			Receiver:     payload.Receiver,
			Labels:       labels,
			Annotations:  annotations,
			GeneratorURL: model.StringPtrOrNil(raw.GeneratorURL),
			ExternalURL:  extURL,
		}
		_ = i

		key := dedupKey{fp: fp, startsAt: norm.StartsAt.UnixNano()}
		if existing, exists := deduped[key]; exists {
			// If a single malformed batch has both firing and resolved for the same episode key, resolved wins.
			if existing.Status == model.AlertStateResolved && norm.Status != model.AlertStateResolved {
				continue
			}
		}
		deduped[key] = norm
	}

	sorted := make([]model.NormalizedAlert, 0, len(deduped))
	for _, item := range deduped {
		sorted = append(sorted, item)
	}
	slices.SortFunc(sorted, func(a, b model.NormalizedAlert) int {
		if c := strings.Compare(a.Fingerprint, b.Fingerprint); c != 0 {
			return c
		}
		return a.StartsAt.Compare(b.StartsAt)
	})
	return sorted, nil
}

func (s *AlertmanagerService) recordWebhookOutcome(
	ctx context.Context,
	groupKey, hash string,
	alertCount, truncated, status int,
	started time.Time,
	errMsg *string,
) {
	if s.metrics != nil {
		s.metrics.AlertmanagerWebhooksTotal.WithLabelValues(statusLabel(status)).Inc()
	}
	if s.logWriter != nil {
		_ = s.logWriter(ctx, model.WebhookIngestionLogEntry{
			Source:          "alertmanager",
			GroupKey:        model.StringPtrOrNil(groupKey),
			PayloadHash:     hash,
			AlertCount:      alertCount,
			TruncatedAlerts: truncated,
			HTTPStatus:      status,
			ProcessingMs:    int(time.Since(started).Milliseconds()),
			ErrorMessage:    errMsg,
		})
	}
}

func statusLabel(status int) string {
	switch status {
	case http.StatusOK:
		return "200"
	case http.StatusBadRequest:
		return "400"
	case http.StatusUnauthorized:
		return "401"
	case http.StatusServiceUnavailable:
		return "503"
	default:
		return "500"
	}
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// NormalizeSeverity normalizes common Prometheus/Alertmanager severity aliases into canonical
// alert severity values ("critical", "warning", etc.) while preserving raw labels["severity"].
func NormalizeSeverity(raw string) string {
	norm := strings.ToLower(strings.TrimSpace(raw))
	if norm == "" {
		return "unknown"
	}
	switch norm {
	case "critical", "crit", "p1", "sev1", "s1", "fatal", "emergency", "emerg",
		"immediate", "disaster", "high", "major", "error", "err", "page", "blocker":
		return "critical"
	case "warning", "warn", "p2", "sev2", "s2", "medium", "med", "moderate", "minor":
		return "warning"
	default:
		if len(norm) > 32 {
			norm = norm[:32]
		}
		return norm
	}
}
