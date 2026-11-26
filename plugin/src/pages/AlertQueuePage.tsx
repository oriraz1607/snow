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
import { FilterState, FilterToolbar, HistoryTimeRange } from '../components/FilterToolbar';
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
import {
  clampPollIntervalSeconds,
  getOpenIncidentDismissBlockReason,
  hasOpenServiceNowIncident,
  isAlertSelectableForBulk,
  isAlertSelectableForIncident,
} from '../utils/format';
import { canTriageAlerts, canWriteIncidents, getCurrentUser } from '../utils/permissions';

interface AlertQueuePageProps {
  pluginSettings: AppPluginSettings;
  selectedAlertId: string | null;
  onSelectAlertId: (alertId: string | null) => void;
}

export const MAX_BULK_SELECTION = 50;

const DEFAULT_FILTERS: FilterState = {
  search: '',
  severities: [],
  environments: [],
  sites: [],
  team: '',
  instance: '',
  labels: [],
  sort: 'severity_desc',
  fingerprint: '',
  timeRange: '',
};

const TIME_RANGE_OFFSETS_MS: Record<Exclude<HistoryTimeRange, ''>, number> = {
  '24h': 24 * 60 * 60 * 1000,
  '7d': 7 * 24 * 60 * 60 * 1000,
  '30d': 30 * 24 * 60 * 60 * 1000,
  '90d': 90 * 24 * 60 * 60 * 1000,
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

  const [viewMode, setViewMode] = useState<'active' | 'history'>('active');
  const [quickFilter, setQuickFilter] = useState<QuickFilterKey>('firing');
  const [filters, setFilters] = useState<FilterState>(DEFAULT_FILTERS);
  const [pageSize, setPageSize] = useState<number>(50);
  const [autoRefresh, setAutoRefresh] = useState<boolean>(true);
  const [metadata, setMetadata] = useState<ServiceNowMetadataResponse | null>(null);

  const [selectedAlertIds, setSelectedAlertIds] = useState<Set<string>>(new Set());
  const [selectionCapHit, setSelectionCapHit] = useState<boolean>(false);
  const [busyAlertId, setBusyAlertId] = useState<string | null>(null);
  const [drawerRefreshTrigger, setDrawerRefreshTrigger] = useState<number>(0);
  const [focusConfluenceOnOpen, setFocusConfluenceOnOpen] = useState<boolean>(false);

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
  const [dismissModalAlerts, setDismissModalAlerts] = useState<Alert[]>([]);
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
    const states: AlertStateFilter[] = viewMode === 'history' ? ['resolved'] : [quickFilter];
    let from: string | undefined;
    if (viewMode === 'history' && filters.timeRange) {
      const offsetMs = TIME_RANGE_OFFSETS_MS[filters.timeRange];
      if (offsetMs) {
        const roundedNow = Math.floor(Date.now() / 1000) * 1000;
        from = new Date(roundedNow - offsetMs).toISOString();
      }
    }

    return {
      states,
      severities: filters.severities.length > 0 ? filters.severities : undefined,
      environments: filters.environments.length > 0 ? filters.environments : undefined,
      sites: filters.sites.length > 0 ? filters.sites : undefined,
      teams: filters.team ? [filters.team] : undefined,
      instances: filters.instance ? [filters.instance] : undefined,
      labels: filters.labels.length > 0 ? filters.labels : undefined,
      search: filters.search.trim() || undefined,
      from,
      fingerprint: filters.fingerprint.trim() || undefined,
      sort: filters.sort,
      limit: pageSize,
    };
  }, [viewMode, quickFilter, filters, pageSize]);

  const {
    items,
    summary,
    filterOptions,
    filteredTotal,
    pageIndex,
    hasNextPage,
    hasPrevPage,
    staleOnLaterPage,
    loading,
    refreshing,
    lastSyncedAt,
    apiDegraded,
    errorMessage,
    refresh,
    goNextPage,
    goPrevPage,
    goFirstPage,
    upsertAlert,
  } = useAlertQueue({
    query,
    pollIntervalSeconds,
    autoRefresh,
  });

  // Auto-prune selections that disappeared or became non-triageable on background refresh.
  useEffect(() => {
    setSelectedAlertIds((prev) => {
      if (prev.size === 0) {
        return prev;
      }
      const eligible = new Set(items.filter(isAlertSelectableForBulk).map((a) => a.id));
      let changed = false;
      const next = new Set<string>();
      for (const id of prev) {
        if (eligible.has(id)) {
          next.add(id);
        } else {
          changed = true;
        }
      }
      if (next.size < MAX_BULK_SELECTION) {
        setSelectionCapHit(false);
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
        if (next.size < MAX_BULK_SELECTION) {
          setSelectionCapHit(false);
        }
        return next;
      }
      if (next.size >= MAX_BULK_SELECTION) {
        setSelectionCapHit(true);
        return prev;
      }
      next.add(alertId);
      if (next.size >= MAX_BULK_SELECTION) {
        setSelectionCapHit(true);
      }
      return next;
    });
  }, []);

  const handleToggleSelectAll = useCallback((selectableIds: string[], selectAll: boolean) => {
    setSelectedAlertIds((prev) => {
      const next = new Set(prev);
      if (selectAll) {
        let truncated = false;
        for (const id of selectableIds) {
          if (next.has(id)) {
            continue;
          }
          if (next.size >= MAX_BULK_SELECTION) {
            truncated = true;
            break;
          }
          next.add(id);
        }
        setSelectionCapHit(truncated || next.size >= MAX_BULK_SELECTION);
      } else {
        for (const id of selectableIds) {
          next.delete(id);
        }
        if (next.size < MAX_BULK_SELECTION) {
          setSelectionCapHit(false);
        }
      }
      return next;
    });
  }, []);

  const handleSelectQuickFilter = useCallback((nextKey: QuickFilterKey) => {
    setViewMode('active');
    setQuickFilter(nextKey);
    setFilters((prev) =>
      prev.sort === 'resolved_at_desc' || prev.timeRange
        ? {
            ...prev,
            sort: prev.sort === 'resolved_at_desc' ? 'severity_desc' : prev.sort,
            timeRange: '',
          }
        : prev
    );
  }, []);

  const handleSelectViewMode = useCallback((nextMode: 'active' | 'history') => {
    setViewMode(nextMode);
    setSelectedAlertIds(new Set());
    setSelectionCapHit(false);
    setFilters((prev) => ({
      ...prev,
      sort: nextMode === 'history' ? 'resolved_at_desc' : prev.sort === 'resolved_at_desc' ? 'severity_desc' : prev.sort,
      timeRange: nextMode === 'history' ? prev.timeRange ?? '' : '',
    }));
  }, []);

  const handleChangeFilters = useCallback((next: FilterState) => {
    setFilters(next);
  }, []);

  const handleResetFilters = useCallback(() => {
    setViewMode('active');
    setQuickFilter('firing');
    setFilters(DEFAULT_FILTERS);
  }, []);

  const handleConflict = useCallback(
    (err: ApiClientError) => {
      if (err.code === 'ALERT_HAS_OPEN_INCIDENT') {
        setToast({
          severity: 'warning',
          title: 'Cannot Dismiss — Open ServiceNow Incident',
          message: err.message,
          testId: 'conflict-toast',
        });
        setDrawerRefreshTrigger((n) => n + 1);
        void refresh();
        return;
      }

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
      void alertApi
        .getServiceNowMetadata()
        .then(setMetadata)
        .catch(() => {});
    },
    [refresh]
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
      setSelectionCapHit(false);
      const idSet = new Set(alertIds);
      for (const item of items) {
        // Only optimistically update alerts that already carry authoritative acknowledged metadata;
        // firing alerts auto-acknowledged server-side will be populated by refresh().
        if (idSet.has(item.id) && item.state === 'acknowledged') {
          upsertAlert({
            ...item,
            servicenow_incident: result.servicenow_incident,
          });
        }
      }
      setDrawerRefreshTrigger((n) => n + 1);
      void refresh();
      void alertApi
        .getServiceNowMetadata()
        .then(setMetadata)
        .catch(() => {});

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
              ? `ServiceNow incident ${incNum} created and ${alertIds.length} alerts acknowledged.`
              : `ServiceNow incident ${incNum} created and alert acknowledged.`,
        });
      }
    },
    [items, refresh, upsertAlert]
  );

  const handleIncidentLinked = useCallback(
    (resp: IncidentSyncResponse) => {
      const target = linkModalAlert;
      setLinkModalAlert(null);
      if (target && target.state === 'acknowledged') {
        upsertAlert({
          ...target,
          servicenow_incident: resp.servicenow_incident,
          incident_drift_status: resp.incident_drift_status,
        });
      }
      const wasFiring = target?.state === 'firing';
      setToast({
        severity: 'success',
        title: `Linked ${resp.servicenow_incident.incident_number || 'Incident'}`,
        message: wasFiring
          ? 'Existing ServiceNow incident linked and alert acknowledged.'
          : 'Existing ServiceNow incident linked to alert episode.',
      });
      setDrawerRefreshTrigger((n) => n + 1);
      void refresh();
      void alertApi
        .getServiceNowMetadata()
        .then(setMetadata)
        .catch(() => {});
    },
    [linkModalAlert, refresh, upsertAlert]
  );

  const incidentEligibleAlerts = selectedAlerts.filter(isAlertSelectableForIncident);
  const selectedFiringForSnowCount = incidentEligibleAlerts.filter((a) => a.state === 'firing').length;
  const dismissEligibleAlerts = selectedAlerts.filter((a) => !hasOpenServiceNowIncident(a));
  const blockedDismissAlerts = selectedAlerts.filter(hasOpenServiceNowIncident);
  const canBulkDismiss = canTriage && dismissEligibleAlerts.length > 0;
  const bulkDismissDisabledReason = !canTriage
    ? 'Requires Editor or Admin role (alerts:triage permission)'
    : (blockedDismissAlerts[0] ? getOpenIncidentDismissBlockReason(blockedDismissAlerts[0]) : undefined) ||
      'Cannot dismiss while linked ServiceNow incident is open';
  const canCreateBulk =
    canWriteSnow &&
    incidentEligibleAlerts.length > 0 &&
    (selectedFiringForSnowCount === 0 || canTriage);
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
        <div className={styles.viewSwitch} role="group" aria-label="Queue view mode" data-testid="view-mode-switch">
          <button
            type="button"
            className={`${styles.viewBtn} ${viewMode === 'active' ? styles.viewBtnActive : ''}`}
            aria-pressed={viewMode === 'active'}
            data-testid="view-tab-active"
            onClick={() => handleSelectViewMode('active')}
          >
            Active Queue
          </button>
          <button
            type="button"
            className={`${styles.viewBtn} ${viewMode === 'history' ? styles.viewBtnActive : ''}`}
            aria-pressed={viewMode === 'history'}
            data-testid="view-tab-history"
            onClick={() => handleSelectViewMode('history')}
          >
            History
          </button>
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
        activeFilter={viewMode === 'active' ? quickFilter : null}
        onSelectFilter={handleSelectQuickFilter}
      />

      <FilterToolbar
        filters={filters}
        filterOptions={filterOptions}
        autoRefresh={autoRefresh}
        pollIntervalSeconds={pollIntervalSeconds}
        refreshing={refreshing}
        lastSyncedAt={lastSyncedAt}
        showTimeRange={viewMode === 'history'}
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
        selectedAlertIds={selectedAlertIds}
        maxBulkSelection={MAX_BULK_SELECTION}
        showServiceNowColumn={
          viewMode === 'history' ||
          quickFilter === 'acknowledged' ||
          quickFilter === 'non_active_acknowledged'
        }
        sort={filters.sort}
        onChangeSort={(nextSort) =>
          setFilters((prev) => ({
            ...prev,
            sort: nextSort,
          }))
        }
        onToggleSelectAlert={handleToggleSelectAlert}
        onToggleSelectAll={handleToggleSelectAll}
        onSelectAlert={(alert) => {
          setFocusConfluenceOnOpen(false);
          onSelectAlertId(alert.id);
        }}
        onOpenConfluenceEdit={(alert) => {
          setFocusConfluenceOnOpen(true);
          onSelectAlertId(alert.id);
        }}
        onUnacknowledge={(alert) => void handleUnacknowledge(alert)}
        onReopen={(alert) => void handleReopen(alert)}
        onOpenDismiss={(alert) => setDismissModalAlerts([alert])}
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

      {selectedAlerts.length > 0 && (
        <div className={styles.bulkBar} data-testid="bulk-selection-bar">
          <div className={styles.bulkSummary}>
            <strong>
              {selectedAlerts.length} alert{selectedAlerts.length === 1 ? '' : 's'} selected
            </strong>
            {selectedFiringForSnowCount > 0 && (
              <span className={styles.bulkNote}>
                ({selectedFiringForSnowCount} firing alert{selectedFiringForSnowCount === 1 ? '' : 's'} acknowledged on ticket creation)
              </span>
            )}
            {blockedDismissAlerts.length > 0 && (
              <span className={styles.bulkNote} data-testid="bulk-dismiss-blocked-notice">
                {blockedDismissAlerts.length} alert{blockedDismissAlerts.length === 1 ? '' : 's'} with open SNOW ticket
                {dismissEligibleAlerts.length > 0 ? ' (excluded from dismiss)' : ' cannot be dismissed'}
              </span>
            )}
            {(selectionCapHit || selectedAlerts.length >= MAX_BULK_SELECTION) && (
              <span className={styles.bulkNote} data-testid="bulk-selection-cap-notice">
                Maximum {MAX_BULK_SELECTION} alerts per batch reached.
              </span>
            )}
          </div>
          <div className={styles.bulkActions}>
            <GatedButton
              allowed={canBulkDismiss}
              disabledReason={bulkDismissDisabledReason}
              variant="destructive"
              size="sm"
              onClick={() => setDismissModalAlerts(dismissEligibleAlerts)}
            >
              {blockedDismissAlerts.length > 0 && dismissEligibleAlerts.length > 0
                ? `Dismiss Eligible (${dismissEligibleAlerts.length})`
                : selectedAlerts.length > 1
                  ? `Dismiss Selected (${selectedAlerts.length})`
                  : 'Dismiss Selected'}
            </GatedButton>
            {incidentEligibleAlerts.length > 0 && (
              <GatedButton
                allowed={canCreateBulk}
                disabledReason={bulkDisabledReason}
                variant="primary"
                size="sm"
                onClick={() => setCreateModalAlerts(incidentEligibleAlerts)}
              >
                {incidentEligibleAlerts.length > 1
                  ? selectedFiringForSnowCount > 0
                    ? `Acknowledge & Create Single Incident (${incidentEligibleAlerts.length})`
                    : `Create Single ServiceNow Incident (${incidentEligibleAlerts.length})`
                  : selectedFiringForSnowCount > 0
                    ? 'Acknowledge & Create Incident'
                    : 'Create ServiceNow Incident'}
              </GatedButton>
            )}
            <Button
              variant="secondary"
              size="sm"
              onClick={() => {
                setSelectedAlertIds(new Set());
                setSelectionCapHit(false);
              }}
            >
              Clear Selection
            </Button>
          </div>
        </div>
      )}

      {(items.length > 0 || hasPrevPage || hasNextPage) && (
        <div className={styles.paginationBar} data-testid="pagination-bar">
          <div className={styles.paginationInfo}>
            <span>
              {(() => {
                const startRow = items.length === 0 ? 0 : pageIndex * pageSize + 1;
                const endRow = pageIndex * pageSize + items.length;
                const effectiveTotal = Math.max(filteredTotal, endRow);
                const totalPages = Math.max(1, Math.ceil(effectiveTotal / pageSize));
                return `Showing ${startRow.toLocaleString()}–${endRow.toLocaleString()} of ${effectiveTotal.toLocaleString()} · Page ${(pageIndex + 1).toLocaleString()} of ${totalPages.toLocaleString()}`;
              })()}
            </span>
            {staleOnLaterPage && pageIndex > 0 && (
              <button
                type="button"
                className={styles.stalePillBtn}
                data-testid="stale-page-jump"
                onClick={() => void goFirstPage()}
              >
                Queue updated — Jump to Page 1
              </button>
            )}
          </div>
          <div className={styles.paginationControls}>
            <label className={styles.pageSizeLabel}>
              <span>Rows:</span>
              <select
                aria-label="Rows per page"
                className={styles.pageSizeSelect}
                value={pageSize}
                onChange={(e) => setPageSize(Number(e.currentTarget.value))}
              >
                <option value={25}>25</option>
                <option value={50}>50</option>
                <option value={100}>100</option>
              </select>
            </label>
            <Button
              variant="secondary"
              size="sm"
              disabled={!hasPrevPage || refreshing}
              onClick={() => void goFirstPage()}
            >
              « First
            </Button>
            <Button
              variant="secondary"
              size="sm"
              disabled={!hasPrevPage || refreshing}
              onClick={() => void goPrevPage()}
            >
              ‹ Prev
            </Button>
            <Button
              variant="secondary"
              size="sm"
              disabled={!hasNextPage || refreshing}
              onClick={() => void goNextPage()}
            >
              Next ›
            </Button>
          </div>
        </div>
      )}

      <AlertDetailDrawer
        alertId={selectedAlertId}
        canTriage={canTriage}
        canWriteIncidents={canWriteSnow}
        onClose={() => {
          setFocusConfluenceOnOpen(false);
          onSelectAlertId(null);
        }}
        onAlertUpdated={(updated) => {
          setFocusConfluenceOnOpen(false);
          upsertAlert(updated);
          void refresh();
        }}
        focusConfluence={focusConfluenceOnOpen}
        onUnacknowledge={(alert) => void handleUnacknowledge(alert)}
        onReopen={(alert) => void handleReopen(alert)}
        onOpenDismiss={(alert) => setDismissModalAlerts([alert])}
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
        onConflict={() => {
          setDrawerRefreshTrigger((n) => n + 1);
          void refresh();
        }}
        onCreated={handleIncidentCreated}
      />

      <LinkIncidentModal
        isOpen={Boolean(linkModalAlert)}
        alert={linkModalAlert}
        onClose={() => setLinkModalAlert(null)}
        onLinked={handleIncidentLinked}
      />

      <DismissAlertModal
        isOpen={dismissModalAlerts.length > 0}
        alerts={dismissModalAlerts}
        onClose={() => setDismissModalAlerts([])}
        onDismissed={(dismissedIds) => {
          setDismissModalAlerts([]);
          const dismissedSet = new Set(dismissedIds);
          setSelectedAlertIds((prev) => {
            const next = new Set<string>();
            for (const id of prev) {
              if (!dismissedSet.has(id)) {
                next.add(id);
              }
            }
            if (next.size < MAX_BULK_SELECTION) {
              setSelectionCapHit(false);
            }
            return next;
          });
          setToast({
            severity: 'success',
            title: `Dismissed ${dismissedIds.length} alert${dismissedIds.length === 1 ? '' : 's'}`,
            message: 'Moved to the Dismissed tab.',
          });
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
    width: 100%;
    max-width: 100%;
    box-sizing: border-box;
    overflow-x: clip;
    padding: ${theme.spacing(2.5)};
    margin: 0 auto;
  `,
  header: css`
    display: flex;
    flex-wrap: wrap;
    justify-content: space-between;
    align-items: flex-start;
    gap: ${theme.spacing(1.5)};
    margin-bottom: ${theme.spacing(2)};
  `,
  viewSwitch: css`
    display: inline-flex;
    align-items: center;
    padding: 3px;
    border: 1px solid ${theme.colors.border.medium};
    border-radius: ${theme.shape.radius.default};
    background: ${theme.colors.background.secondary};
    gap: 2px;
  `,
  viewBtn: css`
    padding: 5px 12px;
    border: none;
    border-radius: ${theme.shape.radius.default};
    background: transparent;
    color: ${theme.colors.text.secondary};
    font-size: ${theme.typography.bodySmall.fontSize};
    font-weight: ${theme.typography.fontWeightMedium};
    cursor: pointer;

    &:hover {
      color: ${theme.colors.text.primary};
    }
  `,
  viewBtnActive: css`
    background: ${theme.colors.primary.main};
    color: ${theme.colors.primary.contrastText};
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
    position: sticky;
    bottom: ${theme.spacing(1)};
    z-index: 15;
    display: flex;
    flex-wrap: wrap;
    justify-content: space-between;
    align-items: center;
    gap: ${theme.spacing(1.5)};
    padding: ${theme.spacing(1.25)} ${theme.spacing(1.5)};
    margin-top: ${theme.spacing(1.5)};
    border: 1px solid ${theme.colors.primary.border};
    border-left: 4px solid ${theme.colors.primary.main};
    border-radius: ${theme.shape.radius.default};
    background: ${theme.colors.background.secondary};
    box-shadow: ${theme.shadows.z3};
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
  paginationBar: css`
    display: flex;
    flex-wrap: wrap;
    justify-content: space-between;
    align-items: center;
    gap: ${theme.spacing(1.5)};
    margin-top: ${theme.spacing(1.5)};
    padding: ${theme.spacing(1)} ${theme.spacing(1.5)};
    border: 1px solid ${theme.colors.border.weak};
    border-radius: ${theme.shape.radius.default};
    background: ${theme.colors.background.secondary};
    font-size: ${theme.typography.bodySmall.fontSize};
    color: ${theme.colors.text.secondary};
  `,
  paginationInfo: css`
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: ${theme.spacing(1.5)};
    font-variant-numeric: tabular-nums;
  `,
  stalePillBtn: css`
    padding: 2px 10px;
    border-radius: 12px;
    border: 1px solid ${theme.colors.warning.border};
    background: ${theme.colors.background.primary};
    color: ${theme.colors.warning.text};
    font-size: 11px;
    font-weight: ${theme.typography.fontWeightMedium};
    cursor: pointer;

    &:hover {
      background: ${theme.colors.background.elevated};
    }
  `,
  paginationControls: css`
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: ${theme.spacing(1)};
  `,
  pageSizeLabel: css`
    display: inline-flex;
    align-items: center;
    gap: ${theme.spacing(0.75)};
    margin: 0 ${theme.spacing(0.5)} 0 0;
  `,
  pageSizeSelect: css`
    height: 28px;
    padding: 0 ${theme.spacing(1)};
    border-radius: ${theme.shape.radius.default};
    border: 1px solid ${theme.colors.border.medium};
    background: ${theme.colors.background.primary};
    color: ${theme.colors.text.primary};
    font-size: ${theme.typography.bodySmall.fontSize};
  `,
});
