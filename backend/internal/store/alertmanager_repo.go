package store

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/oriraz/grafana-snow-integration/backend/internal/apierror"
	"github.com/oriraz/grafana-snow-integration/backend/internal/model"
)

const alertmanagerSubBatchSize = 100

// ReconcileOutcome summarizes the aggregate reconciliation counts across a webhook payload.
type ReconcileOutcome struct {
	Processed             int
	Inserted              int
	Updated               int
	Resolved              int
	Ignored               int
	HasOpenTicketResolved bool
}

// ReconcileAlertmanagerBatch reconciles a deterministically sorted slice of Alertmanager alerts
// in bounded sub-batches of 100 alerts inside PostgreSQL transactions.
func (r *AlertRepository) ReconcileAlertmanagerBatch(ctx context.Context, logger *slog.Logger, alerts []model.NormalizedAlert) (ReconcileOutcome, error) {
	var total ReconcileOutcome
	for start := 0; start < len(alerts); start += alertmanagerSubBatchSize {
		end := start + alertmanagerSubBatchSize
		if end > len(alerts) {
			end = len(alerts)
		}
		chunk := alerts[start:end]

		var chunkOut ReconcileOutcome
		err := r.withTx(ctx, func(tx pgx.Tx) error {
			for _, incoming := range chunk {
				out, err := reconcileOneAlert(ctx, tx, logger, incoming)
				if err != nil {
					return err
				}
				chunkOut.Processed += out.Processed
				chunkOut.Inserted += out.Inserted
				chunkOut.Updated += out.Updated
				chunkOut.Resolved += out.Resolved
				chunkOut.Ignored += out.Ignored
				if out.HasOpenTicketResolved {
					chunkOut.HasOpenTicketResolved = true
				}
			}
			return nil
		})
		if err != nil {
			return total, err
		}

		total.Processed += chunkOut.Processed
		total.Inserted += chunkOut.Inserted
		total.Updated += chunkOut.Updated
		total.Resolved += chunkOut.Resolved
		total.Ignored += chunkOut.Ignored
		if chunkOut.HasOpenTicketResolved {
			total.HasOpenTicketResolved = true
		}
	}
	return total, nil
}

func reconcileOneAlert(ctx context.Context, tx pgx.Tx, logger *slog.Logger, incoming model.NormalizedAlert) (ReconcileOutcome, error) {
	var out ReconcileOutcome
	out.Processed = 1

	labelsJSON, err := json.Marshal(model.NormalizeStringMap(incoming.Labels))
	if err != nil {
		return out, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "Unable to encode alert labels.").WithCause(err)
	}
	annJSON, err := json.Marshal(model.NormalizeStringMap(incoming.Annotations))
	if err != nil {
		return out, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "Unable to encode alert annotations.").WithCause(err)
	}

	var exactID string
	var exactStateStr string
	var exactLabelsRaw, exactAnnRaw []byte
	err = tx.QueryRow(ctx, `
		SELECT id::text, state::text, labels, annotations
		FROM alerts
		WHERE fingerprint = $1 AND starts_at = $2
		FOR UPDATE
	`, incoming.Fingerprint, incoming.StartsAt).Scan(&exactID, &exactStateStr, &exactLabelsRaw, &exactAnnRaw)

	if err == nil {
		exactState := model.AlertState(exactStateStr)
		if incoming.Status == model.AlertStateFiring {
			switch exactState {
			case model.AlertStateFiring, model.AlertStateAcknowledged, model.AlertStateDismissed:
				oldLabels := unmarshalStringMap(exactLabelsRaw)
				oldAnn := unmarshalStringMap(exactAnnRaw)
				changed := !maps.Equal(oldLabels, incoming.Labels) || !maps.Equal(oldAnn, incoming.Annotations)

				if _, err := tx.Exec(ctx, `
					UPDATE alerts
					SET labels = $2::jsonb,
						annotations = $3::jsonb,
						alert_name = $4,
						instance = $5,
						severity = $6,
						group_key = $7,
						receiver = $8,
						generator_url = $9,
						external_url = $10,
						last_received_at = NOW(),
						delivery_count = delivery_count + 1
					WHERE id = $1::uuid
				`, exactID, string(labelsJSON), string(annJSON), incoming.AlertName, incoming.Instance, incoming.Severity,
					incoming.GroupKey, incoming.Receiver, incoming.GeneratorURL, incoming.ExternalURL); err != nil {
					return out, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to update alert episode.").WithCause(err)
				}

				if changed && exactState != model.AlertStateDismissed {
					if err := insertAuditLogTx(ctx, tx, auditEntry{
						AlertID:       exactID,
						EventType:     model.AuditAlertUpdated,
						ActorType:     model.ActorAlertmanager,
						ActorLogin:    "alertmanager",
						PreviousState: &exactState,
						NewState:      &exactState,
						Metadata:      map[string]any{"group_key": incoming.GroupKey},
					}); err != nil {
						return out, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to log alert update.").WithCause(err)
					}
				}
				out.Updated = 1
				return out, nil

			case model.AlertStateResolved:
				// Out-of-order delayed firing delivery for an already resolved episode.
				out.Ignored = 1
				return out, nil
			}
		}

		// incoming.Status == resolved for exact episode match
		if exactState == model.AlertStateResolved {
			if _, err := tx.Exec(ctx, `UPDATE alerts SET last_received_at = NOW() WHERE id = $1::uuid`, exactID); err != nil {
				return out, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to refresh resolved alert.").WithCause(err)
			}
			out.Ignored = 1
			return out, nil
		}

		resolvedAt := time.Now().UTC()
		if incoming.EndsAt != nil {
			resolvedAt = *incoming.EndsAt
		}
		if _, err := tx.Exec(ctx, `
			UPDATE alerts
			SET state = 'resolved',
				ends_at = COALESCE($2, ends_at),
				resolved_at = $3,
				version = version + 1,
				last_received_at = NOW(),
				delivery_count = delivery_count + 1
			WHERE id = $1::uuid
		`, exactID, incoming.EndsAt, resolvedAt); err != nil {
			return out, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to resolve alert episode.").WithCause(err)
		}

		next := model.AlertStateResolved
		if err := insertAuditLogTx(ctx, tx, auditEntry{
			AlertID:       exactID,
			EventType:     model.AuditAlertResolved,
			ActorType:     model.ActorAlertmanager,
			ActorLogin:    "alertmanager",
			PreviousState: &exactState,
			NewState:      &next,
			Metadata:      map[string]any{"group_key": incoming.GroupKey},
		}); err != nil {
			return out, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to log alert resolution.").WithCause(err)
		}

		hasOpen, err := alertHasOpenSnowIncidentTx(ctx, tx, exactID)
		if err != nil {
			return out, err
		}
		out.HasOpenTicketResolved = hasOpen
		out.Resolved = 1
		return out, nil
	}

	if !errors.Is(err, pgx.ErrNoRows) {
		return out, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to query exact alert episode.").WithCause(err)
	}

	// Exact (fingerprint, starts_at) was not found; check for an active open episode with the same fingerprint.
	var activeID string
	var activeStartsAt time.Time
	var activeStateStr string
	err = tx.QueryRow(ctx, `
		SELECT id::text, starts_at, state::text
		FROM alerts
		WHERE fingerprint = $1 AND state IN ('firing', 'acknowledged', 'dismissed')
		FOR UPDATE
	`, incoming.Fingerprint).Scan(&activeID, &activeStartsAt, &activeStateStr)

	if err == nil {
		activeState := model.AlertState(activeStateStr)
		if incoming.Status == model.AlertStateFiring && incoming.StartsAt.After(activeStartsAt) {
			// Missed resolved webhook: auto-resolve the superseded open episode, then insert fresh firing episode.
			if _, err := tx.Exec(ctx, `
				UPDATE alerts
				SET state = 'resolved',
					ends_at = COALESCE(ends_at, $2),
					resolved_at = $2,
					version = version + 1,
					last_received_at = NOW()
				WHERE id = $1::uuid
			`, activeID, incoming.StartsAt); err != nil {
				return out, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to supersede active alert episode.").WithCause(err)
			}

			next := model.AlertStateResolved
			comment := "Auto-resolved: superseded by newer firing episode"
			if err := insertAuditLogTx(ctx, tx, auditEntry{
				AlertID:       activeID,
				EventType:     model.AuditAlertResolved,
				ActorType:     model.ActorAlertmanager,
				ActorLogin:    "alertmanager",
				PreviousState: &activeState,
				NewState:      &next,
				Comment:       &comment,
				Metadata:      map[string]any{"superseded_by_starts_at": incoming.StartsAt.UTC().Format(time.RFC3339)},
			}); err != nil {
				return out, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to log superseded alert resolution.").WithCause(err)
			}

			hasOpen, err := alertHasOpenSnowIncidentTx(ctx, tx, activeID)
			if err != nil {
				return out, err
			}
			out.HasOpenTicketResolved = hasOpen

			if err := insertNewFiringAlertTx(ctx, tx, incoming, labelsJSON, annJSON); err != nil {
				return out, err
			}
			out.Inserted = 1
			return out, nil
		}

		if incoming.Status == model.AlertStateResolved && !incoming.StartsAt.Before(activeStartsAt) {
			resolvedAt := time.Now().UTC()
			if incoming.EndsAt != nil {
				resolvedAt = *incoming.EndsAt
			}
			if _, err := tx.Exec(ctx, `
				UPDATE alerts
				SET state = 'resolved',
					ends_at = COALESCE($2, ends_at),
					resolved_at = $3,
					version = version + 1,
					last_received_at = NOW(),
					delivery_count = delivery_count + 1
				WHERE id = $1::uuid
			`, activeID, incoming.EndsAt, resolvedAt); err != nil {
				return out, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to resolve active alert episode.").WithCause(err)
			}

			next := model.AlertStateResolved
			if err := insertAuditLogTx(ctx, tx, auditEntry{
				AlertID:       activeID,
				EventType:     model.AuditAlertResolved,
				ActorType:     model.ActorAlertmanager,
				ActorLogin:    "alertmanager",
				PreviousState: &activeState,
				NewState:      &next,
				Metadata:      map[string]any{"group_key": incoming.GroupKey, "matched_via": "active_fingerprint"},
			}); err != nil {
				return out, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to log active alert resolution.").WithCause(err)
			}

			hasOpen, err := alertHasOpenSnowIncidentTx(ctx, tx, activeID)
			if err != nil {
				return out, err
			}
			out.HasOpenTicketResolved = hasOpen
			out.Resolved = 1
			return out, nil
		}

		// incoming.StartsAt < activeStartsAt -> stale older episode delivery
		if logger != nil {
			logger.Warn("ignoring stale older alertmanager episode",
				slog.String("component", "alertmanager_reconciler"),
				slog.String("fingerprint", incoming.Fingerprint),
				slog.Time("incoming_starts_at", incoming.StartsAt),
				slog.Time("active_starts_at", activeStartsAt),
			)
		}
		out.Ignored = 1
		return out, nil
	}

	if !errors.Is(err, pgx.ErrNoRows) {
		return out, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to check active alert episode.").WithCause(err)
	}

	// No exact match and no open active episode -> create a fresh episode row.
	if incoming.Status == model.AlertStateFiring {
		if err := insertNewFiringAlertTx(ctx, tx, incoming, labelsJSON, annJSON); err != nil {
			return out, err
		}
		out.Inserted = 1
		return out, nil
	}

	// Missed initial firing webhook -> insert directly in resolved state.
	resolvedAt := time.Now().UTC()
	if incoming.EndsAt != nil {
		resolvedAt = *incoming.EndsAt
	}
	assignedTeam, assignedTeamBy, assignedTeamAt := lookupFingerprintTeamTx(ctx, tx, incoming.Fingerprint, incoming.Labels)
	var newID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO alerts (
			fingerprint, starts_at, ends_at, state, version,
			alert_name, instance, severity, group_key, receiver,
			labels, annotations, generator_url, external_url, resolved_at,
			assigned_team, assigned_team_by, assigned_team_at
		)
		VALUES ($1, $2, $3, 'resolved', 1, $4, $5, $6, $7, $8, $9::jsonb, $10::jsonb, $11, $12, $13, $14, $15, $16)
		RETURNING id::text
	`, incoming.Fingerprint, incoming.StartsAt, incoming.EndsAt,
		incoming.AlertName, incoming.Instance, incoming.Severity, incoming.GroupKey, incoming.Receiver,
		string(labelsJSON), string(annJSON), incoming.GeneratorURL, incoming.ExternalURL, resolvedAt,
		assignedTeam, assignedTeamBy, assignedTeamAt,
	).Scan(&newID); err != nil {
		return out, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to insert resolved alert episode.").WithCause(err)
	}

	next := model.AlertStateResolved
	if err := insertAuditLogTx(ctx, tx, auditEntry{
		AlertID:    newID,
		EventType:  model.AuditAlertResolved,
		ActorType:  model.ActorAlertmanager,
		ActorLogin: "alertmanager",
		NewState:   &next,
		Metadata:   map[string]any{"group_key": incoming.GroupKey, "direct_resolved": true},
	}); err != nil {
		return out, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to log resolved alert.").WithCause(err)
	}
	out.Resolved = 1
	return out, nil
}

func insertNewFiringAlertTx(ctx context.Context, tx pgx.Tx, incoming model.NormalizedAlert, labelsJSON, annJSON []byte) error {
	assignedTeam, assignedTeamBy, assignedTeamAt := lookupFingerprintTeamTx(ctx, tx, incoming.Fingerprint, incoming.Labels)
	var newID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO alerts (
			fingerprint, starts_at, ends_at, state, version,
			alert_name, instance, severity, group_key, receiver,
			labels, annotations, generator_url, external_url,
			assigned_team, assigned_team_by, assigned_team_at
		)
		VALUES ($1, $2, $3, 'firing', 1, $4, $5, $6, $7, $8, $9::jsonb, $10::jsonb, $11, $12, $13, $14, $15)
		RETURNING id::text
	`, incoming.Fingerprint, incoming.StartsAt, incoming.EndsAt,
		incoming.AlertName, incoming.Instance, incoming.Severity, incoming.GroupKey, incoming.Receiver,
		string(labelsJSON), string(annJSON), incoming.GeneratorURL, incoming.ExternalURL,
		assignedTeam, assignedTeamBy, assignedTeamAt,
	).Scan(&newID); err != nil {
		return apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to insert firing alert episode.").WithCause(err)
	}

	next := model.AlertStateFiring
	if err := insertAuditLogTx(ctx, tx, auditEntry{
		AlertID:    newID,
		EventType:  model.AuditAlertFired,
		ActorType:  model.ActorAlertmanager,
		ActorLogin: "alertmanager",
		NewState:   &next,
		Metadata:   map[string]any{"group_key": incoming.GroupKey},
	}); err != nil {
		return apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to log firing alert.").WithCause(err)
	}
	return nil
}

func lookupFingerprintTeamTx(ctx context.Context, tx pgx.Tx, fingerprint string, labels map[string]string) (*string, *string, *time.Time) {
	if labels != nil && strings.TrimSpace(labels["team"]) != "" {
		return nil, nil, nil
	}
	var team, taggedBy string
	var updatedAt time.Time
	err := tx.QueryRow(ctx, `
		SELECT team, tagged_by, updated_at
		FROM alert_fingerprint_teams
		WHERE fingerprint = $1
	`, fingerprint).Scan(&team, &taggedBy, &updatedAt)
	if err != nil {
		return nil, nil, nil
	}
	return &team, &taggedBy, &updatedAt
}

func alertHasOpenSnowIncidentTx(ctx context.Context, tx pgx.Tx, alertID string) (bool, error) {
	var exists bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM servicenow_incidents
			WHERE alert_id = $1::uuid
			  AND sync_status IN ('created', 'linked')
			  AND snow_state IN (1, 2, 3)
		)
	`, alertID).Scan(&exists)
	if err != nil {
		return false, apierror.New(http.StatusServiceUnavailable, apierror.CodeDBUnavailable, "Failed to check linked ServiceNow incident state.").WithCause(err)
	}
	return exists, nil
}

// InsertWebhookIngestionLog writes one operational webhook log entry.
func InsertWebhookIngestionLog(ctx context.Context, pool *pgxpool.Pool, entry model.WebhookIngestionLogEntry) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO webhook_ingestion_log (
			source, group_key, payload_hash, alert_count, truncated_alerts,
			http_status, processing_ms, error_message
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, entry.Source, entry.GroupKey, entry.PayloadHash, entry.AlertCount,
		entry.TruncatedAlerts, entry.HTTPStatus, entry.ProcessingMs, entry.ErrorMessage)
	return err
}
