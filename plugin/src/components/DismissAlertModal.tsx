import React, { useEffect, useState } from 'react';
import { css } from '@emotion/css';
import { GrafanaTheme2 } from '@grafana/data';
import { Alert as GrafanaAlert, Button, Field, Modal, TextArea, useTheme2 } from '@grafana/ui';
import { alertApi, ApiClientError } from '../api/client';
import { Alert } from '../types/api';

interface DismissAlertModalProps {
  isOpen: boolean;
  alerts: Alert[];
  onClose: () => void;
  onDismissed: (dismissedIds: string[]) => void;
  onConflict: (err: ApiClientError) => void;
}

export const DismissAlertModal: React.FC<DismissAlertModalProps> = ({
  isOpen,
  alerts,
  onClose,
  onDismissed,
  onConflict,
}) => {
  const theme = useTheme2();
  const styles = getStyles(theme);
  const [reason, setReason] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [isOpenIncidentConflict, setIsOpenIncidentConflict] = useState(false);

  const alertKey = alerts.map((a) => a.id).join(',');

  useEffect(() => {
    if (isOpen) {
      setReason('');
      setSubmitting(false);
      setError(null);
      setIsOpenIncidentConflict(false);
    }
  }, [isOpen, alertKey]);

  if (!isOpen || alerts.length === 0) {
    return null;
  }

  const isBulk = alerts.length > 1;
  const trimmed = reason.trim();
  const invalid = trimmed.length < 3 || trimmed.length > 500;

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (invalid || submitting) {
      return;
    }
    setSubmitting(true);
    setError(null);
    setIsOpenIncidentConflict(false);
    try {
      if (isBulk) {
        const resp = await alertApi.bulkDismissAlerts({
          alert_ids: alerts.map((a) => a.id),
          reason: trimmed,
        });
        onDismissed(resp.alert_ids ?? alerts.map((a) => a.id));
      } else {
        const target = alerts[0];
        const updated = await alertApi.dismissAlert(target.id, {
          version: target.version,
          reason: trimmed,
        });
        onDismissed([updated.id]);
      }
    } catch (err) {
      const apiErr = err as ApiClientError;
      if (apiErr?.status === 409) {
        onConflict(apiErr);
        if (apiErr.code === 'ALERT_HAS_OPEN_INCIDENT') {
          setIsOpenIncidentConflict(true);
          setError(apiErr.message || 'Cannot dismiss alert while linked ServiceNow incident is open.');
          return;
        }
        onClose();
        return;
      }
      setError(apiErr?.message || 'Failed to dismiss alert(s).');
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Modal
      isOpen={isOpen}
      title={isBulk ? `Dismiss ${alerts.length} Alerts` : `Dismiss Alert — ${alerts[0].alert_name}`}
      onDismiss={onClose}
    >
      <form onSubmit={handleSubmit}>
        {error && (
          <div
            style={{ marginBottom: '12px' }}
            data-testid={isOpenIncidentConflict ? 'dismiss-modal-conflict-error' : 'dismiss-modal-error'}
          >
            <GrafanaAlert
              severity={isOpenIncidentConflict ? 'warning' : 'error'}
              title={isOpenIncidentConflict ? 'Cannot dismiss — open ServiceNow incident' : 'Dismiss failed'}
            >
              {error}
            </GrafanaAlert>
          </div>
        )}

        {isBulk && (
          <div className={styles.summaryBox} data-testid="dismiss-alerts-summary">
            <div className={styles.summaryHeader}>Selected Alerts ({alerts.length})</div>
            <ul className={styles.summaryList}>
              {alerts.map((a) => (
                <li key={a.id}>
                  <strong>{a.alert_name}</strong> ({a.severity})
                </li>
              ))}
            </ul>
          </div>
        )}

        <Field
          label="Dismiss Reason"
          required
          invalid={reason.length > 0 && invalid}
          error={reason.length > 0 && invalid ? 'Reason must be between 3 and 500 characters.' : undefined}
          description={`Required for audit trail (${trimmed.length}/500 characters).`}
        >
          <TextArea
            rows={3}
            value={reason}
            maxLength={500}
            placeholder="e.g., Expected transient spike during scheduled maintenance window."
            onChange={(e) => setReason(e.currentTarget.value)}
            aria-label="Dismiss Reason"
          />
        </Field>

        <Modal.ButtonRow>
          <Button variant="secondary" type="button" onClick={onClose} disabled={submitting}>
            Cancel
          </Button>
          <Button variant="destructive" type="submit" disabled={invalid || submitting}>
            {submitting
              ? 'Dismissing...'
              : isBulk
                ? `Dismiss ${alerts.length} Alerts`
                : 'Confirm Dismiss'}
          </Button>
        </Modal.ButtonRow>
      </form>
    </Modal>
  );
};

const getStyles = (theme: GrafanaTheme2) => ({
  summaryBox: css`
    margin-bottom: ${theme.spacing(1.5)};
    padding: ${theme.spacing(1)} ${theme.spacing(1.25)};
    border-radius: ${theme.shape.radius.default};
    border: 1px solid ${theme.colors.border.weak};
    background: ${theme.colors.background.secondary};
  `,
  summaryHeader: css`
    font-size: ${theme.typography.bodySmall.fontSize};
    font-weight: ${theme.typography.fontWeightBold};
    margin-bottom: ${theme.spacing(0.5)};
  `,
  summaryList: css`
    margin: 0;
    padding-left: ${theme.spacing(2)};
    max-height: 140px;
    overflow-y: auto;
    font-size: ${theme.typography.bodySmall.fontSize};
    color: ${theme.colors.text.secondary};
  `,
});
