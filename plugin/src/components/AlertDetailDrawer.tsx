import React, { useCallback, useEffect, useState } from 'react';
import { css } from '@emotion/css';
import { GrafanaTheme2 } from '@grafana/data';
import {
  Alert as GrafanaAlert,
  Badge,
  Button,
  Drawer,
  Spinner,
  Tab,
  TabsBar,
  useTheme2,
} from '@grafana/ui';
import { alertApi, ApiClientError } from '../api/client';
import { Alert, AlertDetail, AlertHistoryResponse } from '../types/api';
import { formatRelativeTime, formatUtcTimestamp } from '../utils/format';
import { GatedButton, severityColor, stateColor, stateLabel } from './AlertTable';

type DrawerTab = 'overview' | 'servicenow' | 'audit' | 'episodes';

interface AlertDetailDrawerProps {
  alertId: string | null;
  canTriage: boolean;
  canWriteIncidents: boolean;
  onClose: () => void;
  onAcknowledge: (alert: Alert) => void;
  onUnacknowledge: (alert: Alert) => void;
  onReopen: (alert: Alert) => void;
  onOpenDismiss: (alert: Alert) => void;
  onOpenCreateIncident: (alert: Alert) => void;
  onOpenLinkIncident: (alert: Alert) => void;
  onOpenAddNote: (alert: Alert) => void;
  onOpenWorkNote: (alert: Alert, initialText?: string) => void;
  onSyncIncident: (alert: Alert) => Promise<void>;
  onSelectEpisodeId: (alertId: string) => void;
  refreshTrigger: number;
}

const DISABLED_TRIAGE_REASON = 'Requires Editor or Admin role (alerts:triage permission)';
const DISABLED_INCIDENT_REASON = 'Requires Editor or Admin role (incidents:write permission)';

export const AlertDetailDrawer: React.FC<AlertDetailDrawerProps> = ({
  alertId,
  canTriage,
  canWriteIncidents,
  onClose,
  onAcknowledge,
  onUnacknowledge,
  onReopen,
  onOpenDismiss,
  onOpenCreateIncident,
  onOpenLinkIncident,
  onOpenAddNote,
  onOpenWorkNote,
  onSyncIncident,
  onSelectEpisodeId,
  refreshTrigger,
}) => {
  const theme = useTheme2();
  const styles = getStyles(theme);

  const [activeTab, setActiveTab] = useState<DrawerTab>('overview');
  const [detail, setDetail] = useState<AlertDetail | null>(null);
  const [history, setHistory] = useState<AlertHistoryResponse | null>(null);
  const [loading, setLoading] = useState(false);
  const [syncing, setSyncing] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const loadDrawerData = useCallback(async (id: string, signal?: AbortSignal) => {
    setLoading(true);
    setError(null);
    try {
      const [detailResp, historyResp] = await Promise.all([
        alertApi.getAlert(id, signal),
        alertApi.getAlertHistory(id, signal),
      ]);
      if (!signal?.aborted) {
        setDetail(detailResp);
        setHistory(historyResp);
      }
    } catch (err) {
      if (err instanceof DOMException && err.name === 'AbortError') {
        return;
      }
      const apiErr = err as ApiClientError;
      setError(apiErr?.message || 'Failed to load alert details.');
    } finally {
      if (!signal?.aborted) {
        setLoading(false);
      }
    }
  }, []);

  useEffect(() => {
    if (!alertId) {
      setDetail(null);
      setHistory(null);
      setActiveTab('overview');
      return;
    }
    const controller = new AbortController();
    void loadDrawerData(alertId, controller.signal);
    return () => controller.abort();
  }, [alertId, refreshTrigger, loadDrawerData]);

  if (!alertId) {
    return null;
  }

  const handleManualSync = async () => {
    if (!detail || syncing) {
      return;
    }
    setSyncing(true);
    try {
      await onSyncIncident(detail);
      await loadDrawerData(detail.id);
    } finally {
      setSyncing(false);
    }
  };

  const inc = detail?.servicenow_incident ?? null;
  const hasLinkedIncident =
    Boolean(inc) && (inc?.sync_status === 'created' || inc?.sync_status === 'linked');

  const resolvedNudgeText = detail
    ? `Alert resolved in Prometheus at ${formatUtcTimestamp(detail.resolved_at || detail.ends_at)}. Please verify recovery and resolve/close this incident.`
    : '';

  return (
    <Drawer
      title={detail ? `${detail.alert_name}` : 'Alert Details'}
      subtitle={detail ? `Fingerprint ${detail.fingerprint} · Version ${detail.version}` : undefined}
      onClose={onClose}
      size="md"
    >
      {loading && !detail && (
        <div className={styles.centerBox}>
          <Spinner size="lg" />
          <span>Loading alert details...</span>
        </div>
      )}

      {error && (
        <div className={styles.section}>
          <GrafanaAlert severity="error" title="Error loading alert">
            {error}
          </GrafanaAlert>
        </div>
      )}

      {detail && (
        <div className={styles.container}>
          <div className={styles.statusHeader}>
            <Badge text={detail.severity} color={severityColor(detail.severity)} />
            <Badge text={stateLabel(detail.state)} color={stateColor(detail.state)} />
            {detail.acknowledged_by && (
              <span className={styles.metaText}>
                Acked by <strong>{detail.acknowledged_by}</strong> ({formatRelativeTime(detail.acknowledged_at)} ago)
              </span>
            )}
          </div>

          {detail.incident_drift_status === 'resolved_open_ticket' && inc && (
            <div className={styles.section} data-testid="drawer-drift-banner">
              <GrafanaAlert severity="warning" title="Alert Resolved — ServiceNow Incident Still Open">
                <p>
                  Alert stopped firing at <strong>{formatUtcTimestamp(detail.resolved_at || detail.ends_at)}</strong> (
                  {formatRelativeTime(detail.resolved_at || detail.ends_at)} ago), but ServiceNow incident{' '}
                  <strong>{inc.incident_number}</strong> is still{' '}
                  <strong>{inc.snow_state_label || 'Open'}</strong> (last synced{' '}
                  {inc.snow_state_synced_at ? `${formatRelativeTime(inc.snow_state_synced_at)} ago` : 'recently'}).
                </p>
                <div className={styles.bannerActions}>
                  <GatedButton
                    allowed={canWriteIncidents}
                    disabledReason={DISABLED_INCIDENT_REASON}
                    variant="secondary"
                    size="sm"
                    disabled={syncing}
                    onClick={handleManualSync}
                  >
                    {syncing ? 'Refreshing...' : 'Refresh Status'}
                  </GatedButton>
                  <GatedButton
                    allowed={canWriteIncidents}
                    disabledReason={DISABLED_INCIDENT_REASON}
                    variant="primary"
                    size="sm"
                    onClick={() => onOpenWorkNote(detail, resolvedNudgeText)}
                  >
                    Post Resolution Nudge
                  </GatedButton>
                  {inc.incident_url && (
                    <a
                      href={inc.incident_url}
                      target="_blank"
                      rel="noreferrer"
                      className={styles.externalLinkBtn}
                    >
                      Open in ServiceNow ↗
                    </a>
                  )}
                </div>
              </GrafanaAlert>
            </div>
          )}

          {detail.incident_drift_status === 'active_closed_ticket' && inc && (
            <div className={styles.section}>
              <GrafanaAlert severity="error" title="Alert Firing — ServiceNow Incident Closed">
                Alert is still active while ServiceNow incident <strong>{inc.incident_number}</strong> is{' '}
                <strong>{inc.snow_state_label || 'Closed'}</strong>.
              </GrafanaAlert>
            </div>
          )}

          {detail.state === 'acknowledged' && !hasLinkedIncident && (
            <div className={styles.pinnedBar} data-testid="drawer-action-bar">
              <GatedButton
                allowed={canWriteIncidents}
                disabledReason={DISABLED_INCIDENT_REASON}
                variant="primary"
                size="sm"
                onClick={() => onOpenCreateIncident(detail)}
              >
                Create ServiceNow Incident
              </GatedButton>
              <GatedButton
                allowed={canWriteIncidents}
                disabledReason={DISABLED_INCIDENT_REASON}
                variant="secondary"
                size="sm"
                onClick={() => onOpenLinkIncident(detail)}
              >
                Link Existing Incident
              </GatedButton>
              <GatedButton
                allowed={canTriage}
                disabledReason={DISABLED_TRIAGE_REASON}
                variant="secondary"
                size="sm"
                onClick={() => onOpenAddNote(detail)}
              >
                Add Note
              </GatedButton>
              <GatedButton
                allowed={canTriage}
                disabledReason={DISABLED_TRIAGE_REASON}
                variant="secondary"
                size="sm"
                onClick={() => onUnacknowledge(detail)}
              >
                Unacknowledge
              </GatedButton>
              <GatedButton
                allowed={canTriage}
                disabledReason={DISABLED_TRIAGE_REASON}
                variant="destructive"
                fill="outline"
                size="sm"
                onClick={() => onOpenDismiss(detail)}
              >
                Dismiss
              </GatedButton>
            </div>
          )}

          {detail.state === 'firing' && (
            <div className={styles.pinnedBar}>
              <GatedButton
                allowed={canTriage}
                disabledReason={DISABLED_TRIAGE_REASON}
                variant="primary"
                size="sm"
                onClick={() => onAcknowledge(detail)}
              >
                Acknowledge
              </GatedButton>
              <GatedButton
                allowed={canTriage}
                disabledReason={DISABLED_TRIAGE_REASON}
                variant="secondary"
                size="sm"
                onClick={() => onOpenAddNote(detail)}
              >
                Add Note
              </GatedButton>
              <GatedButton
                allowed={canTriage}
                disabledReason={DISABLED_TRIAGE_REASON}
                variant="destructive"
                fill="outline"
                size="sm"
                onClick={() => onOpenDismiss(detail)}
              >
                Dismiss
              </GatedButton>
            </div>
          )}

          {detail.state === 'dismissed' && (
            <div className={styles.pinnedBar}>
              <GatedButton
                allowed={canTriage}
                disabledReason={DISABLED_TRIAGE_REASON}
                variant="secondary"
                size="sm"
                onClick={() => onReopen(detail)}
              >
                Reopen Alert
              </GatedButton>
            </div>
          )}

          <TabsBar className={styles.tabsBar}>
            <Tab label="Overview" active={activeTab === 'overview'} onChangeTab={() => setActiveTab('overview')} />
            <Tab
              label="ServiceNow"
              active={activeTab === 'servicenow'}
              onChangeTab={() => setActiveTab('servicenow')}
            />
            <Tab
              label={`Audit Timeline (${history?.audit_events.length ?? detail.recent_audit_events.length})`}
              active={activeTab === 'audit'}
              onChangeTab={() => setActiveTab('audit')}
            />
            <Tab
              label={`Past Episodes (${history?.previous_episodes.length ?? detail.previous_episode_count})`}
              active={activeTab === 'episodes'}
              onChangeTab={() => setActiveTab('episodes')}
            />
          </TabsBar>

          {activeTab === 'overview' && (
            <div className={styles.tabBody}>
              <div className={styles.kvGrid}>
                <KvRow label="Starts At (UTC)" value={formatUtcTimestamp(detail.starts_at)} />
                <KvRow label="Resolved At (UTC)" value={formatUtcTimestamp(detail.resolved_at)} />
                <KvRow label="Summary" value={detail.annotations?.summary || '—'} />
                <KvRow label="Description" value={detail.annotations?.description || '—'} />
                <KvRow label="Receiver" value={detail.receiver || '—'} />
                <KvRow label="Deliveries" value={String(detail.delivery_count ?? 1)} />
              </div>

              <div className={styles.linksRow}>
                {detail.generator_url && (
                  <a href={detail.generator_url} target="_blank" rel="noreferrer">
                    Open in Prometheus ↗
                  </a>
                )}
                {detail.annotations?.runbook_url && (
                  <a href={detail.annotations.runbook_url} target="_blank" rel="noreferrer">
                    Open Runbook ↗
                  </a>
                )}
              </div>

              <h4 className={styles.subheading}>Labels</h4>
              <table className={styles.subTable}>
                <tbody>
                  {Object.entries(detail.labels ?? {})
                    .sort(([a], [b]) => a.localeCompare(b))
                    .map(([k, v]) => (
                      <tr key={k}>
                        <td className={styles.labelKey}>{k}</td>
                        <td>{v}</td>
                      </tr>
                    ))}
                </tbody>
              </table>
            </div>
          )}

          {activeTab === 'servicenow' && (
            <div className={styles.tabBody}>
              {!inc ? (
                <div className={styles.emptyTab}>
                  <p>No ServiceNow incident is linked to this alert episode.</p>
                  {detail.state === 'acknowledged' && (
                    <div className={styles.bannerActions}>
                      <GatedButton
                        allowed={canWriteIncidents}
                        disabledReason={DISABLED_INCIDENT_REASON}
                        variant="primary"
                        size="sm"
                        onClick={() => onOpenCreateIncident(detail)}
                      >
                        Create ServiceNow Incident
                      </GatedButton>
                      <GatedButton
                        allowed={canWriteIncidents}
                        disabledReason={DISABLED_INCIDENT_REASON}
                        variant="secondary"
                        size="sm"
                        onClick={() => onOpenLinkIncident(detail)}
                      >
                        Link Existing Incident
                      </GatedButton>
                    </div>
                  )}
                </div>
              ) : (
                <>
                  <div className={styles.kvGrid}>
                    <KvRow label="Incident Number" value={inc.incident_number || 'Pending'} />
                    <KvRow label="Sys ID" value={inc.snow_sys_id || '—'} />
                    <KvRow label="Sync Status" value={inc.sync_status} />
                    <KvRow
                      label="ServiceNow State"
                      value={inc.snow_state_label ? `${inc.snow_state_label} (${inc.snow_state ?? ''})` : '—'}
                    />
                    <KvRow label="Priority" value={inc.priority ? `P${inc.priority}` : '—'} />
                    <KvRow label="Assignment Group" value={inc.assignment_group || '—'} />
                    <KvRow label="Caller Mode" value={inc.caller_mode} />
                    <KvRow label="Caller Resolution" value={inc.caller_resolution_status} />
                    {inc.caller_fallback_reason && (
                      <KvRow label="Fallback Reason" value={inc.caller_fallback_reason} />
                    )}
                    <KvRow label="Last Synced (UTC)" value={formatUtcTimestamp(inc.snow_state_synced_at)} />
                    {inc.last_error && <KvRow label="Last Error" value={inc.last_error} />}
                  </div>

                  <div className={styles.bannerActions}>
                    <GatedButton
                      allowed={canWriteIncidents}
                      disabledReason={DISABLED_INCIDENT_REASON}
                      variant="secondary"
                      size="sm"
                      disabled={syncing}
                      onClick={handleManualSync}
                    >
                      {syncing ? 'Refreshing...' : 'Refresh Status'}
                    </GatedButton>
                    <GatedButton
                      allowed={canWriteIncidents}
                      disabledReason={DISABLED_INCIDENT_REASON}
                      variant="primary"
                      size="sm"
                      onClick={() => onOpenWorkNote(detail)}
                    >
                      Post Work Note
                    </GatedButton>
                    {inc.incident_url && (
                      <a href={inc.incident_url} target="_blank" rel="noreferrer" className={styles.externalLinkBtn}>
                        Open in ServiceNow ↗
                      </a>
                    )}
                  </div>
                </>
              )}
            </div>
          )}

          {activeTab === 'audit' && (
            <div className={styles.tabBody}>
              <div className={styles.auditHeader}>
                <GatedButton
                  allowed={canTriage}
                  disabledReason={DISABLED_TRIAGE_REASON}
                  variant="secondary"
                  size="sm"
                  onClick={() => onOpenAddNote(detail)}
                >
                  Add Triage Note
                </GatedButton>
              </div>
              <div className={styles.timeline}>
                {(history?.audit_events ?? detail.recent_audit_events).map((ev) => (
                  <div key={ev.id} className={styles.timelineItem}>
                    <div className={styles.timelineTop}>
                      <strong>{ev.event_type}</strong>
                      <span className={styles.metaText}>{formatUtcTimestamp(ev.created_at)}</span>
                    </div>
                    <div className={styles.metaText}>
                      Actor: {ev.actor_login} ({ev.actor_type})
                      {ev.actor_email ? ` · ${ev.actor_email}` : ''}
                      {ev.previous_state || ev.new_state
                        ? ` · ${ev.previous_state ?? 'none'} → ${ev.new_state ?? 'none'}`
                        : ''}
                    </div>
                    {ev.comment && <div className={styles.commentBox}>{ev.comment}</div>}
                  </div>
                ))}
              </div>
            </div>
          )}

          {activeTab === 'episodes' && (
            <div className={styles.tabBody}>
              {(history?.previous_episodes ?? []).length === 0 ? (
                <div className={styles.emptyTab}>No earlier episodes recorded for this fingerprint.</div>
              ) : (
                <table className={styles.subTable}>
                  <thead>
                    <tr>
                      <th>Started</th>
                      <th>State</th>
                      <th>Incident</th>
                      <th></th>
                    </tr>
                  </thead>
                  <tbody>
                    {history?.previous_episodes.map((ep) => (
                      <tr key={ep.id}>
                        <td>{formatUtcTimestamp(ep.starts_at)}</td>
                        <td>
                          <Badge text={stateLabel(ep.state)} color={stateColor(ep.state)} />
                        </td>
                        <td>
                          {ep.incident_number
                            ? `${ep.incident_number} (${ep.snow_state_label || 'Unknown'})`
                            : '—'}
                        </td>
                        <td>
                          <Button variant="secondary" fill="text" size="sm" onClick={() => onSelectEpisodeId(ep.id)}>
                            Open
                          </Button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </div>
          )}
        </div>
      )}
    </Drawer>
  );
};

const KvRow: React.FC<{ label: string; value: string }> = ({ label, value }) => (
  <div style={{ display: 'contents' }}>
    <div style={{ fontWeight: 500, opacity: 0.75 }}>{label}</div>
    <div style={{ wordBreak: 'break-word', whiteSpace: 'pre-wrap' }}>{value}</div>
  </div>
);

const getStyles = (theme: GrafanaTheme2) => ({
  centerBox: css`
    display: flex;
    align-items: center;
    justify-content: center;
    gap: ${theme.spacing(1.5)};
    padding: ${theme.spacing(5)};
  `,
  container: css`
    display: flex;
    flex-direction: column;
    gap: ${theme.spacing(1.5)};
  `,
  statusHeader: css`
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: ${theme.spacing(1)};
  `,
  metaText: css`
    font-size: ${theme.typography.bodySmall.fontSize};
    color: ${theme.colors.text.secondary};
  `,
  section: css`
    margin-bottom: ${theme.spacing(0.5)};
  `,
  pinnedBar: css`
    display: flex;
    flex-wrap: wrap;
    gap: ${theme.spacing(1)};
    padding: ${theme.spacing(1.25)};
    background: ${theme.colors.background.secondary};
    border: 1px solid ${theme.colors.border.weak};
    border-radius: ${theme.shape.radius.default};
  `,
  bannerActions: css`
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: ${theme.spacing(1)};
    margin-top: ${theme.spacing(1)};
  `,
  externalLinkBtn: css`
    display: inline-flex;
    align-items: center;
    padding: 4px 10px;
    border-radius: ${theme.shape.radius.default};
    border: 1px solid ${theme.colors.border.medium};
    color: ${theme.colors.text.link};
    font-size: ${theme.typography.bodySmall.fontSize};
    text-decoration: none;
  `,
  tabsBar: css`
    margin-top: ${theme.spacing(0.5)};
  `,
  tabBody: css`
    display: flex;
    flex-direction: column;
    gap: ${theme.spacing(1.5)};
    padding-top: ${theme.spacing(1)};
  `,
  kvGrid: css`
    display: grid;
    grid-template-columns: 150px 1fr;
    gap: ${theme.spacing(1)} ${theme.spacing(1.5)};
    font-size: ${theme.typography.bodySmall.fontSize};
  `,
  linksRow: css`
    display: flex;
    gap: ${theme.spacing(2)};
  `,
  subheading: css`
    margin: ${theme.spacing(1)} 0 ${theme.spacing(0.5)};
    font-size: ${theme.typography.h5.fontSize};
  `,
  subTable: css`
    width: 100%;
    border-collapse: collapse;
    font-size: ${theme.typography.bodySmall.fontSize};

    th,
    td {
      text-align: left;
      padding: ${theme.spacing(0.75)} ${theme.spacing(1)};
      border-bottom: 1px solid ${theme.colors.border.weak};
    }
  `,
  labelKey: css`
    font-family: ${theme.typography.fontFamilyMonospace};
    color: ${theme.colors.text.secondary};
    width: 180px;
  `,
  emptyTab: css`
    padding: ${theme.spacing(2)};
    color: ${theme.colors.text.secondary};
  `,
  auditHeader: css`
    display: flex;
    justify-content: flex-end;
  `,
  timeline: css`
    display: flex;
    flex-direction: column;
    gap: ${theme.spacing(1)};
  `,
  timelineItem: css`
    padding: ${theme.spacing(1.25)};
    border: 1px solid ${theme.colors.border.weak};
    border-left: 3px solid ${theme.colors.primary.main};
    border-radius: ${theme.shape.radius.default};
    background: ${theme.colors.background.secondary};
  `,
  timelineTop: css`
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: ${theme.spacing(1)};
    margin-bottom: ${theme.spacing(0.25)};
  `,
  commentBox: css`
    margin-top: ${theme.spacing(0.75)};
    padding: ${theme.spacing(0.75)};
    background: ${theme.colors.background.primary};
    border-radius: ${theme.shape.radius.default};
    white-space: pre-wrap;
    font-size: ${theme.typography.bodySmall.fontSize};
  `,
});
