import React from 'react';
import { css } from '@emotion/css';
import { GrafanaTheme2 } from '@grafana/data';
import { Badge, Button, Spinner, Tooltip, useTheme2 } from '@grafana/ui';
import { Alert } from '../types/api';
import {
  formatRelativeTime,
  getAlertEnvironment,
  getAlertSite,
  getAlertTeam,
  isAlertSelectableForIncident,
} from '../utils/format';

interface AlertTableProps {
  alerts: Alert[];
  loading: boolean;
  canTriage: boolean;
  canWriteIncidents: boolean;
  busyAlertId: string | null;
  selectedAlertIds: Set<string>;
  onToggleSelectAlert: (alertId: string) => void;
  onToggleSelectAll: (selectableIds: string[], selectAll: boolean) => void;
  onSelectAlert: (alert: Alert) => void;
  onAcknowledge: (alert: Alert) => void;
  onUnacknowledge: (alert: Alert) => void;
  onReopen: (alert: Alert) => void;
  onOpenDismiss: (alert: Alert) => void;
  onOpenCreateIncident: (alert: Alert) => void;
  onOpenLinkIncident: (alert: Alert) => void;
  onOpenAddNote: (alert: Alert) => void;
  onOpenWorkNote: (alert: Alert) => void;
  onSyncIncident: (alert: Alert) => void;
  onFilterByFingerprint: (fingerprint: string) => void;
}

const DISABLED_TRIAGE_REASON = 'Requires Editor or Admin role (alerts:triage permission)';
const DISABLED_INCIDENT_REASON = 'Requires Editor or Admin role (incidents:write permission)';

export const AlertTable: React.FC<AlertTableProps> = ({
  alerts,
  loading,
  canTriage,
  canWriteIncidents,
  busyAlertId,
  selectedAlertIds,
  onToggleSelectAlert,
  onToggleSelectAll,
  onSelectAlert,
  onAcknowledge,
  onUnacknowledge,
  onReopen,
  onOpenDismiss,
  onOpenCreateIncident,
  onOpenLinkIncident,
  onOpenAddNote,
  onOpenWorkNote,
  onSyncIncident,
  onFilterByFingerprint,
}) => {
  const theme = useTheme2();
  const styles = getStyles(theme);

  if (loading && alerts.length === 0) {
    return (
      <div className={styles.emptyState}>
        <Spinner size="lg" />
        <span>Loading alert queue...</span>
      </div>
    );
  }

  if (alerts.length === 0) {
    return (
      <div className={styles.emptyState}>
        <span>No alerts match the current filter criteria.</span>
      </div>
    );
  }

  const selectableIds = alerts.filter(isAlertSelectableForIncident).map((a) => a.id);
  const allSelectableChecked =
    selectableIds.length > 0 && selectableIds.every((id) => selectedAlertIds.has(id));

  return (
    <div className={styles.tableWrapper}>
      <table className={styles.table} aria-label="Alert queue table">
        <thead>
          <tr>
            <th className={styles.checkboxCell}>
              <input
                type="checkbox"
                aria-label="Select all eligible alerts"
                disabled={selectableIds.length === 0}
                checked={allSelectableChecked}
                onChange={(e) => onToggleSelectAll(selectableIds, e.currentTarget.checked)}
              />
            </th>
            <th>Severity</th>
            <th>State</th>
            <th>Alert</th>
            <th>Environment</th>
            <th>Site</th>
            <th>Team</th>
            <th>Duration</th>
            <th>Owner</th>
            <th>ServiceNow</th>
            <th>Actions</th>
          </tr>
        </thead>
        <tbody>
          {alerts.map((alert, idx) => {
            const inc = alert.servicenow_incident;
            const hasLinkedIncident =
              Boolean(inc) && (inc?.sync_status === 'created' || inc?.sync_status === 'linked');
            const isAckWithoutIncident = alert.state === 'acknowledged' && !hasLinkedIncident;
            const isBusy = busyAlertId === alert.id;
            const selectable = isAlertSelectableForIncident(alert);
            const checked = selectedAlertIds.has(alert.id);
            const env = getAlertEnvironment(alert);
            const site = getAlertSite(alert);
            const teamInfo = getAlertTeam(alert);

            return (
              <React.Fragment key={alert.id}>
                <tr
                  className={`${styles.row} ${idx % 2 === 1 ? styles.zebraRow : ''} ${
                    checked ? styles.selectedRow : ''
                  } ${isAckWithoutIncident ? styles.rowWithActionBar : ''}`}
                  data-testid={`alert-row-${alert.id}`}
                >
                  <td className={styles.checkboxCell}>
                    <input
                      type="checkbox"
                      aria-label={`Select ${alert.alert_name}`}
                      disabled={!selectable}
                      checked={checked}
                      onChange={() => onToggleSelectAlert(alert.id)}
                    />
                  </td>

                  <td className={styles.nowrapCell}>
                    <Badge text={alert.severity || 'unknown'} color={severityColor(alert.severity)} />
                  </td>

                  <td className={styles.nowrapCell}>
                    <Badge text={displayStateLabel(alert)} color={displayStateColor(alert)} />
                  </td>

                  <td className={styles.alertCell}>
                    <button
                      type="button"
                      className={styles.alertNameBtn}
                      onClick={() => onSelectAlert(alert)}
                    >
                      {alert.alert_name}
                    </button>
                    {alert.annotations?.summary && (
                      <div className={styles.summaryText} title={alert.annotations.summary}>
                        {alert.annotations.summary}
                      </div>
                    )}
                    {alert.previous_episode_count > 0 && (
                      <button
                        type="button"
                        className={styles.historyLinkBtn}
                        onClick={() => onFilterByFingerprint(alert.fingerprint)}
                      >
                        {alert.previous_episode_count} prior episode
                        {alert.previous_episode_count === 1 ? '' : 's'}
                      </button>
                    )}
                  </td>

                  <td className={styles.nowrapCell}>
                    {env ? <span className={styles.metaPill}>{env}</span> : <span className={styles.mutedText}>—</span>}
                  </td>

                  <td className={styles.nowrapCell}>
                    {site ? <span className={styles.metaPill}>{site}</span> : <span className={styles.mutedText}>—</span>}
                  </td>

                  <td className={styles.nowrapCell}>
                    {teamInfo.team ? (
                      <Tooltip
                        content={
                          teamInfo.source === 'label'
                            ? 'From Prometheus team label'
                            : `Tagged by ${teamInfo.taggedBy || 'operator'}`
                        }
                        placement="top"
                      >
                        <span>
                          <Badge
                            text={teamInfo.team}
                            color={teamInfo.source === 'label' ? 'blue' : 'purple'}
                          />
                        </span>
                      </Tooltip>
                    ) : (
                      <span className={styles.untaggedText}>Untagged</span>
                    )}
                  </td>

                  <td className={styles.nowrapCell}>
                    {alert.state === 'resolved' && alert.resolved_at ? (
                      <div>
                        <div>Resolved {formatRelativeTime(alert.resolved_at)} ago</div>
                        <div className={styles.mutedText}>Started {formatRelativeTime(alert.starts_at)} ago</div>
                      </div>
                    ) : (
                      <div>
                        <div>{formatRelativeTime(alert.starts_at)}</div>
                        <div className={styles.mutedText}>v{alert.version}</div>
                      </div>
                    )}
                  </td>

                  <td className={styles.nowrapCell}>
                    {alert.acknowledged_by ? (
                      <div>
                        <div>{alert.acknowledged_by}</div>
                        {alert.acknowledged_at && (
                          <div className={styles.mutedText}>{formatRelativeTime(alert.acknowledged_at)} ago</div>
                        )}
                      </div>
                    ) : alert.dismissed_by ? (
                      <div>Dismissed by {alert.dismissed_by}</div>
                    ) : (
                      <span className={styles.mutedText}>—</span>
                    )}
                  </td>

                  <td>
                    {!inc && <span className={styles.mutedText}>—</span>}
                    {inc && inc.sync_status === 'pending' && (
                      <span className={styles.pendingSpan}>
                        <Spinner inline size="sm" /> Creating...
                      </span>
                    )}
                    {inc && inc.sync_status === 'failed' && <Badge text="SNOW Failed" color="red" />}
                    {inc && hasLinkedIncident && (
                      <div className={styles.snowCol}>
                        <div className={styles.snowLinkRow}>
                          {inc.incident_url ? (
                            <a
                              href={inc.incident_url}
                              target="_blank"
                              rel="noreferrer"
                              className={styles.snowLink}
                            >
                              {inc.incident_number || 'Incident'} ↗
                            </a>
                          ) : (
                            <span>{inc.incident_number || 'Incident'}</span>
                          )}
                          {inc.snow_state_label && <Badge text={inc.snow_state_label} color="blue" />}
                        </div>
                        {alert.incident_drift_status === 'resolved_open_ticket' && (
                          <Badge text="Ticket Still Open" color="orange" />
                        )}
                        {alert.incident_drift_status === 'active_closed_ticket' && (
                          <Badge text="Ticket Closed" color="red" />
                        )}
                      </div>
                    )}
                  </td>

                  <td className={styles.actionsCell}>
                    <div className={styles.actionsRow}>
                      {alert.state === 'firing' && (
                        <>
                          <GatedButton
                            allowed={canTriage}
                            disabledReason={DISABLED_TRIAGE_REASON}
                            variant="primary"
                            size="sm"
                            disabled={isBusy}
                            onClick={() => onAcknowledge(alert)}
                          >
                            Acknowledge
                          </GatedButton>
                          <GatedButton
                            allowed={canTriage}
                            disabledReason={DISABLED_TRIAGE_REASON}
                            variant="secondary"
                            size="sm"
                            disabled={isBusy}
                            onClick={() => onOpenDismiss(alert)}
                          >
                            Dismiss
                          </GatedButton>
                        </>
                      )}

                      {alert.state === 'acknowledged' && hasLinkedIncident && (
                        <>
                          <GatedButton
                            allowed={canWriteIncidents}
                            disabledReason={DISABLED_INCIDENT_REASON}
                            variant="secondary"
                            size="sm"
                            disabled={isBusy}
                            onClick={() => onOpenWorkNote(alert)}
                          >
                            Add Work Note
                          </GatedButton>
                          <GatedButton
                            allowed={canWriteIncidents}
                            disabledReason={DISABLED_INCIDENT_REASON}
                            variant="secondary"
                            size="sm"
                            disabled={isBusy}
                            onClick={() => onSyncIncident(alert)}
                          >
                            Sync Status
                          </GatedButton>
                          <GatedButton
                            allowed={canTriage}
                            disabledReason={DISABLED_TRIAGE_REASON}
                            variant="secondary"
                            size="sm"
                            disabled={isBusy}
                            onClick={() => onOpenDismiss(alert)}
                          >
                            Dismiss
                          </GatedButton>
                        </>
                      )}

                      {alert.state === 'dismissed' && (
                        <GatedButton
                          allowed={canTriage}
                          disabledReason={DISABLED_TRIAGE_REASON}
                          variant="secondary"
                          size="sm"
                          disabled={isBusy}
                          onClick={() => onReopen(alert)}
                        >
                          Reopen
                        </GatedButton>
                      )}

                      {alert.state === 'resolved' &&
                        alert.incident_drift_status === 'resolved_open_ticket' &&
                        inc && (
                          <>
                            <GatedButton
                              allowed={canWriteIncidents}
                              disabledReason={DISABLED_INCIDENT_REASON}
                              variant="secondary"
                              size="sm"
                              disabled={isBusy}
                              onClick={() => onSyncIncident(alert)}
                            >
                              Refresh Status
                            </GatedButton>
                            <GatedButton
                              allowed={canWriteIncidents}
                              disabledReason={DISABLED_INCIDENT_REASON}
                              variant="primary"
                              size="sm"
                              disabled={isBusy}
                              onClick={() => onOpenWorkNote(alert)}
                            >
                              Post Work Note
                            </GatedButton>
                          </>
                        )}

                      <Button
                        variant="secondary"
                        fill="outline"
                        size="sm"
                        onClick={() => onSelectAlert(alert)}
                      >
                        Details
                      </Button>
                    </div>
                  </td>
                </tr>

                {isAckWithoutIncident && (
                  <tr className={styles.actionBarRow} data-testid={`action-bar-${alert.id}`}>
                    <td colSpan={11}>
                      <div className={styles.actionBarContent}>
                        <span className={styles.actionBarPrompt}>
                          Acknowledged by <strong>{alert.acknowledged_by || 'operator'}</strong> — next step:
                        </span>
                        <div className={styles.actionBarButtons}>
                          <GatedButton
                            allowed={canWriteIncidents}
                            disabledReason={DISABLED_INCIDENT_REASON}
                            variant="primary"
                            size="sm"
                            disabled={isBusy}
                            onClick={() => onOpenCreateIncident(alert)}
                          >
                            Create ServiceNow Incident
                          </GatedButton>
                          <GatedButton
                            allowed={canWriteIncidents}
                            disabledReason={DISABLED_INCIDENT_REASON}
                            variant="secondary"
                            size="sm"
                            disabled={isBusy}
                            onClick={() => onOpenLinkIncident(alert)}
                          >
                            Link Existing Incident
                          </GatedButton>
                          <GatedButton
                            allowed={canTriage}
                            disabledReason={DISABLED_TRIAGE_REASON}
                            variant="secondary"
                            size="sm"
                            disabled={isBusy}
                            onClick={() => onOpenAddNote(alert)}
                          >
                            Add Note
                          </GatedButton>
                          <GatedButton
                            allowed={canTriage}
                            disabledReason={DISABLED_TRIAGE_REASON}
                            variant="secondary"
                            size="sm"
                            disabled={isBusy}
                            onClick={() => onUnacknowledge(alert)}
                          >
                            Unacknowledge
                          </GatedButton>
                          <GatedButton
                            allowed={canTriage}
                            disabledReason={DISABLED_TRIAGE_REASON}
                            variant="destructive"
                            fill="outline"
                            size="sm"
                            disabled={isBusy}
                            onClick={() => onOpenDismiss(alert)}
                          >
                            Dismiss
                          </GatedButton>
                        </div>
                      </div>
                    </td>
                  </tr>
                )}
              </React.Fragment>
            );
          })}
        </tbody>
      </table>
    </div>
  );
};

interface GatedButtonProps {
  allowed: boolean;
  disabledReason: string;
  variant?: 'primary' | 'secondary' | 'destructive';
  fill?: 'solid' | 'outline' | 'text';
  size?: 'xs' | 'sm' | 'md' | 'lg';
  disabled?: boolean;
  onClick: () => void;
  children: React.ReactNode;
}

export const GatedButton: React.FC<GatedButtonProps> = ({
  allowed,
  disabledReason,
  variant = 'secondary',
  fill = 'solid',
  size = 'sm',
  disabled = false,
  onClick,
  children,
}) => {
  const btn = (
    <Button
      variant={variant}
      fill={fill}
      size={size}
      disabled={disabled || !allowed}
      onClick={onClick}
    >
      {children}
    </Button>
  );

  if (!allowed) {
    return (
      <Tooltip content={disabledReason} placement="top">
        <span>{btn}</span>
      </Tooltip>
    );
  }
  return btn;
};

export function severityColor(severity: string): 'red' | 'orange' | 'blue' | 'purple' {
  switch (severity.toLowerCase()) {
    case 'critical':
      return 'red';
    case 'warning':
      return 'orange';
    case 'info':
      return 'blue';
    default:
      return 'purple';
  }
}

export function stateColor(state: Alert['state']): 'red' | 'orange' | 'green' | 'blue' {
  switch (state) {
    case 'firing':
      return 'red';
    case 'acknowledged':
      return 'orange';
    case 'resolved':
      return 'green';
    case 'dismissed':
      return 'blue';
  }
}

export function stateLabel(state: Alert['state']): string {
  switch (state) {
    case 'firing':
      return 'Firing';
    case 'acknowledged':
      return 'Acknowledged';
    case 'dismissed':
      return 'Dismissed';
    case 'resolved':
      return 'Resolved';
  }
}

export function displayStateLabel(alert: Alert): string {
  if (alert.state === 'resolved' && alert.acknowledged_at) {
    return 'Non active Ack';
  }
  return stateLabel(alert.state);
}

export function displayStateColor(alert: Alert): 'red' | 'orange' | 'green' | 'blue' | 'purple' {
  if (alert.state === 'resolved' && alert.acknowledged_at) {
    return 'purple';
  }
  return stateColor(alert.state);
}

const getStyles = (theme: GrafanaTheme2) => ({
  emptyState: css`
    display: flex;
    align-items: center;
    justify-content: center;
    gap: ${theme.spacing(1.5)};
    padding: ${theme.spacing(6)};
    background: ${theme.colors.background.secondary};
    border: 1px solid ${theme.colors.border.weak};
    border-radius: ${theme.shape.radius.default};
    color: ${theme.colors.text.secondary};
  `,
  tableWrapper: css`
    overflow-x: auto;
    border: 1px solid ${theme.colors.border.medium};
    border-radius: ${theme.shape.radius.default};
    background: ${theme.colors.background.primary};
  `,
  table: css`
    width: 100%;
    border-collapse: collapse;
    font-size: ${theme.typography.bodySmall.fontSize};

    th {
      text-align: left;
      padding: ${theme.spacing(1)} ${theme.spacing(1.25)};
      font-size: 12px;
      font-weight: ${theme.typography.fontWeightBold};
      text-transform: uppercase;
      letter-spacing: 0.03em;
      color: ${theme.colors.text.secondary};
      background: ${theme.colors.background.secondary};
      border-bottom: 1px solid ${theme.colors.border.medium};
      border-right: 1px solid ${theme.colors.border.weak};
      white-space: nowrap;
    }

    th:last-child {
      border-right: none;
    }

    td {
      padding: ${theme.spacing(1)} ${theme.spacing(1.25)};
      border-bottom: 1px solid ${theme.colors.border.weak};
      border-right: 1px solid ${theme.colors.border.weak};
      vertical-align: middle;
    }

    td:last-child {
      border-right: none;
    }
  `,
  checkboxCell: css`
    width: 34px;
    text-align: center !important;
  `,
  row: css`
    &:hover {
      background: ${theme.colors.emphasize(theme.colors.background.primary, 0.04)};
    }
  `,
  zebraRow: css`
    background: ${theme.colors.emphasize(theme.colors.background.primary, 0.015)};
  `,
  selectedRow: css`
    background: rgba(50, 116, 217, 0.12) !important;
  `,
  rowWithActionBar: css`
    td {
      border-bottom: none;
    }
  `,
  actionBarRow: css`
    background: ${theme.colors.background.secondary};
    td {
      padding: ${theme.spacing(0.75)} ${theme.spacing(1.25)} ${theme.spacing(1.25)};
      border-bottom: 1px solid ${theme.colors.border.medium};
    }
  `,
  actionBarContent: css`
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: ${theme.spacing(1)};
    padding: ${theme.spacing(0.75)} ${theme.spacing(1.25)};
    border-left: 3px solid ${theme.colors.warning.main};
    background: ${theme.colors.background.primary};
    border-radius: ${theme.shape.radius.default};
  `,
  actionBarPrompt: css`
    font-size: ${theme.typography.bodySmall.fontSize};
    color: ${theme.colors.text.secondary};
  `,
  actionBarButtons: css`
    display: flex;
    flex-wrap: wrap;
    gap: ${theme.spacing(0.75)};
  `,
  alertCell: css`
    min-width: 220px;
    max-width: 340px;
  `,
  alertNameBtn: css`
    background: none;
    border: none;
    padding: 0;
    margin: 0;
    font-weight: ${theme.typography.fontWeightBold};
    font-size: ${theme.typography.body.fontSize};
    color: ${theme.colors.text.link};
    cursor: pointer;
    text-align: left;

    &:hover {
      text-decoration: underline;
    }
  `,
  summaryText: css`
    font-size: 12px;
    color: ${theme.colors.text.secondary};
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    max-width: 320px;
  `,
  historyLinkBtn: css`
    background: none;
    border: none;
    padding: 0;
    margin-top: 2px;
    font-size: 11px;
    color: ${theme.colors.text.link};
    cursor: pointer;
    text-decoration: underline;
  `,
  metaPill: css`
    display: inline-block;
    padding: 2px 8px;
    border-radius: ${theme.shape.radius.default};
    background: ${theme.colors.background.secondary};
    border: 1px solid ${theme.colors.border.weak};
    font-family: ${theme.typography.fontFamilyMonospace};
    font-size: 11px;
  `,
  untaggedText: css`
    font-size: 11px;
    font-style: italic;
    color: ${theme.colors.text.disabled};
  `,
  nowrapCell: css`
    white-space: nowrap;
  `,
  mutedText: css`
    font-size: 11px;
    color: ${theme.colors.text.secondary};
  `,
  pendingSpan: css`
    display: inline-flex;
    align-items: center;
    gap: ${theme.spacing(0.75)};
    color: ${theme.colors.text.secondary};
  `,
  snowCol: css`
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 3px;
  `,
  snowLinkRow: css`
    display: flex;
    align-items: center;
    gap: ${theme.spacing(0.75)};
    white-space: nowrap;
  `,
  snowLink: css`
    font-weight: ${theme.typography.fontWeightMedium};
    color: ${theme.colors.text.link};
  `,
  actionsCell: css`
    white-space: nowrap;
  `,
  actionsRow: css`
    display: flex;
    flex-wrap: nowrap;
    gap: ${theme.spacing(0.5)};
  `,
});
