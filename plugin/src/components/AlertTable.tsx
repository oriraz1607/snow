import React from 'react';
import { css } from '@emotion/css';
import { GrafanaTheme2 } from '@grafana/data';
import { Button, Spinner, Tooltip, useTheme2 } from '@grafana/ui';
import { Alert, AlertSort } from '../types/api';
import {
  formatDurationBetween,
  formatRelativeTime,
  getAlertConfluenceDoc,
  getAlertEnvironment,
  getAlertInstance,
  getAlertSite,
  getAlertTeam,
  isAlertSelectableForBulk,
} from '../utils/format';
import { RowActionMenu } from './RowActionMenu';

interface AlertTableProps {
  alerts: Alert[];
  loading: boolean;
  canTriage: boolean;
  canWriteIncidents: boolean;
  busyAlertId: string | null;
  selectedAlertIds: Set<string>;
  maxBulkSelection?: number;
  showServiceNowColumn: boolean;
  sort: AlertSort;
  onChangeSort: (nextSort: AlertSort) => void;
  onToggleSelectAlert: (alertId: string) => void;
  onToggleSelectAll: (selectableIds: string[], selectAll: boolean) => void;
  onSelectAlert: (alert: Alert) => void;
  onOpenConfluenceEdit: (alert: Alert) => void;
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

export const AlertTable: React.FC<AlertTableProps> = ({
  alerts,
  loading,
  canTriage,
  canWriteIncidents,
  busyAlertId,
  selectedAlertIds,
  maxBulkSelection = 50,
  showServiceNowColumn,
  sort,
  onChangeSort,
  onToggleSelectAlert,
  onToggleSelectAll,
  onSelectAlert,
  onOpenConfluenceEdit,
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

  const selectableIds = alerts.filter(isAlertSelectableForBulk).map((a) => a.id);
  const atSelectionCap = selectedAlertIds.size >= maxBulkSelection;
  const allSelectableChecked =
    selectableIds.length > 0 &&
    (selectableIds.every((id) => selectedAlertIds.has(id)) ||
      (atSelectionCap && selectableIds.some((id) => selectedAlertIds.has(id))));

  const getSortDirection = (ascSort: AlertSort, descSort: AlertSort): 'ascending' | 'descending' | 'none' => {
    if (sort === ascSort) {
      return 'ascending';
    }
    if (sort === descSort) {
      return 'descending';
    }
    return 'none';
  };

  const renderSortButton = (
    label: string,
    ascSort: AlertSort,
    descSort: AlertSort,
    defaultSort: AlertSort
  ) => {
    const dir = getSortDirection(ascSort, descSort);
    const nextSort = sort === defaultSort ? (defaultSort === ascSort ? descSort : ascSort) : defaultSort;
    return (
      <button
        type="button"
        className={`${styles.sortHeaderBtn} ${dir !== 'none' ? styles.sortHeaderBtnActive : ''}`}
        onClick={() => onChangeSort(nextSort)}
        data-testid={`sort-header-${label.toLowerCase()}`}
      >
        <span>{label}</span>
        <span aria-hidden="true" className={styles.sortIndicator}>
          {dir === 'ascending' ? '↑' : dir === 'descending' ? '↓' : '⇅'}
        </span>
      </button>
    );
  };

  return (
    <div className={styles.tableWrapper}>
      <table className={styles.table} aria-label="Alert queue table">
        <colgroup>
          <col style={{ width: '36px' }} />
          <col style={{ width: '96px' }} />
          <col />
          <col style={{ width: '160px' }} />
          <col style={{ width: '76px' }} />
          <col style={{ width: '100px' }} />
          <col style={{ width: '118px' }} />
          <col style={{ width: '96px' }} />
          {showServiceNowColumn && <col style={{ width: '148px' }} />}
          <col style={{ width: '48px' }} />
        </colgroup>
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
            <th aria-sort={getSortDirection('severity_asc', 'severity_desc')}>
              {renderSortButton('Severity', 'severity_asc', 'severity_desc', 'severity_desc')}
            </th>
            <th aria-sort={getSortDirection('alert_name_asc', 'alert_name_desc')}>
              {renderSortButton('Alert', 'alert_name_asc', 'alert_name_desc', 'alert_name_asc')}
            </th>
            <th aria-sort={getSortDirection('instance_asc', 'instance_desc')}>
              {renderSortButton('Instance', 'instance_asc', 'instance_desc', 'instance_asc')}
            </th>
            <th>Env</th>
            <th>Site</th>
            <th>Team</th>
            <th aria-sort={getSortDirection('starts_at_asc', 'starts_at_desc')}>
              {renderSortButton('Duration', 'starts_at_asc', 'starts_at_desc', 'starts_at_desc')}
            </th>
            {showServiceNowColumn && <th>ServiceNow</th>}
            <th className={styles.actionsHeaderCell}>
              <span className={styles.srOnly}>Actions</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {alerts.map((alert, idx) => {
            const inc = alert.servicenow_incident;
            const hasLinkedIncident =
              Boolean(inc) && (inc?.sync_status === 'created' || inc?.sync_status === 'linked');
            const isBusy = busyAlertId === alert.id;
            const selectable = isAlertSelectableForBulk(alert);
            const checked = selectedAlertIds.has(alert.id);
            const instance = getAlertInstance(alert);
            const env = getAlertEnvironment(alert);
            const site = getAlertSite(alert);
            const teamInfo = getAlertTeam(alert);
            const confluenceDoc = getAlertConfluenceDoc(alert);
            const isCritical = alert.severity.toLowerCase() === 'critical';

            return (
              <tr
                key={alert.id}
                className={`${styles.row} ${idx % 2 === 1 ? styles.zebraRow : ''} ${
                  checked ? styles.selectedRow : ''
                }`}
                data-testid={`alert-row-${alert.id}`}
              >
                <td
                  className={`${styles.checkboxCell} ${
                    isCritical ? styles.criticalStripe : styles.warningStripe
                  }`}
                >
                  <input
                    type="checkbox"
                    aria-label={`Select ${alert.alert_name}`}
                    disabled={!selectable || (!checked && atSelectionCap)}
                    title={
                      !checked && atSelectionCap
                        ? `Maximum ${maxBulkSelection} alerts can be selected per incident`
                        : undefined
                    }
                    checked={checked}
                    onChange={() => onToggleSelectAlert(alert.id)}
                  />
                </td>

                <td className={styles.nowrapCell}>
                  <span className={isCritical ? styles.sevCritical : styles.sevWarning}>
                    {isCritical ? 'Critical' : 'Warning'}
                  </span>
                </td>

                <td className={styles.alertCell}>
                  <div className={styles.alertTitleRow}>
                    <button
                      type="button"
                      className={styles.alertNameBtn}
                      title={alert.alert_name}
                      onClick={() => onSelectAlert(alert)}
                    >
                      {alert.alert_name}
                    </button>
                    {alert.previous_episode_count > 0 && (
                      <button
                        type="button"
                        className={styles.historyLinkBtn}
                        onClick={() => onFilterByFingerprint(alert.fingerprint)}
                      >
                        {alert.previous_episode_count} prior
                      </button>
                    )}
                    {confluenceDoc.url ? (
                      <a
                        href={confluenceDoc.url}
                        target="_blank"
                        rel="noopener noreferrer"
                        className={styles.confluenceLink}
                        data-testid={`confluence-link-${alert.id}`}
                        title={`Open Confluence documentation: ${confluenceDoc.url}`}
                      >
                        Docs ↗
                      </a>
                    ) : (
                      <button
                        type="button"
                        className={styles.addDocBtn}
                        disabled={!canTriage}
                        title={canTriage ? 'Link NOC Confluence documentation' : DISABLED_TRIAGE_REASON}
                        data-testid={`add-confluence-btn-${alert.id}`}
                        onClick={() => onOpenConfluenceEdit(alert)}
                      >
                        + Add Doc
                      </button>
                    )}
                  </div>
                  {alert.annotations?.summary && (
                    <div className={styles.summaryText} title={alert.annotations.summary}>
                      {alert.annotations.summary}
                    </div>
                  )}
                </td>

                <td className={styles.ellipsisCell}>
                  {instance ? (
                    <span className={styles.metaText} title={instance}>
                      {instance}
                    </span>
                  ) : (
                    <span className={styles.mutedDash}>—</span>
                  )}
                </td>

                <td className={styles.ellipsisCell}>
                  {env ? (
                    <span className={styles.metaText} title={env}>
                      {env}
                    </span>
                  ) : (
                    <span className={styles.mutedDash}>—</span>
                  )}
                </td>

                <td className={styles.ellipsisCell}>
                  {site ? (
                    <span className={styles.metaText} title={site}>
                      {site}
                    </span>
                  ) : (
                    <span className={styles.mutedDash}>—</span>
                  )}
                </td>

                <td className={styles.ellipsisCell}>
                  {teamInfo.team ? (
                    <Tooltip
                      content={
                        teamInfo.source === 'label'
                          ? 'From Prometheus team label'
                          : `Tagged by ${teamInfo.taggedBy || 'operator'}`
                      }
                      placement="top"
                    >
                      <span className={styles.teamInline}>
                        <span
                          className={`${styles.teamDot} ${
                            teamInfo.source === 'label' ? styles.teamDotLabel : styles.teamDotOperator
                          }`}
                          aria-hidden="true"
                        />
                        <span className={styles.teamText}>{teamInfo.team}</span>
                      </span>
                    </Tooltip>
                  ) : (
                    <span className={styles.mutedDash}>—</span>
                  )}
                </td>

                <td className={`${styles.nowrapCell} ${styles.durationCell}`}>
                  {alert.state === 'resolved' && alert.resolved_at ? (
                    <div>
                      <div>{formatDurationBetween(alert.starts_at, alert.resolved_at)}</div>
                      <div className={styles.mutedText}>Res {formatRelativeTime(alert.resolved_at)} ago</div>
                    </div>
                  ) : (
                    <div>
                      <div>{formatRelativeTime(alert.starts_at)}</div>
                      <div className={styles.mutedText}>v{alert.version}</div>
                    </div>
                  )}
                </td>

                {showServiceNowColumn && (
                  <td className={styles.ellipsisCell}>
                    {!inc && <span className={styles.mutedDash}>—</span>}
                    {inc && inc.sync_status === 'pending' && (
                      <span className={styles.pendingSpan}>
                        <Spinner inline size="sm" /> Creating...
                      </span>
                    )}
                    {inc && inc.sync_status === 'failed' && (
                      <span className={styles.snowFailedText}>SNOW Failed</span>
                    )}
                    {inc && hasLinkedIncident && (
                      <div className={styles.snowCol}>
                        {inc.incident_url ? (
                          <a
                            href={inc.incident_url}
                            target="_blank"
                            rel="noopener noreferrer"
                            className={styles.snowLink}
                            title={inc.incident_number || 'Incident'}
                          >
                            {inc.incident_number || 'Incident'} ↗
                          </a>
                        ) : (
                          <span className={styles.snowLink}>{inc.incident_number || 'Incident'}</span>
                        )}
                        <div className={styles.snowSubRow}>
                          {inc.snow_state_label && (
                            <span className={styles.snowStateText}>{inc.snow_state_label}</span>
                          )}
                          {alert.incident_drift_status === 'resolved_open_ticket' && (
                            <span className={styles.driftOpenText}>· Open Drift</span>
                          )}
                          {alert.incident_drift_status === 'active_closed_ticket' && (
                            <span className={styles.driftClosedText}>· Ticket Closed</span>
                          )}
                        </div>
                      </div>
                    )}
                  </td>
                )}

                <td className={styles.actionsCell}>
                  <RowActionMenu
                    alert={alert}
                    hasLinkedIncident={hasLinkedIncident}
                    canTriage={canTriage}
                    canWriteIncidents={canWriteIncidents}
                    busy={isBusy}
                    onAcknowledge={onAcknowledge}
                    onUnacknowledge={onUnacknowledge}
                    onReopen={onReopen}
                    onOpenDismiss={onOpenDismiss}
                    onOpenCreateIncident={onOpenCreateIncident}
                    onOpenLinkIncident={onOpenLinkIncident}
                    onOpenAddNote={onOpenAddNote}
                    onOpenWorkNote={onOpenWorkNote}
                    onSyncIncident={onSyncIncident}
                    onSelectAlert={onSelectAlert}
                  />
                </td>
              </tr>
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
    default:
      return 'orange';
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
  if (alert.state === 'resolved' && alert.acknowledged_at && !alert.dismissed_at) {
    return 'Non active Ack';
  }
  return stateLabel(alert.state);
}

export function displayStateColor(alert: Alert): 'red' | 'orange' | 'green' | 'blue' | 'purple' {
  if (alert.state === 'resolved' && alert.acknowledged_at && !alert.dismissed_at) {
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
    width: 100%;
    max-width: 100%;
    border: 1px solid ${theme.colors.border.medium};
    border-radius: ${theme.shape.radius.default};
    background: ${theme.colors.background.primary};
    overflow-x: visible;

    @media (max-width: 1080px) {
      overflow-x: auto;
    }
  `,
  table: css`
    width: 100%;
    table-layout: fixed;
    border-collapse: collapse;
    font-size: ${theme.typography.bodySmall.fontSize};

    @media (max-width: 1080px) {
      min-width: 980px;
    }

    th {
      position: sticky;
      top: 0;
      z-index: 2;
      text-align: left;
      padding: ${theme.spacing(1)} ${theme.spacing(1.25)};
      font-size: 11px;
      font-weight: ${theme.typography.fontWeightBold};
      text-transform: uppercase;
      letter-spacing: 0.04em;
      color: ${theme.colors.text.secondary};
      background: ${theme.colors.background.secondary};
      border-bottom: 1px solid ${theme.colors.border.medium};
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
    }

    td {
      padding: ${theme.spacing(0.875)} ${theme.spacing(1.25)};
      border-bottom: 1px solid ${theme.colors.border.weak};
      vertical-align: middle;
    }
  `,
  sortHeaderBtn: css`
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 0;
    margin: 0;
    border: none;
    background: transparent;
    color: inherit;
    font: inherit;
    text-transform: inherit;
    letter-spacing: inherit;
    cursor: pointer;
    max-width: 100%;

    &:hover {
      color: ${theme.colors.text.primary};
    }
  `,
  sortHeaderBtnActive: css`
    color: ${theme.colors.text.primary};
  `,
  sortIndicator: css`
    font-size: 10px;
    opacity: 0.75;
  `,
  checkboxCell: css`
    text-align: center !important;
    padding-left: ${theme.spacing(0.75)} !important;
    padding-right: ${theme.spacing(0.75)} !important;
  `,
  criticalStripe: css`
    box-shadow: inset 3px 0 0 ${theme.colors.error.main};
  `,
  warningStripe: css`
    box-shadow: inset 3px 0 0 ${theme.colors.warning.main};
  `,
  actionsHeaderCell: css`
    text-align: center !important;
  `,
  srOnly: css`
    position: absolute;
    width: 1px;
    height: 1px;
    padding: 0;
    margin: -1px;
    overflow: hidden;
    clip: rect(0, 0, 0, 0);
    white-space: nowrap;
    border: 0;
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
  sevCritical: css`
    font-weight: ${theme.typography.fontWeightBold};
    color: ${theme.colors.error.text};
  `,
  sevWarning: css`
    font-weight: ${theme.typography.fontWeightMedium};
    color: ${theme.colors.warning.text};
  `,
  alertCell: css`
    overflow: hidden;
  `,
  alertTitleRow: css`
    display: flex;
    align-items: baseline;
    gap: ${theme.spacing(1)};
    min-width: 0;
  `,
  alertNameBtn: css`
    background: none;
    border: none;
    padding: 0;
    margin: 0;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
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
    margin-top: 1px;
  `,
  historyLinkBtn: css`
    flex-shrink: 0;
    background: none;
    border: none;
    padding: 0;
    font-size: 11px;
    color: ${theme.colors.text.secondary};
    cursor: pointer;
    text-decoration: underline;

    &:hover {
      color: ${theme.colors.text.link};
    }
  `,
  confluenceLink: css`
    flex-shrink: 0;
    font-size: 11px;
    font-weight: ${theme.typography.fontWeightMedium};
    color: ${theme.colors.info.text};
    text-decoration: none;

    &:hover {
      text-decoration: underline;
    }
  `,
  addDocBtn: css`
    flex-shrink: 0;
    background: none;
    border: 1px dashed ${theme.colors.border.medium};
    border-radius: ${theme.shape.radius.default};
    padding: 0 5px;
    font-size: 10px;
    line-height: 16px;
    color: ${theme.colors.text.secondary};
    cursor: pointer;

    &:hover:not(:disabled) {
      color: ${theme.colors.text.link};
      border-color: ${theme.colors.primary.border};
    }

    &:disabled {
      opacity: 0.45;
      cursor: not-allowed;
    }
  `,
  ellipsisCell: css`
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  `,
  metaText: css`
    display: block;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-family: ${theme.typography.fontFamilyMonospace};
    font-size: 12px;
    color: ${theme.colors.text.secondary};
  `,
  mutedDash: css`
    color: ${theme.colors.text.disabled};
  `,
  teamInline: css`
    display: inline-flex;
    align-items: center;
    gap: 6px;
    max-width: 100%;
    overflow: hidden;
    vertical-align: middle;
  `,
  teamDot: css`
    flex-shrink: 0;
    width: 7px;
    height: 7px;
    border-radius: 50%;
  `,
  teamDotLabel: css`
    background: ${theme.colors.info.main};
  `,
  teamDotOperator: css`
    background: #a855f7;
  `,
  teamText: css`
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: ${theme.colors.text.primary};
  `,
  nowrapCell: css`
    white-space: nowrap;
  `,
  durationCell: css`
    font-variant-numeric: tabular-nums;
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
    font-size: 12px;
  `,
  snowFailedText: css`
    font-size: 12px;
    font-weight: ${theme.typography.fontWeightMedium};
    color: ${theme.colors.error.text};
  `,
  snowCol: css`
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 1px;
    min-width: 0;
  `,
  snowLink: css`
    display: block;
    max-width: 100%;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-weight: ${theme.typography.fontWeightMedium};
    color: ${theme.colors.text.link};
  `,
  snowSubRow: css`
    display: flex;
    align-items: center;
    gap: 4px;
    max-width: 100%;
    overflow: hidden;
    white-space: nowrap;
    font-size: 11px;
  `,
  snowStateText: css`
    color: ${theme.colors.text.secondary};
  `,
  driftOpenText: css`
    color: ${theme.colors.warning.text};
    font-weight: ${theme.typography.fontWeightMedium};
  `,
  driftClosedText: css`
    color: ${theme.colors.error.text};
    font-weight: ${theme.typography.fontWeightMedium};
  `,
  actionsCell: css`
    text-align: center !important;
    padding-left: ${theme.spacing(0.5)} !important;
    padding-right: ${theme.spacing(0.5)} !important;
  `,
});
