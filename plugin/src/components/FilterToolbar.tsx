import React, { useState } from 'react';
import { css } from '@emotion/css';
import { GrafanaTheme2, SelectableValue } from '@grafana/data';
import { Button, Input, Select, Switch, Tag, useTheme2 } from '@grafana/ui';
import { AlertSort, IncidentDriftStatus } from '../types/api';

export type SnowStatusFilter =
  | 'all'
  | 'no_incident'
  | 'incident_open'
  | 'resolved_open_ticket'
  | 'active_closed_ticket';

export interface FilterState {
  search: string;
  severities: string[];
  labels: string[];
  snowStatus: SnowStatusFilter;
  sort: AlertSort;
  fingerprint: string;
}

interface FilterToolbarProps {
  filters: FilterState;
  autoRefresh: boolean;
  pollIntervalSeconds: number;
  refreshing: boolean;
  lastSyncedAt: Date | null;
  onChangeFilters: (next: FilterState) => void;
  onToggleAutoRefresh: (enabled: boolean) => void;
  onManualRefresh: () => void;
  onResetAll: () => void;
}

const SEVERITY_OPTIONS = ['critical', 'warning', 'info', 'unknown'];

const SNOW_STATUS_OPTIONS: Array<SelectableValue<SnowStatusFilter>> = [
  { label: 'ServiceNow: All', value: 'all' },
  { label: 'No Incident Linked', value: 'no_incident' },
  { label: 'Has Linked Incident', value: 'incident_open' },
  { label: 'Drift: Resolved w/ Open Ticket', value: 'resolved_open_ticket' },
  { label: 'Drift: Active w/ Closed Ticket', value: 'active_closed_ticket' },
];

const SORT_OPTIONS: Array<SelectableValue<AlertSort>> = [
  { label: 'Newest Firing First', value: 'starts_at_desc' },
  { label: 'Oldest Firing First', value: 'starts_at_asc' },
  { label: 'Highest Severity First', value: 'severity_desc' },
  { label: 'Recently Resolved First', value: 'resolved_at_desc' },
];

export const FilterToolbar: React.FC<FilterToolbarProps> = ({
  filters,
  autoRefresh,
  pollIntervalSeconds,
  refreshing,
  lastSyncedAt,
  onChangeFilters,
  onToggleAutoRefresh,
  onManualRefresh,
  onResetAll,
}) => {
  const theme = useTheme2();
  const styles = getStyles(theme);
  const [labelInput, setLabelInput] = useState('');

  const toggleSeverity = (sev: string) => {
    const exists = filters.severities.includes(sev);
    const next = exists ? filters.severities.filter((s) => s !== sev) : [...filters.severities, sev];
    onChangeFilters({ ...filters, severities: next });
  };

  const addLabelFilter = () => {
    const trimmed = labelInput.trim();
    if (!trimmed || !trimmed.includes('=')) {
      return;
    }
    if (!filters.labels.includes(trimmed)) {
      onChangeFilters({ ...filters, labels: [...filters.labels, trimmed] });
    }
    setLabelInput('');
  };

  const removeLabelFilter = (pair: string) => {
    onChangeFilters({ ...filters, labels: filters.labels.filter((l) => l !== pair) });
  };

  const hasActiveFilters =
    Boolean(filters.search.trim()) ||
    filters.severities.length > 0 ||
    filters.labels.length > 0 ||
    filters.snowStatus !== 'all' ||
    Boolean(filters.fingerprint);

  return (
    <div className={styles.container}>
      <div className={styles.row}>
        <div className={styles.searchBox}>
          <Input
            placeholder="Search alert name, fingerprint, summary, or INC..."
            value={filters.search}
            onChange={(e) => onChangeFilters({ ...filters, search: e.currentTarget.value })}
            aria-label="Search alerts"
          />
        </div>

        <div className={styles.selectBox}>
          <Select
            aria-label="ServiceNow status filter"
            options={SNOW_STATUS_OPTIONS}
            value={filters.snowStatus}
            onChange={(opt) =>
              onChangeFilters({
                ...filters,
                snowStatus: (opt.value ?? 'all') as SnowStatusFilter,
              })
            }
          />
        </div>

        <div className={styles.selectBox}>
          <Select
            aria-label="Sort alerts"
            options={SORT_OPTIONS}
            value={filters.sort}
            onChange={(opt) =>
              onChangeFilters({
                ...filters,
                sort: (opt.value ?? 'starts_at_desc') as AlertSort,
              })
            }
          />
        </div>

        <div className={styles.labelInputGroup}>
          <Input
            placeholder="Add label key=value"
            value={labelInput}
            onChange={(e) => setLabelInput(e.currentTarget.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                e.preventDefault();
                addLabelFilter();
              }
            }}
            aria-label="Label filter key=value"
          />
          <Button variant="secondary" size="sm" onClick={addLabelFilter} disabled={!labelInput.includes('=')}>
            Add Label
          </Button>
        </div>
      </div>

      <div className={styles.bottomRow}>
        <div className={styles.severityChips}>
          <span className={styles.chipLabel}>Severity:</span>
          {SEVERITY_OPTIONS.map((sev) => {
            const active = filters.severities.includes(sev);
            return (
              <button
                key={sev}
                type="button"
                className={`${styles.sevChip} ${active ? styles.sevChipActive : ''}`}
                aria-pressed={active}
                onClick={() => toggleSeverity(sev)}
              >
                {sev}
              </button>
            );
          })}

          {filters.labels.map((pair) => (
            <Tag
              key={pair}
              name={`${pair} ×`}
              colorIndex={1}
              onClick={() => removeLabelFilter(pair)}
            />
          ))}

          {filters.fingerprint && (
            <Tag
              name={`fingerprint=${filters.fingerprint} ×`}
              colorIndex={2}
              onClick={() => onChangeFilters({ ...filters, fingerprint: '' })}
            />
          )}

          {hasActiveFilters && (
            <Button variant="secondary" fill="text" size="sm" onClick={onResetAll}>
              Clear filters
            </Button>
          )}
        </div>

        <div className={styles.refreshControls}>
          <label className={styles.autoRefreshLabel}>
            <Switch
              value={autoRefresh}
              onChange={(e) => onToggleAutoRefresh(e.currentTarget.checked)}
              aria-label="Auto-refresh alerts"
            />
            <span>Auto-refresh ({pollIntervalSeconds}s)</span>
          </label>
          <Button
            variant="secondary"
            size="sm"
            icon="sync"
            onClick={onManualRefresh}
            disabled={refreshing}
          >
            {refreshing ? 'Refreshing...' : 'Refresh'}
          </Button>
          <span className={styles.syncedText}>
            {lastSyncedAt
              ? `Synced ${lastSyncedAt.toLocaleTimeString([], { hour12: false })}`
              : 'Not synced yet'}
          </span>
        </div>
      </div>
    </div>
  );
};

export function snowStatusToQueryParams(snowStatus: SnowStatusFilter): {
  has_incident?: boolean;
  incident_drift?: 'all' | IncidentDriftStatus;
} {
  switch (snowStatus) {
    case 'no_incident':
      return { has_incident: false, incident_drift: 'all' };
    case 'incident_open':
      return { has_incident: true, incident_drift: 'all' };
    case 'resolved_open_ticket':
      return { incident_drift: 'resolved_open_ticket' };
    case 'active_closed_ticket':
      return { incident_drift: 'active_closed_ticket' };
    default:
      return { incident_drift: 'all' };
  }
}

const getStyles = (theme: GrafanaTheme2) => ({
  container: css`
    display: flex;
    flex-direction: column;
    gap: ${theme.spacing(1.5)};
    padding: ${theme.spacing(1.5)};
    margin-bottom: ${theme.spacing(2)};
    background: ${theme.colors.background.secondary};
    border: 1px solid ${theme.colors.border.weak};
    border-radius: ${theme.shape.radius.default};
  `,
  row: css`
    display: flex;
    flex-wrap: wrap;
    gap: ${theme.spacing(1)};
    align-items: center;
  `,
  searchBox: css`
    flex: 2 1 260px;
  `,
  selectBox: css`
    flex: 1 1 200px;
    min-width: 190px;
  `,
  labelInputGroup: css`
    display: flex;
    gap: ${theme.spacing(0.75)};
    align-items: center;
    flex: 1 1 230px;
  `,
  bottomRow: css`
    display: flex;
    flex-wrap: wrap;
    justify-content: space-between;
    align-items: center;
    gap: ${theme.spacing(1.5)};
  `,
  severityChips: css`
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: ${theme.spacing(0.75)};
  `,
  chipLabel: css`
    font-size: ${theme.typography.bodySmall.fontSize};
    color: ${theme.colors.text.secondary};
    margin-right: ${theme.spacing(0.5)};
  `,
  sevChip: css`
    padding: 3px 10px;
    border-radius: 12px;
    border: 1px solid ${theme.colors.border.medium};
    background: ${theme.colors.background.primary};
    color: ${theme.colors.text.secondary};
    font-size: ${theme.typography.bodySmall.fontSize};
    text-transform: capitalize;
    cursor: pointer;
  `,
  sevChipActive: css`
    background: ${theme.colors.primary.main};
    border-color: ${theme.colors.primary.main};
    color: ${theme.colors.primary.contrastText};
  `,
  refreshControls: css`
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: ${theme.spacing(1.5)};
  `,
  autoRefreshLabel: css`
    display: inline-flex;
    align-items: center;
    gap: ${theme.spacing(0.75)};
    font-size: ${theme.typography.bodySmall.fontSize};
    color: ${theme.colors.text.secondary};
    margin: 0;
  `,
  syncedText: css`
    font-size: ${theme.typography.bodySmall.fontSize};
    color: ${theme.colors.text.secondary};
  `,
});
