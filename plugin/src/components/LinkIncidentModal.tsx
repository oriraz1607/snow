import React, { useEffect, useState } from 'react';
import { Alert as GrafanaAlert, Button, Checkbox, Field, Input, Modal } from '@grafana/ui';
import { alertApi, ApiClientError } from '../api/client';
import { Alert, IncidentSyncResponse } from '../types/api';

interface LinkIncidentModalProps {
  isOpen: boolean;
  alert: Alert | null;
  onClose: () => void;
  onLinked: (resp: IncidentSyncResponse) => void;
}

export const LinkIncidentModal: React.FC<LinkIncidentModalProps> = ({
  isOpen,
  alert,
  onClose,
  onLinked,
}) => {
  const [incidentNumber, setIncidentNumber] = useState('');
  const [postWorkNote, setPostWorkNote] = useState(true);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (isOpen) {
      setIncidentNumber('');
      setPostWorkNote(true);
      setSubmitting(false);
      setError(null);
    }
  }, [isOpen, alert?.id]);

  if (!isOpen || !alert) {
    return null;
  }

  const trimmed = incidentNumber.trim().toUpperCase();
  const isValid = trimmed.length >= 4;

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!isValid || submitting) {
      return;
    }
    setSubmitting(true);
    setError(null);
    try {
      const resp = await alertApi.linkIncident(alert.id, {
        incident_number: trimmed,
        post_work_note: postWorkNote,
      });
      onLinked(resp);
    } catch (err) {
      const apiErr = err as ApiClientError;
      setError(apiErr?.message || 'Failed to link existing ServiceNow incident.');
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Modal isOpen={isOpen} title={`Link Existing Incident — ${alert.alert_name}`} onDismiss={onClose}>
      <form onSubmit={handleSubmit}>
        {error && (
          <div style={{ marginBottom: '12px' }}>
            <GrafanaAlert severity="error" title="Unable to link incident">
              {error}
            </GrafanaAlert>
          </div>
        )}

        <Field label="ServiceNow Incident Number" required description="Example: INC0012345">
          <Input
            placeholder="INC0012345"
            value={incidentNumber}
            onChange={(e) => setIncidentNumber(e.currentTarget.value)}
            aria-label="Incident Number"
          />
        </Field>

        <Field>
          <Checkbox
            label="Post Grafana alert link work note to the ServiceNow incident"
            value={postWorkNote}
            onChange={(e) => setPostWorkNote(e.currentTarget.checked)}
          />
        </Field>

        <Modal.ButtonRow>
          <Button variant="secondary" type="button" onClick={onClose} disabled={submitting}>
            Cancel
          </Button>
          <Button variant="primary" type="submit" disabled={!isValid || submitting}>
            {submitting ? 'Linking...' : 'Link Incident'}
          </Button>
        </Modal.ButtonRow>
      </form>
    </Modal>
  );
};
