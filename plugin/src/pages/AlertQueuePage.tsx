import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { css } from '@emotion/css';
import { GrafanaTheme2 } from '@grafana/data';
import { Alert as GrafanaAlert, Button, useTheme2 } from '@grafana/ui';
import { alertApi, ApiClientError } from '../api/client';
import { AlertDetailDrawer } from '../components/AlertDetailDrawer';
import { AlertTable } from '../components/AlertTable';
import { CreateIncidentModal } from '../components/CreateIncidentModal';
import { DismissAlertModal } from '../components/DismissAlertModal';
import { FallbackBannerInfo, FallbackWarningBanner } from '../components/FallbackWarningBanner';
import { FilterState, FilterToolbar, snowStatusToQueryParams } from '../components/FilterToolbar';
import { KpiFilterStrip, QuickFilterKey } from '../components/KpiFilterStrip';
import { LinkIncidentModal } from '../components/LinkIncidentModal';
import { NoteModalMode, WorkNoteModal } from '../components/WorkNoteModal';
import { useAlertQueue } from '../hooks/useAlertQueue';
import {
  Alert,
  AlertState,
  AppPluginSettings,
  CreateIncidentResponse,
  IncidentSyncResponse,
  ListAlertsQuery,
  ServiceNowMetadataResponse,
} from '../types/api';
import { clampPollIntervalSeconds } from '../utils/format';
import { canTriageAlerts, canWriteIncidents, getCurrentUser } from '../utils/permissions';

interface AlertQueuePageProps {
  pluginSettings: AppPluginSettings;
  selectedAlertId: string | null;
  onSelectAlertId: (alertId: string | null) => void;
}

const DEFAULT_FILTERS: FilterState = {
  search: '',
  severities: [],
  labels: [],
  snowStatus: 'all',
  sort: 'starts_at_desc',
  fingerprint: '',
};

export const AlertQueuePage: React.FC<AlertQueuePageProps> = ({
  pluginSettings,
  selectedAlertId,
  onSelectAlertId,
}) => {
  const theme = useTheme2();
  const styles = getStyles(theme);

  const canTriage = canTriageAlerts();
  const canWriteSnow = canWriteIncidents();
  const pollIntervalSeconds = clampPollIntervalSeconds(pluginSettings.pollIntervalSeconds);

  const [quickFilter, setQuickFilter] = useState<QuickFilterKey>('all');
  const [filters, setFilters] = useState<FilterState>(DEFAULT_FILTERS);
  const [autoRefresh, setAutoRefresh] = useState<boolean>(true);
  const [metadata, setMetadata] = useState<ServiceNowMetadataResponse | null>(null);

  const [busyAlertId, setBusyAlertId] = useState<string | null>(null);
  const [drawerRefreshTrigger, setDrawerRefreshTrigger] = useState<number>(0);

  const [toast, setToast] = useState<{
    severity: 'success' | 'warning' | 'error' | 'info';
    title: string;
    message: string;
    testId?: string;
  } | null>(null);
  const [fallbackBanner, setFallbackBanner] = useState<FallbackBannerInfo | null>(null);

  // Isolated modal state — background polling updates never clobber these.
  const [createModalAlert, setCreateModalAlert] = useState<Alert | null>(null);
  const [linkModalAlert, setLinkModalAlert] = useState<Alert | null>(null);
  const [dismissModalAlert, setDismissModalAlert] = useState<Alert | null>(null);
  const [noteModal, setNoteModal] = useState<{
    alert: Alert;
    mode: NoteModalMode;
    title: string;
    initialText?: string;
  } | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    alertApi
      .getServiceNowMetadata(controller.signal)
      .then((resp) => {
        if (!controller.signal.aborted) {
          setMetadata(resp);
        }
      })
      .catch(() => {
        // Non-blocking: metadata defaults remain usable if ServiceNow or API is temporarily down.
      });
    return () => controller.abort();
  }, []);

  const query = useMemo<ListAlertsQuery>(() => {
    let states: AlertState[] | undefined;
    if (quickFilter === 'firing') {
      states = ['firing'];
    } else if (quickFilter === 'acknowledged') {
      states = ['acknowledged'];
    } else if (quickFilter === 'dismissed') {
      states = ['dismissed'];
    } else if (quickFilter === 'resolved') {
      states = ['resolved'];
    }

    const snowParams =
      quickFilter === 'resolved_open_ticket'
        ? { incident_drift: 'resolved_open_ticket' as const }
        : snowStatusToQueryParams(filters.snowStatus);

    return {
      states,
      severities: filters.severities.length > 0 ? filters.severities : undefined,
      labels: filters.labels.length > 0 ? filters.labels : undefined,
      search: filters.search.trim() || undefined,
      fingerprint: filters.fingerprint.trim() || undefined,
      sort: filters.sort,
      limit: 50,
      ...snowParams,
    };
  }, [quickFilter, filters]);

  const {
    items,
    summary,
    nextCursor,
    loading,
    refreshing,
    lastSyncedAt,
    apiDegraded,
    errorMessage,
    refresh,
    loadMore,
    upsertAlert,
  } = useAlertQueue({
    query,
    pollIntervalSeconds,
    autoRefresh,
  });

  const handleSelectQuickFilter = useCallback((nextKey: QuickFilterKey) => {
    setQuickFilter(nextKey);
    if (nextKey === 'resolved_open_ticket') {
      setFilters((prev) => ({ ...prev, snowStatus: 'resolved_open_ticket' }));
    } else {
      setFilters((prev) =>
        prev.snowStatus === 'resolved_open_ticket' ? { ...prev, snowStatus: 'all' } : prev
      );
    }
  }, []);

  const handleChangeFilters = useCallback((next: FilterState) => {
    setFilters(next);
    if (next.snowStatus === 'resolved_open_ticket') {
      setQuickFilter('resolved_open_ticket');
    } else if (quickFilter === 'resolved_open_ticket') {
      setQuickFilter('all');
    }
  }, [quickFilter]);

  const handleResetFilters = useCallback(() => {
    setQuickFilter('all');
    setFilters(DEFAULT_FILTERS);
  }, []);

  const handleConflict = useCallback(
    (err: ApiClientError) => {
      const details = err.details ?? {};
      const currentState = typeof details.current_state === 'string' ? details.current_state : '';
      const ackedBy = typeof details.acknowledged_by === 'string' ? details.acknowledged_by : '';
      const dismissedBy = typeof details.dismissed_by === 'string' ? details.dismissed_by : '';

      let detailHint = err.message;
      if (ackedBy) {
        detailHint = `Alert was already acknowledged by ${ackedBy}. Queue refreshed.`;
      } else if (dismissedBy) {
        detailHint = `Alert was already dismissed by ${dismissedBy}. Queue refreshed.`;
      } else if (currentState === 'resolved') {
        detailHint = 'Alert resolved before your action completed. Queue refreshed.';
      } else if (currentState) {
        detailHint = `Alert state changed to ${currentState}. Queue refreshed.`;
      }

      setToast({
        severity: 'warning',
        title: '409 Conflict — Alert State Changed',
        message: detailHint,
        testId: 'conflict-toast',
      });
      setDrawerRefreshTrigger((n) => n + 1);
      void refresh();
    },
    [refresh]
  );

  const handleAcknowledge = useCallback(
    async (alert: Alert) => {
      setBusyAlertId(alert.id);
      try {
        const updated = await alertApi.acknowledgeAlert(alert.id, { version: alert.version });
        upsertAlert(updated);
        setDrawerRefreshTrigger((n) => n + 1);
        void refresh();
      } catch (err) {
        const apiErr = err as ApiClientError;
        if (apiErr?.status === 409) {
          handleConflict(apiErr);
        } else {
          setToast({
            severity: 'error',
            title: 'Acknowledge failed',
            message: apiErr?.message || 'Unable to acknowledge alert.',
          });
        }
      } finally {
        setBusyAlertId(null);
      }
    },
    [handleConflict, refresh, upsertAlert]
  );

  const handleUnacknowledge = useCallback(
    async (alert: Alert) => {
      setBusyAlertId(alert.id);
      try {
        const updated = await alertApi.unacknowledgeAlert(alert.id, { version: alert.version });
        upsertAlert(updated);
        setDrawerRefreshTrigger((n) => n + 1);
        void refresh();
      } catch (err) {
        const apiErr = err as ApiClientError;
        if (apiErr?.status === 409) {
          handleConflict(apiErr);
        } else {
          setToast({
            severity: 'error',
            title: 'Unacknowledge failed',
            message: apiErr?.message || 'Unable to unacknowledge alert.',
          });
        }
      } finally {
        setBusyAlertId(null);
      }
    },
    [handleConflict, refresh, upsertAlert]
  );

  const handleReopen = useCallback(
    async (alert: Alert) => {
      setBusyAlertId(alert.id);
      try {
        const updated = await alertApi.reopenAlert(alert.id, { version: alert.version });
        upsertAlert(updated);
        setDrawerRefreshTrigger((n) => n + 1);
        void refresh();
      } catch (err) {
        const apiErr = err as ApiClientError;
        if (apiErr?.status === 409) {
          handleConflict(apiErr);
        } else {
          setToast({
            severity: 'error',
            title: 'Reopen failed',
            message: apiErr?.message || 'Unable to reopen alert.',
          });
        }
      } finally {
        setBusyAlertId(null);
      }
    },
    [handleConflict, refresh, upsertAlert]
  );

  const handleSyncIncident = useCallback(
    async (alert: Alert) => {
      setBusyAlertId(alert.id);
      try {
        const resp = await alertApi.syncIncident(alert.id);
        upsertAlert({
          ...alert,
          servicenow_incident: resp.servicenow_incident,
          incident_drift_status: resp.incident_drift_status,
        });
        setToast({
          severity: 'success',
          title: 'ServiceNow Status Synced',
          message: `${resp.servicenow_incident.incident_number || 'Incident'} is currently ${resp.servicenow_incident.snow_state_label || 'updated'}.`,
        });
        setDrawerRefreshTrigger((n) => n + 1);
        void refresh();
      } catch (err) {
        const apiErr = err as ApiClientError;
        setToast({
          severity: 'error',
          title: 'ServiceNow sync failed',
          message: apiErr?.message || 'Unable to refresh ServiceNow incident status.',
        });
      } finally {
        setBusyAlertId(null);
      }
    },
    [refresh, upsertAlert]
  );

  const handleIncidentCreated = useCallback(
    (alert: Alert, resp: CreateIncidentResponse) => {
      setCreateModalAlert(null);
      upsertAlert({
        ...alert,
        servicenow_incident: resp.servicenow_incident,
      });
      setDrawerRefreshTrigger((n) => n + 1);
      void refresh();

      const incNum = resp.servicenow_incident.incident_number || 'Incident';
      if (
        resp.servicenow_incident.caller_resolution_status === 'fallback_noc' ||
        (resp.warnings && resp.warnings.length > 0)
      ) {
        const user = getCurrentUser();
        setFallbackBanner({
          incidentNumber: incNum,
          userEmail: user.email || user.login,
          reason: resp.servicenow_incident.caller_fallback_reason,
          warnings: resp.warnings,
        });
      } else {
        setToast({
          severity: 'success',
          title: `Created ${incNum}`,
          message: `ServiceNow incident ${incNum} linked to ${alert.alert_name}.`,
        });
      }
    },
    [refresh, upsertAlert]
  );

  const handleIncidentLinked = useCallback(
    (resp: IncidentSyncResponse) => {
      const target = linkModalAlert;
      setLinkModalAlert(null);
      if (target) {
        upsertAlert({
          ...target,
          servicenow_incident: resp.servicenow_incident,
          incident_drift_status: resp.incident_drift_status,
        });
      }
      setToast({
        severity: 'success',
        title: `Linked ${resp.servicenow_incident.incident_number || 'Incident'}`,
        message: 'Existing ServiceNow incident linked to alert episode.',
      });
      setDrawerRefreshTrigger((n) => n + 1);
      void refresh();
    },
    [linkModalAlert, refresh, upsertAlert]
  );

  return (
    <div className={styles.page}>
      <div className={styles.header}>
        <div>
          <h1 className={styles.title}>Alert Queue</h1>
          <p className={styles.subtitle}>
            Triage Prometheus Alertmanager episodes and track linked ServiceNow incidents.
          </p>
        </div>
      </div>

      {apiDegraded && (
        <div className={styles.bannerSpacing} data-testid="api-degraded-banner">
          <GrafanaAlert severity="warning" title="Live updates paused — Alert Manager API unreachable">
            {errorMessage || 'Retaining the last known alert queue and retrying automatically.'}
          </GrafanaAlert>
        </div>
      )}

      {!apiDegraded && errorMessage && (
        <div className={styles.bannerSpacing}>
          <GrafanaAlert severity="error" title="Error loading alerts">
            {errorMessage}
          </GrafanaAlert>
        </div>
      )}

      {toast && (
        <div className={styles.bannerSpacing} role="alert" data-testid={toast.testId || 'status-toast'}>
          <GrafanaAlert
            severity={toast.severity}
            title={toast.title}
            onRemove={() => setToast(null)}
          >
            {toast.message}
          </GrafanaAlert>
        </div>
      )}

      <FallbackWarningBanner info={fallbackBanner} onDismiss={() => setFallbackBanner(null)} />

      <KpiFilterStrip
        summary={summary}
        activeFilter={quickFilter}
        onSelectFilter={handleSelectQuickFilter}
      />

      <FilterToolbar
        filters={filters}
        autoRefresh={autoRefresh}
        pollIntervalSeconds={pollIntervalSeconds}
        refreshing={refreshing}
        lastSyncedAt={lastSyncedAt}
        onChangeFilters={handleChangeFilters}
        onToggleAutoRefresh={setAutoRefresh}
        onManualRefresh={() => void refresh()}
        onResetAll={handleResetFilters}
      />

      <AlertTable
        alerts={items}
        loading={loading}
        canTriage={canTriage}
        canWriteIncidents={canWriteSnow}
        busyAlertId={busyAlertId}
        onSelectAlert={(alert) => onSelectAlertId(alert.id)}
        onAcknowledge={(alert) => void handleAcknowledge(alert)}
        onUnacknowledge={(alert) => void handleUnacknowledge(alert)}
        onReopen={(alert) => void handleReopen(alert)}
        onOpenDismiss={(alert) => setDismissModalAlert(alert)}
        onOpenCreateIncident={(alert) => setCreateModalAlert(alert)}
        onOpenLinkIncident={(alert) => setLinkModalAlert(alert)}
        onOpenAddNote={(alert) =>
          setNoteModal({
            alert,
            mode: 'triage_note',
            title: `Add Triage Note — ${alert.alert_name}`,
          })
        }
        onOpenWorkNote={(alert) =>
          setNoteModal({
            alert,
            mode: 'snow_work_note',
            title: `Post ServiceNow Work Note — ${alert.servicenow_incident?.incident_number || alert.alert_name}`,
            initialText:
              alert.incident_drift_status === 'resolved_open_ticket'
                ? `Alert resolved in Prometheus. Please verify recovery and resolve/close this incident.`
                : '',
          })
        }
        onSyncIncident={(alert) => void handleSyncIncident(alert)}
        onFilterByFingerprint={(fp) =>
          setFilters((prev) => ({
            ...prev,
            fingerprint: fp,
          }))
        }
      />

      {nextCursor && (
        <div className={styles.loadMoreRow}>
          <Button variant="secondary" onClick={() => void loadMore()} disabled={refreshing}>
            {refreshing ? 'Loading...' : 'Load More Alerts'}
          </Button>
        </div>
      )}

      <AlertDetailDrawer
        alertId={selectedAlertId}
        canTriage={canTriage}
        canWriteIncidents={canWriteSnow}
        onClose={() => onSelectAlertId(null)}
        onAcknowledge={(alert) => void handleAcknowledge(alert)}
        onUnacknowledge={(alert) => void handleUnacknowledge(alert)}
        onReopen={(alert) => void handleReopen(alert)}
        onOpenDismiss={(alert) => setDismissModalAlert(alert)}
        onOpenCreateIncident={(alert) => setCreateModalAlert(alert)}
        onOpenLinkIncident={(alert) => setLinkModalAlert(alert)}
        onOpenAddNote={(alert) =>
          setNoteModal({
            alert,
            mode: 'triage_note',
            title: `Add Triage Note — ${alert.alert_name}`,
          })
        }
        onOpenWorkNote={(alert, initialText) =>
          setNoteModal({
            alert,
            mode: 'snow_work_note',
            title: `Post ServiceNow Work Note — ${alert.servicenow_incident?.incident_number || alert.alert_name}`,
            initialText,
          })
        }
        onSyncIncident={handleSyncIncident}
        onSelectEpisodeId={(id) => onSelectAlertId(id)}
        refreshTrigger={drawerRefreshTrigger}
      />

      <CreateIncidentModal
        isOpen={Boolean(createModalAlert)}
        alert={createModalAlert}
        metadata={metadata}
        pluginSettings={pluginSettings}
        onClose={() => setCreateModalAlert(null)}
        onCreated={handleIncidentCreated}
      />

      <LinkIncidentModal
        isOpen={Boolean(linkModalAlert)}
        alert={linkModalAlert}
        onClose={() => setLinkModalAlert(null)}
        onLinked={handleIncidentLinked}
      />

      <DismissAlertModal
        isOpen={Boolean(dismissModalAlert)}
        alert={dismissModalAlert}
        onClose={() => setDismissModalAlert(null)}
        onDismissed={(updated) => {
          setDismissModalAlert(null);
          upsertAlert(updated);
          setDrawerRefreshTrigger((n) => n + 1);
          void refresh();
        }}
        onConflict={(err) => handleConflict(err)}
      />

      <WorkNoteModal
        isOpen={Boolean(noteModal)}
        mode={noteModal?.mode ?? 'triage_note'}
        alert={noteModal?.alert ?? null}
        title={noteModal?.title ?? 'Add Note'}
        initialText={noteModal?.initialText}
        onClose={() => setNoteModal(null)}
        onSubmitted={(_, mode) => {
          setNoteModal(null);
          setToast({
            severity: 'success',
            title: mode === 'snow_work_note' ? 'Work note posted to ServiceNow' : 'Triage note saved',
            message:
              mode === 'snow_work_note'
                ? 'The work note was appended to the linked ServiceNow incident and audit timeline.'
                : 'The triage note was recorded in the alert audit timeline.',
          });
          setDrawerRefreshTrigger((n) => n + 1);
        }}
      />
    </div>
  );
};

const getStyles = (theme: GrafanaTheme2) => ({
  page: css`
    padding: ${theme.spacing(3)};
    max-width: 1600px;
    margin: 0 auto;
  `,
  header: css`
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    margin-bottom: ${theme.spacing(2)};
  `,
  title: css`
    margin: 0 0 ${theme.spacing(0.5)} 0;
    font-size: ${theme.typography.h2.fontSize};
  `,
  subtitle: css`
    margin: 0;
    color: ${theme.colors.text.secondary};
  `,
  bannerSpacing: css`
    margin-bottom: ${theme.spacing(1.5)};
  `,
  loadMoreRow: css`
    display: flex;
    justify-content: center;
    margin-top: ${theme.spacing(2)};
  `,
});
