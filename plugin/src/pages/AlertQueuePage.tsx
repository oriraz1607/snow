import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { css } from '@emotion/css';
import { GrafanaTheme2 } from '@grafana/data';
import { Alert as GrafanaAlert, Button, useTheme2 } from '@grafana/ui';
import { alertApi, ApiClientError } from '../api/client';
import { AlertDetailDrawer } from '../components/AlertDetailDrawer';
import { AlertTable, GatedButton } from '../components/AlertTable';
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
  AlertStateFilter,
  AppPluginSettings,
  IncidentSyncResponse,
  ListAlertsQuery,
  ServiceNowIncident,
  ServiceNowMetadataResponse,
} from '../types/api';
import { clampPollIntervalSeconds, isAlertSelectableForIncident } from '../utils/format';
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

  const [selectedAlertIds, setSelectedAlertIds] = useState<Set<string>>(new Set());
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
  const [createModalAlerts, setCreateModalAlerts] = useState<Alert[]>([]);
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
    let states: AlertStateFilter[] | undefined;
    if (quickFilter === 'firing') {
      states = ['firing'];
    } else if (quickFilter === 'acknowledged') {
      states = ['acknowledged'];
    } else if (quickFilter === 'non_active_acknowledged') {
      states = ['non_active_acknowledged'];
    } else if (quickFilter === 'dismissed') {
      states = ['dismissed'];
    }

    const snowParams = snowStatusToQueryParams(filters.snowStatus);

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

  // Auto-prune selections that disappeared or gained a linked incident on background refresh.
  useEffect(() => {
    setSelectedAlertIds((prev) => {
      if (prev.size === 0) {
        return prev;
      }
      const eligible = new Set(items.filter(isAlertSelectableForIncident).map((a) => a.id));
      let changed = false;
      const next = new Set<string>();
      for (const id of prev) {
        if (eligible.has(id)) {
          next.add(id);
        } else {
          changed = true;
        }
      }
      return changed ? next : prev;
    });
  }, [items]);

  const selectedAlerts = useMemo(
    () => items.filter((a) => selectedAlertIds.has(a.id)),
    [items, selectedAlertIds]
  );

  const handleToggleSelectAlert = useCallback((alertId: string) => {
    setSelectedAlertIds((prev) => {
      const next = new Set(prev);
      if (next.has(alertId)) {
        next.delete(alertId);
      } else {
        next.add(alertId);
      }
      return next;
    });
  }, []);

  const handleToggleSelectAll = useCallback((selectableIds: string[], selectAll: boolean) => {
    setSelectedAlertIds((prev) => {
      const next = new Set(prev);
      if (selectAll) {
        for (const id of selectableIds) {
          next.add(id);
        }
      } else {
        for (const id of selectableIds) {
          next.delete(id);
        }
      }
      return next;
    });
  }, []);

  const handleSelectQuickFilter = useCallback((nextKey: QuickFilterKey) => {
    setQuickFilter(nextKey);
    if (nextKey !== 'non_active_acknowledged') {
      setFilters((prev) =>
        prev.snowStatus === 'resolved_open_ticket' ? { ...prev, snowStatus: 'all' } : prev
      );
    }
  }, []);

  const handleChangeFilters = useCallback(
    (next: FilterState) => {
      setFilters(next);
      if (next.snowStatus === 'resolved_open_ticket') {
        setQuickFilter('non_active_acknowledged');
      }
    },
    []
  );

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
    (
      alertIds: string[],
      result: { servicenow_incident: ServiceNowIncident; warnings?: string[] }
    ) => {
      setCreateModalAlerts([]);
      setSelectedAlertIds(new Set());
      const idSet = new Set(alertIds);
      for (const item of items) {
        if (idSet.has(item.id)) {
          upsertAlert({
            ...item,
            state: 'acknowledged',
            servicenow_incident: result.servicenow_incident,
          });
        }
      }
      setDrawerRefreshTrigger((n) => n + 1);
      void refresh();

      const incNum = result.servicenow_incident.incident_number || 'Incident';
      if (
        result.servicenow_incident.caller_resolution_status === 'fallback_noc' ||
        (result.warnings && result.warnings.length > 0)
      ) {
        const user = getCurrentUser();
        setFallbackBanner({
          incidentNumber: incNum,
          userEmail: user.email || user.login,
          reason: result.servicenow_incident.caller_fallback_reason,
          warnings: result.warnings,
        });
      } else {
        setToast({
          severity: 'success',
          title: `Created ${incNum}`,
          message:
            alertIds.length > 1
              ? `ServiceNow incident ${incNum} created and linked to ${alertIds.length} alerts.`
              : `ServiceNow incident ${incNum} linked to alert.`,
        });
      }
    },
    [items, refresh, upsertAlert]
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

  const selectedFiringCount = selectedAlerts.filter((a) => a.state === 'firing').length;
  const canCreateBulk = canWriteSnow && (selectedFiringCount === 0 || canTriage);
  const bulkDisabledReason = !canWriteSnow
    ? 'Requires Editor or Admin role (incidents:write permission)'
    : 'Requires triage permission to auto-acknowledge firing alerts';

  return (
    <div className={styles.page}>
      <div className={styles.header}>
        <div>
          <h1 className={styles.title}>Alert Queue</h1>
          <p className={styles.subtitle}>
            Triage Prometheus Alertmanager episodes, assign owning teams, and manage ServiceNow incidents.
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

      {selectedAlerts.length > 0 && (
        <div className={styles.bulkBar} data-testid="bulk-selection-bar">
          <div className={styles.bulkSummary}>
            <strong>
              {selectedAlerts.length} alert{selectedAlerts.length === 1 ? '' : 's'} selected
            </strong>
            {selectedFiringCount > 0 && (
              <span className={styles.bulkNote}>
                ({selectedFiringCount} firing alert{selectedFiringCount === 1 ? '' : 's'} will be auto-acknowledged)
              </span>
            )}
          </div>
          <div className={styles.bulkActions}>
            <GatedButton
              allowed={canCreateBulk}
              disabledReason={bulkDisabledReason}
              variant="primary"
              size="sm"
              onClick={() => setCreateModalAlerts(selectedAlerts)}
            >
              {selectedAlerts.length > 1
                ? `Create Single ServiceNow Incident (${selectedAlerts.length})`
                : 'Create ServiceNow Incident'}
            </GatedButton>
            <Button variant="secondary" size="sm" onClick={() => setSelectedAlertIds(new Set())}>
              Clear Selection
            </Button>
          </div>
        </div>
      )}

      <AlertTable
        alerts={items}
        loading={loading}
        canTriage={canTriage}
        canWriteIncidents={canWriteSnow}
        busyAlertId={busyAlertId}
        selectedAlertIds={selectedAlertIds}
        onToggleSelectAlert={handleToggleSelectAlert}
        onToggleSelectAll={handleToggleSelectAll}
        onSelectAlert={(alert) => onSelectAlertId(alert.id)}
        onAcknowledge={(alert) => void handleAcknowledge(alert)}
        onUnacknowledge={(alert) => void handleUnacknowledge(alert)}
        onReopen={(alert) => void handleReopen(alert)}
        onOpenDismiss={(alert) => setDismissModalAlert(alert)}
        onOpenCreateIncident={(alert) => setCreateModalAlerts([alert])}
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
                ? 'Alert resolved in Prometheus. Please verify recovery and resolve/close this incident.'
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
        metadata={metadata}
        canTriage={canTriage}
        canWriteIncidents={canWriteSnow}
        onClose={() => onSelectAlertId(null)}
        onAlertUpdated={(updated) => {
          upsertAlert(updated);
          void refresh();
        }}
        onAcknowledge={(alert) => void handleAcknowledge(alert)}
        onUnacknowledge={(alert) => void handleUnacknowledge(alert)}
        onReopen={(alert) => void handleReopen(alert)}
        onOpenDismiss={(alert) => setDismissModalAlert(alert)}
        onOpenCreateIncident={(alert) => setCreateModalAlerts([alert])}
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
        isOpen={createModalAlerts.length > 0}
        alerts={createModalAlerts}
        metadata={metadata}
        pluginSettings={pluginSettings}
        onClose={() => setCreateModalAlerts([])}
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
    max-width: 1720px;
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
  bulkBar: css`
    display: flex;
    flex-wrap: wrap;
    justify-content: space-between;
    align-items: center;
    gap: ${theme.spacing(1.5)};
    padding: ${theme.spacing(1.25)} ${theme.spacing(1.5)};
    margin-bottom: ${theme.spacing(1.5)};
    border: 1px solid ${theme.colors.primary.border};
    border-left: 4px solid ${theme.colors.primary.main};
    border-radius: ${theme.shape.radius.default};
    background: ${theme.colors.background.secondary};
  `,
  bulkSummary: css`
    display: flex;
    align-items: center;
    gap: ${theme.spacing(1)};
  `,
  bulkNote: css`
    font-size: ${theme.typography.bodySmall.fontSize};
    color: ${theme.colors.warning.text};
  `,
  bulkActions: css`
    display: flex;
    gap: ${theme.spacing(1)};
  `,
  loadMoreRow: css`
    display: flex;
    justify-content: center;
    margin-top: ${theme.spacing(2)};
  `,
});
