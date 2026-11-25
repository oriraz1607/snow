import React, { useEffect, useState } from 'react';
import { Alert as GrafanaAlert, Button, Field, Modal, TextArea } from '@grafana/ui';
import { alertApi, ApiClientError } from '../api/client';
import { Alert } from '../types/api';

interface DismissAlertModalProps {
  isOpen: boolean;
  alert: Alert | null;
  onClose: () => void;
  onDismissed: (updated: Alert) => void;
  onConflict: (err: ApiClientError, alertId: string) => void;
}

export const DismissAlertModal: React.FC<DismissAlertModalProps> = ({
  isOpen,
  alert,
  onClose,
  onDismissed,
  onConflict,
}) => {
  const [reason, setReason] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (isOpen) {
      setReason('');
      setSubmitting(false);
      setError(null);
    }
  }, [isOpen, alert?.id]);

  if (!isOpen || !alert) {
    return null;
  }

  const trimmed = reason.trim();
  const invalid = trimmed.length < 3 || trimmed.length > 500;

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (invalid || submitting) {
      return;
    }
    setSubmitting(true);
    setError(null);
    try {
      const updated = await alertApi.dismissAlert(alert.id, {
        version: alert.version,
        reason: trimmed,
      });
      onDismissed(updated);
    } catch (err) {
      const apiErr = err as ApiClientError;
      if (apiErr?.status === 409) {
        onConflict(apiErr, alert.id);
        onClose();
        return;
      }
      setError(apiErr?.message || 'Failed to dismiss alert.');
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Modal isOpen={isOpen} title={`Dismiss Alert — ${alert.alert_name}`} onDismiss={onClose}>
      <form onSubmit={handleSubmit}>
        {error && (
          <div style={{ marginBottom: '12px' }}>
            <GrafanaAlert severity="error" title="Dismiss failed">
              {error}
            </GrafanaAlert>
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
            {submitting ? 'Dismissing...' : 'Confirm Dismiss'}
          </Button>
        </Modal.ButtonRow>
      </form>
    </Modal>
  );
};
