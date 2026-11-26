import React, { useCallback, useEffect, useState } from 'react';
import { css } from '@emotion/css';
import { GrafanaTheme2 } from '@grafana/data';
import {
  Alert as GrafanaAlert,
  Badge,
  Button,
  Drawer,
  Input,
  Spinner,
  Tab,
  TabsBar,
  useTheme2,
} from '@grafana/ui';
import { alertApi, ApiClientError } from '../api/client';
import {
  Alert,
  AlertDetail,
  AlertHistoryResponse,
  ServiceNowMetadataResponse,
} from '../types/api';
import {
  formatDurationBetween,
  formatDurationSeconds,
  formatRelativeTime,
  formatUtcTimestamp,
  getAlertEnvironment,
  getAlertSite,
  getAlertTeam,
  getOpenIncidentDismissBlockReason,
} from '../utils/format';
import {
  displayStateColor,
  displayStateLabel,
  GatedButton,
  severityColor,
  stateColor,
  stateLabel,
} from './AlertTable';

type DrawerTab = 'overview' | 'servicenow' | 'audit' | 'episodes';

interface AlertDetailDrawerProps {
  alertId: string | null;
  metadata: ServiceNowMetadataResponse | null;
  canTriage: boolean;
  canWriteIncidents: boolean;
  onClose: () => void;
  onAlertUpdated: (updated: Alert) => void;
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
  metadata,
  canTriage,
  canWriteIncidents,
  onClose,
  onAlertUpdated,
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
  const [loadingMoreEpisodes, setLoadingMoreEpisodes] = useState(false);
  const [syncing, setSyncing] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const [editingTeam, setEditingTeam] = useState(false);
  const [teamInput, setTeamInput] = useState('');
  const [savingTeam, setSavingTeam] = useState(false);
  const [teamError, setTeamError] = useState<string | null>(null);

  const loadDrawerData = useCallback(async (id: string, signal?: AbortSignal) => {
    setLoading(true);
    setError(null);
    try {
      const [detailResp, historyResp] = await Promise.all([
        alertApi.getAlert(id, signal),
        alertApi.getAlertHistory(id, signal, { limit: 20, offset: 0 }),
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
      setEditingTeam(false);
      setTeamInput('');
      setTeamError(null);
      return;
    }
    setEditingTeam(false);
    setTeamError(null);
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

  const handleSaveTeam = async (teamValue: string) => {
    if (!detail || savingTeam) {
      return;
    }
    const trimmed = teamValue.trim();
    if (!trimmed) {
      return;
    }
    setSavingTeam(true);
    setTeamError(null);
    try {
      const updated = await alertApi.tagAlertTeam(detail.id, { team: trimmed });
      onAlertUpdated(updated);
      setEditingTeam(false);
      setTeamInput('');
      await loadDrawerData(detail.id);
    } catch (err) {
      const apiErr = err as ApiClientError;
      setTeamError(apiErr?.message || 'Failed to tag team on alert.');
    } finally {
      setSavingTeam(false);
    }
  };

  const handleLoadMoreEpisodes = async () => {
    if (!detail || !history || loadingMoreEpisodes || !history.has_more) {
      return;
    }
    setLoadingMoreEpisodes(true);
    try {
      const nextResp = await alertApi.getAlertHistory(detail.id, undefined, {
        limit: 20,
        offset: history.previous_episodes.length,
      });
      setHistory((prev) => {
        if (!prev) {
          return nextResp;
        }
        const seen = new Set(prev.previous_episodes.map((ep) => ep.id));
        const appended = (nextResp.previous_episodes ?? []).filter((ep) => !seen.has(ep.id));
        return {
          ...prev,
          previous_episodes: [...prev.previous_episodes, ...appended],
          previous_episodes_total:
            nextResp.previous_episodes_total ?? prev.previous_episodes_total,
          has_more: nextResp.has_more ?? false,
        };
      });
    } finally {
      setLoadingMoreEpisodes(false);
    }
  };

  const inc = detail?.servicenow_incident ?? null;
  const hasLinkedIncident =
    Boolean(inc) && (inc?.sync_status === 'created' || inc?.sync_status === 'linked');
  const openIncidentDismissReason = detail ? getOpenIncidentDismissBlockReason(detail) : undefined;
  const totalPreviousEpisodes =
    history?.previous_episodes_total ??
    Math.max(history?.previous_episodes.length ?? 0, detail?.previous_episode_count ?? 0);

  const resolvedNudgeText = detail
    ? `Alert resolved in Prometheus at ${formatUtcTimestamp(detail.resolved_at || detail.ends_at)}. Please verify recovery and resolve/close this incident.`
    : '';

  const teamInfo = detail ? getAlertTeam(detail) : { team: '', source: 'none' as const, taggedBy: null };
  const suggestedTeams = Array.from(
    new Set([
      'observability',
      ...(metadata?.assignment_groups.map((g) => g.name) ?? []),
      'Platform Engineering',
      'Database SRE',
      'Infrastructure',
    ])
  );

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
            <Badge text={displayStateLabel(detail)} color={displayStateColor(detail)} />
            {getAlertEnvironment(detail) && (
              <span className={styles.metaChip}>env: {getAlertEnvironment(detail)}</span>
            )}
            {getAlertSite(detail) && <span className={styles.metaChip}>site: {getAlertSite(detail)}</span>}
            {detail.dismissed_by && (
              <span className={styles.metaText} data-testid="drawer-dismissed-by">
                Dismissed by <strong>{detail.dismissed_by}</strong> ({formatRelativeTime(detail.dismissed_at)} ago)
              </span>
            )}
            {detail.acknowledged_by && (
              <span className={styles.metaText}>
                Acked by <strong>{detail.acknowledged_by}</strong> ({formatRelativeTime(detail.acknowledged_at)} ago)
              </span>
            )}
          </div>

          <div className={styles.teamCard} data-testid="drawer-team-section">
            <div className={styles.teamHeaderRow}>
              <div>
                <div className={styles.teamLabel}>Connected Team</div>
                {teamInfo.source === 'label' && (
                  <div className={styles.teamValueRow}>
                    <Badge text={teamInfo.team} color="blue" />
                    <span className={styles.metaText}>
                      From Prometheus label (<code>team={teamInfo.team}</code>)
                    </span>
                  </div>
                )}
                {teamInfo.source === 'operator' && (
                  <div className={styles.teamValueRow}>
                    <Badge text={teamInfo.team} color="purple" />
                    <span className={styles.metaText}>
                      Tagged by <strong>{teamInfo.taggedBy || 'operator'}</strong> · inherited on future re-fires
                    </span>
                  </div>
                )}
                {teamInfo.source === 'none' && (
                  <div className={styles.teamMissingText}>
                    No team connected via alert labels (<code>team=...</code> missing). Tag the owning team below:
                  </div>
                )}
              </div>

              {teamInfo.source === 'operator' && !editingTeam && (
                <GatedButton
                  allowed={canTriage}
                  disabledReason={DISABLED_TRIAGE_REASON}
                  variant="secondary"
                  size="sm"
                  onClick={() => {
                    setTeamInput(teamInfo.team);
                    setEditingTeam(true);
                  }}
                >
                  Change Team
                </GatedButton>
              )}
            </div>

            {(teamInfo.source === 'none' || editingTeam) && (
              <div className={styles.teamFormBox}>
                {teamError && (
                  <div style={{ marginBottom: '8px' }}>
                    <GrafanaAlert severity="error" title="Unable to save team">
                      {teamError}
                    </GrafanaAlert>
                  </div>
                )}
                <div className={styles.teamInputRow}>
                  <Input
                    aria-label="Tag team"
                    placeholder="Enter team name (e.g., observability)"
                    value={teamInput}
                    disabled={!canTriage || savingTeam}
                    onChange={(e) => setTeamInput(e.currentTarget.value)}
                    list="alert-team-suggestions"
                  />
                  <datalist id="alert-team-suggestions">
                    {suggestedTeams.map((t) => (
                      <option key={t} value={t} />
                    ))}
                  </datalist>
                  <GatedButton
                    allowed={canTriage}
                    disabledReason={DISABLED_TRIAGE_REASON}
                    variant="primary"
                    size="sm"
                    disabled={savingTeam || !teamInput.trim()}
                    onClick={() => void handleSaveTeam(teamInput)}
                  >
                    {savingTeam ? 'Saving...' : 'Tag Team'}
                  </GatedButton>
                  {editingTeam && (
                    <Button
                      variant="secondary"
                      size="sm"
                      disabled={savingTeam}
                      onClick={() => {
                        setEditingTeam(false);
                        setTeamError(null);
                      }}
                    >
                      Cancel
                    </Button>
                  )}
                </div>
                <div className={styles.quickTeamChips}>
                  <span className={styles.metaText}>Quick select:</span>
                  {suggestedTeams.slice(0, 5).map((t) => (
                    <button
                      key={t}
                      type="button"
                      className={styles.quickTeamChip}
                      disabled={!canTriage || savingTeam}
                      onClick={() => setTeamInput(t)}
                    >
                      {t}
                    </button>
                  ))}
                </div>
              </div>
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
                allowed={canTriage && !openIncidentDismissReason}
                disabledReason={
                  !canTriage
                    ? DISABLED_TRIAGE_REASON
                    : openIncidentDismissReason || DISABLED_TRIAGE_REASON
                }
                variant="destructive"
                fill="outline"
                size="sm"
                onClick={() => onOpenDismiss(detail)}
              >
                Dismiss
              </GatedButton>
            </div>
          )}

          {detail.state === 'acknowledged' && hasLinkedIncident && (
            <div className={styles.pinnedBar} data-testid="drawer-action-bar">
              <GatedButton
                allowed={canWriteIncidents}
                disabledReason={DISABLED_INCIDENT_REASON}
                variant="primary"
                size="sm"
                onClick={() => onOpenWorkNote(detail)}
              >
                Post Work Note
              </GatedButton>
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
                allowed={canTriage}
                disabledReason={DISABLED_TRIAGE_REASON}
                variant="secondary"
                size="sm"
                onClick={() => onOpenAddNote(detail)}
              >
                Add Note
              </GatedButton>
              <GatedButton
                allowed={canTriage && !openIncidentDismissReason}
                disabledReason={
                  !canTriage
                    ? DISABLED_TRIAGE_REASON
                    : openIncidentDismissReason || DISABLED_TRIAGE_REASON
                }
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
                allowed={canTriage && !openIncidentDismissReason}
                disabledReason={
                  !canTriage
                    ? DISABLED_TRIAGE_REASON
                    : openIncidentDismissReason || DISABLED_TRIAGE_REASON
                }
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
              label={`Previous Occurrences (${totalPreviousEpisodes})`}
              active={activeTab === 'episodes'}
              onChangeTab={() => setActiveTab('episodes')}
            />
          </TabsBar>

          {activeTab === 'overview' && (
            <div className={styles.tabBody}>
              <div className={styles.kvGrid}>
                <KvRow label="Environment" value={getAlertEnvironment(detail) || '—'} />
                <KvRow label="Site" value={getAlertSite(detail) || '—'} />
                <KvRow label="Team" value={teamInfo.team || 'Untagged'} />
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

              <div className={styles.prevOccurrencesCard} data-testid="drawer-previous-occurrences">
                <div className={styles.prevOccurrencesHeader}>
                  <h4 className={styles.subheading} style={{ margin: 0 }}>
                    Previous Occurrences ({totalPreviousEpisodes})
                  </h4>
                  {(history?.previous_episodes.length ?? 0) > 0 && (
                    <Button
                      variant="secondary"
                      fill="text"
                      size="sm"
                      onClick={() => setActiveTab('episodes')}
                    >
                      View full history →
                    </Button>
                  )}
                </div>
                {(history?.previous_episodes ?? []).length === 0 ? (
                  <div className={styles.metaText}>
                    No prior firings recorded for this alert fingerprint.
                  </div>
                ) : (
                  <div className={styles.occurrenceList}>
                    {(history?.previous_episodes ?? []).slice(0, 3).map((ep) => {
                      const epDuration =
                        typeof ep.duration_seconds === 'number'
                          ? formatDurationSeconds(ep.duration_seconds)
                          : formatDurationBetween(ep.starts_at, ep.resolved_at || ep.ends_at);
                      return (
                        <div key={ep.id} className={styles.occurrenceItem}>
                          <div className={styles.occurrenceTop}>
                            <div className={styles.occurrenceBadges}>
                              <Badge text={stateLabel(ep.state)} color={stateColor(ep.state)} />
                              <span className={styles.metaText}>{epDuration}</span>
                            </div>
                            <span className={styles.metaText}>{formatUtcTimestamp(ep.starts_at)}</span>
                          </div>
                          <div className={styles.occurrenceMeta}>
                            {ep.acknowledged_by && <span>Acked by {ep.acknowledged_by}</span>}
                            {ep.dismissed_by && (
                              <span>
                                Dismissed by {ep.dismissed_by}
                                {ep.dismiss_reason ? ` (${ep.dismiss_reason})` : ''}
                              </span>
                            )}
                            {ep.incident_number && (
                              <span>
                                {ep.incident_url ? (
                                  <a href={ep.incident_url} target="_blank" rel="noreferrer">
                                    {ep.incident_number} ↗
                                  </a>
                                ) : (
                                  ep.incident_number
                                )}{' '}
                                ({ep.snow_state_label || 'Unknown'})
                              </span>
                            )}
                            <Button
                              variant="secondary"
                              fill="text"
                              size="sm"
                              onClick={() => onSelectEpisodeId(ep.id)}
                            >
                              Open
                            </Button>
                          </div>
                        </div>
                      );
                    })}
                  </div>
                )}
              </div>
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
            <div className={styles.tabBody} data-testid="drawer-episodes-tab">
              {(history?.previous_episodes ?? []).length === 0 ? (
                <div className={styles.emptyTab}>No earlier episodes recorded for this fingerprint.</div>
              ) : (
                <>
                  <div className={styles.occurrenceList}>
                    {history?.previous_episodes.map((ep) => {
                      const epDuration =
                        typeof ep.duration_seconds === 'number'
                          ? formatDurationSeconds(ep.duration_seconds)
                          : formatDurationBetween(ep.starts_at, ep.resolved_at || ep.ends_at);
                      return (
                        <div key={ep.id} className={styles.occurrenceItem}>
                          <div className={styles.occurrenceTop}>
                            <div className={styles.occurrenceBadges}>
                              {ep.severity && (
                                <Badge text={ep.severity} color={severityColor(ep.severity)} />
                              )}
                              <Badge text={stateLabel(ep.state)} color={stateColor(ep.state)} />
                              <span className={styles.metaText}>Duration: {epDuration}</span>
                            </div>
                            <Button
                              variant="secondary"
                              fill="text"
                              size="sm"
                              onClick={() => onSelectEpisodeId(ep.id)}
                            >
                              Open
                            </Button>
                          </div>
                          <div className={styles.metaText}>
                            Started: {formatUtcTimestamp(ep.starts_at)}
                            {ep.resolved_at ? ` · Resolved: ${formatUtcTimestamp(ep.resolved_at)}` : ''}
                          </div>
                          {(ep.acknowledged_by || ep.dismissed_by) && (
                            <div className={styles.metaText}>
                              {ep.acknowledged_by ? `Acked by ${ep.acknowledged_by}` : ''}
                              {ep.acknowledged_by && ep.dismissed_by ? ' · ' : ''}
                              {ep.dismissed_by
                                ? `Dismissed by ${ep.dismissed_by}${ep.dismiss_reason ? ` — ${ep.dismiss_reason}` : ''}`
                                : ''}
                            </div>
                          )}
                          {ep.incident_number && (
                            <div className={styles.metaText}>
                              Ticket:{' '}
                              {ep.incident_url ? (
                                <a href={ep.incident_url} target="_blank" rel="noreferrer">
                                  {ep.incident_number} ↗
                                </a>
                              ) : (
                                ep.incident_number
                              )}{' '}
                              ({ep.snow_state_label || 'Unknown'})
                              {ep.assignment_group ? ` · Group: ${ep.assignment_group}` : ''}
                            </div>
                          )}
                        </div>
                      );
                    })}
                  </div>
                  {history?.has_more && (
                    <div className={styles.loadMoreRow}>
                      <Button
                        variant="secondary"
                        size="sm"
                        disabled={loadingMoreEpisodes}
                        data-testid="drawer-episodes-load-more"
                        onClick={() => void handleLoadMoreEpisodes()}
                      >
                        {loadingMoreEpisodes ? 'Loading...' : 'Load more'}
                      </Button>
                    </div>
                  )}
                </>
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
  metaChip: css`
    padding: 2px 8px;
    border-radius: ${theme.shape.radius.default};
    background: ${theme.colors.background.secondary};
    border: 1px solid ${theme.colors.border.weak};
    font-family: ${theme.typography.fontFamilyMonospace};
    font-size: 11px;
  `,
  metaText: css`
    font-size: ${theme.typography.bodySmall.fontSize};
    color: ${theme.colors.text.secondary};
  `,
  teamCard: css`
    padding: ${theme.spacing(1.5)};
    border: 1px solid ${theme.colors.border.medium};
    border-radius: ${theme.shape.radius.default};
    background: ${theme.colors.background.secondary};
  `,
  teamHeaderRow: css`
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: ${theme.spacing(1)};
  `,
  teamLabel: css`
    font-size: 11px;
    font-weight: ${theme.typography.fontWeightBold};
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: ${theme.colors.text.secondary};
    margin-bottom: ${theme.spacing(0.5)};
  `,
  teamValueRow: css`
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: ${theme.spacing(1)};
  `,
  teamMissingText: css`
    font-size: ${theme.typography.bodySmall.fontSize};
    color: ${theme.colors.warning.text};
  `,
  teamFormBox: css`
    margin-top: ${theme.spacing(1)};
  `,
  teamInputRow: css`
    display: flex;
    gap: ${theme.spacing(1)};
    align-items: center;
  `,
  quickTeamChips: css`
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: ${theme.spacing(0.75)};
    margin-top: ${theme.spacing(0.75)};
  `,
  quickTeamChip: css`
    padding: 2px 8px;
    border-radius: 12px;
    border: 1px solid ${theme.colors.border.medium};
    background: ${theme.colors.background.primary};
    color: ${theme.colors.text.primary};
    font-size: 11px;
    cursor: pointer;

    &:hover:not(:disabled) {
      border-color: ${theme.colors.primary.main};
    }

    &:disabled {
      opacity: 0.5;
      cursor: not-allowed;
    }
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
  prevOccurrencesCard: css`
    margin-top: ${theme.spacing(1)};
    padding: ${theme.spacing(1.25)};
    border: 1px solid ${theme.colors.border.weak};
    border-radius: ${theme.shape.radius.default};
    background: ${theme.colors.background.secondary};
  `,
  prevOccurrencesHeader: css`
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: ${theme.spacing(1)};
    margin-bottom: ${theme.spacing(1)};
  `,
  occurrenceList: css`
    display: flex;
    flex-direction: column;
    gap: ${theme.spacing(1)};
  `,
  occurrenceItem: css`
    display: flex;
    flex-direction: column;
    gap: 4px;
    padding: ${theme.spacing(1)};
    border: 1px solid ${theme.colors.border.weak};
    border-radius: ${theme.shape.radius.default};
    background: ${theme.colors.background.primary};
  `,
  occurrenceTop: css`
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: ${theme.spacing(1)};
    flex-wrap: wrap;
  `,
  occurrenceBadges: css`
    display: inline-flex;
    align-items: center;
    gap: ${theme.spacing(0.75)};
  `,
  occurrenceMeta: css`
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: ${theme.spacing(1)};
    font-size: ${theme.typography.bodySmall.fontSize};
    color: ${theme.colors.text.secondary};
  `,
  loadMoreRow: css`
    display: flex;
    justify-content: center;
    padding-top: ${theme.spacing(0.5)};
  `,
});
