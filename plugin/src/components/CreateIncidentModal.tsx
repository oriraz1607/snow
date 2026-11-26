import React, { useEffect, useRef, useState } from 'react';
import { css } from '@emotion/css';
import { GrafanaTheme2, SelectableValue } from '@grafana/data';
import {
  Alert as GrafanaAlert,
  Badge,
  Button,
  Field,
  Input,
  Modal,
  Select,
  TextArea,
  useTheme2,
} from '@grafana/ui';
import { alertApi, ApiClientError } from '../api/client';
import {
  Alert,
  AppPluginSettings,
  ServiceNowIncident,
  ServiceNowMetadataResponse,
  SnowCallerMode,
} from '../types/api';
import {
  buildBulkDescription,
  buildBulkShortDescription,
  calculateServiceNowPriority,
  defaultImpactUrgencyForSeverity,
  generateUuid,
  getAlertEnvironment,
  getAlertSite,
  highestSeverity,
} from '../utils/format';
import { getCurrentUser } from '../utils/permissions';

interface CreateIncidentModalProps {
  isOpen: boolean;
  alerts: Alert[];
  metadata: ServiceNowMetadataResponse | null;
  pluginSettings: AppPluginSettings;
  initialWorkNote?: string;
  onClose: () => void;
  onConflict?: (err: ApiClientError) => void;
  onCreated: (
    alertIds: string[],
    result: { servicenow_incident: ServiceNowIncident; warnings?: string[] }
  ) => void;
}

const IMPACT_URGENCY_OPTIONS: Array<SelectableValue<1 | 2 | 3>> = [
  { label: '1 - High', value: 1 },
  { label: '2 - Medium', value: 2 },
  { label: '3 - Low', value: 3 },
];

function getSharedPrometheusTeam(alerts: Alert[]): string {
  if (alerts.length === 0) {
    return '';
  }
  const first = alerts[0].labels?.team?.trim() || '';
  if (!first) {
    return '';
  }
  return alerts.every((a) => (a.labels?.team?.trim() || '') === first) ? first : '';
}

export const CreateIncidentModal: React.FC<CreateIncidentModalProps> = ({
  isOpen,
  alerts,
  metadata,
  pluginSettings,
  initialWorkNote = '',
  onClose,
  onConflict,
  onCreated,
}) => {
  const theme = useTheme2();
  const styles = getStyles(theme);
  const currentUser = getCurrentUser();

  const [shortDescription, setShortDescription] = useState('');
  const [description, setDescription] = useState('');
  const [impact, setImpact] = useState<1 | 2 | 3>(2);
  const [urgency, setUrgency] = useState<1 | 2 | 3>(2);
  const [assignmentGroup, setAssignmentGroup] = useState('Infrastructure');
  const [category, setCategory] = useState('Infrastructure');
  const [callerMode, setCallerMode] = useState<SnowCallerMode>('sso_user');
  const [workNotes, setWorkNotes] = useState('');
  const [idempotencyKey, setIdempotencyKey] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState<string | null>(null);
  const [isConflict, setIsConflict] = useState(false);
  const [canRetry, setCanRetry] = useState(false);

  // Track the initialized selection key so background queue polling never resets modal edits.
  const initializedKeyRef = useRef<string | null>(null);

  useEffect(() => {
    if (!isOpen || alerts.length === 0) {
      initializedKeyRef.current = null;
      return;
    }
    const selectionKey = alerts
      .map((a) => a.id)
      .sort()
      .join(',');
    if (initializedKeyRef.current === selectionKey) {
      return;
    }
    initializedKeyRef.current = selectionKey;

    const topSeverity = highestSeverity(alerts);
    const mapped = defaultImpactUrgencyForSeverity(topSeverity);

    const sharedLabelTeam = getSharedPrometheusTeam(alerts);
    const learnedGroup =
      sharedLabelTeam && metadata?.team_assignment_groups
        ? metadata.team_assignment_groups[sharedLabelTeam]?.trim() || ''
        : '';

    let defaultGroup = metadata?.default_assignment_group ?? '';
    if (learnedGroup) {
      defaultGroup = learnedGroup;
    } else {
      for (const a of alerts) {
        const explicitGroup = a.labels?.assignment_group?.trim();
        if (explicitGroup) {
          defaultGroup = explicitGroup;
          break;
        }
      }
    }

    const defaultCategory = metadata?.default_category || 'Infrastructure';
    const defaultMode: SnowCallerMode =
      pluginSettings.defaultCallerMode || metadata?.default_caller_mode || 'sso_user';

    setShortDescription(buildBulkShortDescription(alerts));
    setDescription(buildBulkDescription(alerts));
    setImpact(mapped.impact);
    setUrgency(mapped.urgency);
    setAssignmentGroup(defaultGroup);
    setCategory(defaultCategory);
    setCallerMode(defaultMode);
    setWorkNotes(initialWorkNote);
    setIdempotencyKey(generateUuid());
    setSubmitError(null);
    setIsConflict(false);
    setCanRetry(false);
    setSubmitting(false);
  }, [isOpen, alerts, metadata, pluginSettings.defaultCallerMode, initialWorkNote]);

  if (!isOpen || alerts.length === 0) {
    return null;
  }

  const groupNames = new Set<string>();
  if (assignmentGroup) {
    groupNames.add(assignmentGroup);
  }
  if (metadata?.default_assignment_group) {
    groupNames.add(metadata.default_assignment_group);
  }
  for (const g of metadata?.assignment_groups ?? []) {
    if (g.name) {
      groupNames.add(g.name);
    }
  }
  const groupOptions: Array<SelectableValue<string>> = Array.from(groupNames).map((name) => ({
    label: name,
    value: name,
  }));

  const categoryNames = new Set<string>(['Infrastructure', 'Database', 'Network', 'Software']);
  if (category) {
    categoryNames.add(category);
  }
  if (metadata?.default_category) {
    categoryNames.add(metadata.default_category);
  }
  for (const c of metadata?.categories ?? []) {
    if (c) {
      categoryNames.add(c);
    }
  }
  const categoryOptions: Array<SelectableValue<string>> = Array.from(categoryNames).map((c) => ({
    label: c,
    value: c,
  }));

  const priority = calculateServiceNowPriority(impact, urgency);
  const sharedLabelTeam = getSharedPrometheusTeam(alerts);
  const learnedGroupForTeam =
    sharedLabelTeam && metadata?.team_assignment_groups
      ? metadata.team_assignment_groups[sharedLabelTeam]?.trim() || ''
      : '';
  const priorityColor =
    priority.value === 1 ? 'red' : priority.value === 2 ? 'orange' : priority.value === 3 ? 'blue' : 'green';
  const genericNocLabel =
    pluginSettings.genericNocDisplayLabel?.trim() ||
    `Fallback Service Account (${metadata?.default_caller_username || 'noc'})`;
  const ssoIdentityLabel = currentUser.email || currentUser.login || 'current SSO user';

  const trimmedShort = shortDescription.trim();
  const shortInvalid = trimmedShort.length < 5 || trimmedShort.length > 160;
  const firingCount = alerts.filter((a) => a.state === 'firing').length;
  const isBulk = alerts.length > 1;

  const modalTitle =
    alerts.length > 1
      ? firingCount > 0
        ? `Acknowledge & Create Single ServiceNow Incident (${alerts.length} alerts)`
        : `Create Single ServiceNow Incident (${alerts.length} alerts)`
      : firingCount > 0
        ? `Acknowledge & Create ServiceNow Incident — ${alerts[0].alert_name}`
        : `Create ServiceNow Incident — ${alerts[0].alert_name}`;

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (shortInvalid || submitting) {
      return;
    }
    setSubmitting(true);
    setSubmitError(null);
    setIsConflict(false);

    try {
      if (isBulk) {
        const resp = await alertApi.createBulkIncident(
          {
            alert_ids: alerts.map((a) => a.id),
            short_description: trimmedShort,
            description,
            impact,
            urgency,
            assignment_group: assignmentGroup.trim(),
            category: category.trim(),
            caller_mode: callerMode,
            work_notes: workNotes.trim(),
          },
          idempotencyKey
        );
        onCreated(resp.alert_ids, {
          servicenow_incident: resp.servicenow_incident,
          warnings: resp.warnings,
        });
      } else {
        const resp = await alertApi.createIncident(
          alerts[0].id,
          {
            short_description: trimmedShort,
            description,
            impact,
            urgency,
            assignment_group: assignmentGroup.trim(),
            category: category.trim(),
            caller_mode: callerMode,
            work_notes: workNotes.trim(),
          },
          idempotencyKey
        );
        onCreated([resp.alert_id], {
          servicenow_incident: resp.servicenow_incident,
          warnings: resp.warnings,
        });
      }
    } catch (err) {
      const apiErr = err as ApiClientError;
      if (apiErr?.status === 409) {
        const details = apiErr.details ?? {};
        const conflictAlertId =
          typeof details.alert_id === 'string'
            ? details.alert_id
            : alerts.length === 1
              ? alerts[0].id
              : '';
        const conflictAlert = conflictAlertId ? alerts.find((a) => a.id === conflictAlertId) : undefined;
        const conflictNum =
          typeof details.incident_number === 'string' && details.incident_number
            ? ` (${details.incident_number})`
            : '';
        const conflictState =
          typeof details.current_state === 'string' && details.current_state
            ? ` (current state: ${details.current_state})`
            : '';
        const alertPrefix = conflictAlert ? `${conflictAlert.alert_name}: ` : '';

        setCanRetry(false);
        setIsConflict(true);
        setSubmitError(
          `${alertPrefix}${apiErr.message || 'Alert state or linked incident changed.'}${conflictNum}${conflictState} Queue has been refreshed — review updated state or close this dialog.`
        );
        onConflict?.(apiErr);
        return;
      }

      const isTransient = apiErr?.status === 502 || apiErr?.status === 503 || apiErr?.status === 504;
      setIsConflict(false);
      setCanRetry(isTransient);
      setSubmitError(
        isTransient
          ? firingCount > 0
            ? `ServiceNow returned HTTP ${apiErr.status}: ${apiErr.message}. Alert(s) remain firing until the incident is created — retry safely with the preserved form and idempotency key.`
            : `ServiceNow returned HTTP ${apiErr.status}: ${apiErr.message}. Alert(s) remain acknowledged — retry safely with the preserved form and idempotency key.`
          : apiErr?.message || 'Failed to create ServiceNow incident.'
      );
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Modal
      isOpen={isOpen}
      title={modalTitle}
      onDismiss={onClose}
      closeOnBackdropClick={false}
      className={styles.modal}
    >
      <form onSubmit={handleSubmit}>
        {submitError && (
          <div
            className={styles.errorBox}
            data-testid={isConflict ? 'create-modal-conflict-error' : 'create-modal-error'}
          >
            <GrafanaAlert
              severity={isConflict ? 'warning' : 'error'}
              title={isConflict ? 'Conflict — queue refreshed' : 'ServiceNow incident creation failed'}
            >
              {submitError}
            </GrafanaAlert>
          </div>
        )}

        {(alerts.length > 1 || firingCount > 0) && (
          <div className={styles.selectedSummary} data-testid="selected-alerts-summary">
            <div className={styles.selectedHeader}>
              <strong>Included Alerts ({alerts.length})</strong>
              {firingCount > 0 && (
                <span className={styles.autoAckNote}>
                  {firingCount} firing alert{firingCount === 1 ? '' : 's'} will be acknowledged once the incident is created
                </span>
              )}
            </div>
            <ul className={styles.selectedList}>
              {alerts.map((a) => (
                <li key={a.id}>
                  <strong>{a.alert_name}</strong> ({a.severity}, {a.state}
                  {getAlertEnvironment(a) ? `, env=${getAlertEnvironment(a)}` : ''}
                  {getAlertSite(a) ? `, site=${getAlertSite(a)}` : ''})
                </li>
              ))}
            </ul>
          </div>
        )}

        <Field
          label="Short Description"
          required
          invalid={shortInvalid}
          error={shortInvalid ? 'Short description must be between 5 and 160 characters.' : undefined}
          description={`${trimmedShort.length}/160 characters`}
        >
          <Input
            value={shortDescription}
            maxLength={160}
            onChange={(e) => setShortDescription(e.currentTarget.value)}
            aria-label="Short Description"
          />
        </Field>

        <Field label="Description">
          <TextArea
            rows={5}
            value={description}
            onChange={(e) => setDescription(e.currentTarget.value)}
            aria-label="Description"
          />
        </Field>

        <div className={styles.gridRow}>
          <Field label="Impact" className={styles.gridItem}>
            <Select
              aria-label="Impact"
              options={IMPACT_URGENCY_OPTIONS}
              value={impact}
              onChange={(opt) => setImpact((opt.value ?? 2) as 1 | 2 | 3)}
            />
          </Field>

          <Field label="Urgency" className={styles.gridItem}>
            <Select
              aria-label="Urgency"
              options={IMPACT_URGENCY_OPTIONS}
              value={urgency}
              onChange={(opt) => setUrgency((opt.value ?? 2) as 1 | 2 | 3)}
            />
          </Field>

          <Field label="Calculated Priority" className={styles.gridItem}>
            <div className={styles.priorityBox} data-testid="calculated-priority">
              <Badge text={priority.label} color={priorityColor} />
            </div>
          </Field>
        </div>

        <div className={styles.gridRow}>
          <Field
            label="Assignment Group"
            className={styles.gridItem}
            description={
              learnedGroupForTeam && assignmentGroup === learnedGroupForTeam
                ? `Auto-selected from learned mapping for team "${sharedLabelTeam}".`
                : undefined
            }
          >
            <Select
              aria-label="Assignment Group"
              options={groupOptions}
              value={assignmentGroup}
              allowCustomValue
              onChange={(opt) => setAssignmentGroup(opt.value ?? '')}
              onCreateOption={(custom) => setAssignmentGroup(custom)}
            />
          </Field>

          <Field label="Category" className={styles.gridItem}>
            <Select
              aria-label="Category"
              options={categoryOptions}
              value={category}
              allowCustomValue
              onChange={(opt) => setCategory(opt.value ?? '')}
              onCreateOption={(custom) => setCategory(custom)}
            />
          </Field>
        </div>

        <Field
          label="Caller Identity"
          description="Falls back to the service account if SSO email lookup misses in ServiceNow."
        >
          <div className={styles.radioGroup}>
            <label className={styles.radioOption}>
              <input
                type="radio"
                name="caller_mode"
                value="sso_user"
                checked={callerMode === 'sso_user'}
                onChange={() => setCallerMode('sso_user')}
              />
              <span>Use my SSO account ({ssoIdentityLabel})</span>
            </label>
            <label className={styles.radioOption}>
              <input
                type="radio"
                name="caller_mode"
                value="generic_noc"
                checked={callerMode === 'generic_noc'}
                onChange={() => setCallerMode('generic_noc')}
              />
              <span>{genericNocLabel}</span>
            </label>
          </div>
        </Field>

        <Field label="Initial Work Notes (optional)">
          <TextArea
            rows={2}
            value={workNotes}
            onChange={(e) => setWorkNotes(e.currentTarget.value)}
            placeholder="Optional triage context to include as initial work notes..."
            aria-label="Initial Work Notes"
          />
        </Field>

        <div className={styles.footerRow}>
          <span className={styles.idempotencyText}>Idempotency-Key: {idempotencyKey}</span>
          <div className={styles.actions}>
            <Button variant="secondary" type="button" onClick={onClose} disabled={submitting}>
              Cancel
            </Button>
            <Button variant="primary" type="submit" disabled={shortInvalid || submitting}>
              {submitting
                ? 'Creating Incident...'
                : canRetry
                  ? 'Retry Creation'
                  : alerts.length > 1
                    ? firingCount > 0
                      ? `Acknowledge & Create Single Incident (${alerts.length})`
                      : `Create Single Incident (${alerts.length})`
                    : firingCount > 0
                      ? 'Acknowledge & Create Incident'
                      : 'Create ServiceNow Incident'}
            </Button>
          </div>
        </div>
      </form>
    </Modal>
  );
};

const getStyles = (theme: GrafanaTheme2) => ({
  modal: css`
    width: min(700px, 95vw);
  `,
  errorBox: css`
    margin-bottom: ${theme.spacing(2)};
  `,
  selectedSummary: css`
    padding: ${theme.spacing(1.25)};
    margin-bottom: ${theme.spacing(1.5)};
    border: 1px solid ${theme.colors.border.medium};
    border-radius: ${theme.shape.radius.default};
    background: ${theme.colors.background.secondary};
    font-size: ${theme.typography.bodySmall.fontSize};
  `,
  selectedHeader: css`
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: ${theme.spacing(0.5)};
  `,
  autoAckNote: css`
    color: ${theme.colors.warning.text};
    font-size: 11px;
  `,
  selectedList: css`
    margin: 0;
    padding-left: ${theme.spacing(2.25)};
    max-height: 110px;
    overflow-y: auto;
  `,
  gridRow: css`
    display: flex;
    flex-wrap: wrap;
    gap: ${theme.spacing(1.5)};
  `,
  gridItem: css`
    flex: 1 1 160px;
  `,
  priorityBox: css`
    display: flex;
    align-items: center;
    min-height: 32px;
  `,
  radioGroup: css`
    display: flex;
    flex-direction: column;
    gap: ${theme.spacing(0.75)};
    padding: ${theme.spacing(1)};
    border: 1px solid ${theme.colors.border.weak};
    border-radius: ${theme.shape.radius.default};
    background: ${theme.colors.background.secondary};
  `,
  radioOption: css`
    display: flex;
    align-items: center;
    gap: ${theme.spacing(1)};
    cursor: pointer;
    font-size: ${theme.typography.body.fontSize};
    margin: 0;
  `,
  footerRow: css`
    display: flex;
    flex-wrap: wrap;
    justify-content: space-between;
    align-items: center;
    gap: ${theme.spacing(1)};
    margin-top: ${theme.spacing(2)};
  `,
  idempotencyText: css`
    font-size: 11px;
    font-family: ${theme.typography.fontFamilyMonospace};
    color: ${theme.colors.text.disabled};
  `,
  actions: css`
    display: flex;
    gap: ${theme.spacing(1)};
  `,
});
