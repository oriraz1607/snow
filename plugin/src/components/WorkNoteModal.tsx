import React, { useEffect, useState } from 'react';
import { Alert as GrafanaAlert, Button, Field, Modal, TextArea } from '@grafana/ui';
import { alertApi, ApiClientError } from '../api/client';
import { Alert } from '../types/api';

export type NoteModalMode = 'snow_work_note' | 'triage_note';

interface WorkNoteModalProps {
  isOpen: boolean;
  mode: NoteModalMode;
  alert: Alert | null;
  title: string;
  initialText?: string;
  onClose: () => void;
  onSubmitted: (alertId: string, mode: NoteModalMode) => void;
}

export const WorkNoteModal: React.FC<WorkNoteModalProps> = ({
  isOpen,
  mode,
  alert,
  title,
  initialText = '',
  onClose,
  onSubmitted,
}) => {
  const [text, setText] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (isOpen) {
      setText(initialText);
      setSubmitting(false);
      setError(null);
    }
  }, [isOpen, initialText, alert?.id]);

  if (!isOpen || !alert) {
    return null;
  }

  const trimmed = text.trim();
  const invalid = trimmed.length === 0 || trimmed.length > 2000;

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (invalid || submitting) {
      return;
    }
    setSubmitting(true);
    setError(null);
    try {
      if (mode === 'snow_work_note') {
        await alertApi.postWorkNote(alert.id, { work_notes: trimmed });
      } else {
        await alertApi.addNote(alert.id, { comment: trimmed });
      }
      onSubmitted(alert.id, mode);
    } catch (err) {
      const apiErr = err as ApiClientError;
      setError(apiErr?.message || 'Failed to submit note.');
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Modal isOpen={isOpen} title={title} onDismiss={onClose}>
      <form onSubmit={handleSubmit}>
        {error && (
          <div style={{ marginBottom: '12px' }}>
            <GrafanaAlert severity="error" title="Unable to post note">
              {error}
            </GrafanaAlert>
          </div>
        )}

        <Field
          label={mode === 'snow_work_note' ? 'ServiceNow Work Note' : 'Triage Audit Note'}
          required
          description={`${trimmed.length}/2000 characters`}
        >
          <TextArea
            rows={4}
            maxLength={2000}
            value={text}
            onChange={(e) => setText(e.currentTarget.value)}
            placeholder={
              mode === 'snow_work_note'
                ? 'Enter work note to append to the linked ServiceNow incident...'
                : 'Enter operator triage note for the audit log...'
            }
            aria-label="Note text"
          />
        </Field>

        <Modal.ButtonRow>
          <Button variant="secondary" type="button" onClick={onClose} disabled={submitting}>
            Cancel
          </Button>
          <Button variant="primary" type="submit" disabled={invalid || submitting}>
            {submitting
              ? 'Posting...'
              : mode === 'snow_work_note'
                ? 'Post Work Note'
                : 'Save Note'}
          </Button>
        </Modal.ButtonRow>
      </form>
    </Modal>
  );
};
