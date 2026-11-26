import React, { useEffect, useMemo, useState } from 'react';
import { css } from '@emotion/css';
import { GrafanaTheme2, SelectableValue } from '@grafana/data';
import { Button, Input, Select, Switch, Tag, useTheme2 } from '@grafana/ui';
import { AlertFilterOptions, AlertSort } from '../types/api';

export type HistoryTimeRange = '' | '24h' | '7d' | '30d' | '90d';

export interface FilterState {
  search: string;
  severities: string[];
  environments: string[];
  sites: string[];
  team: string;
  instance: string;
  labels: string[];
  sort: AlertSort;
  fingerprint: string;
  timeRange?: HistoryTimeRange;
}

interface FilterToolbarProps {
  filters: FilterState;
  filterOptions: AlertFilterOptions;
  autoRefresh: boolean;
  pollIntervalSeconds: number;
  refreshing: boolean;
  lastSyncedAt: Date | null;
  showTimeRange?: boolean;
  onChangeFilters: (next: FilterState) => void;
  onToggleAutoRefresh: (enabled: boolean) => void;
  onManualRefresh: () => void;
  onResetAll: () => void;
}

const SEVERITY_OPTIONS = ['critical', 'warning'];

const SORT_OPTIONS: Array<SelectableValue<AlertSort>> = [
  { label: 'Highest Severity First', value: 'severity_desc' },
  { label: 'Lowest Severity First', value: 'severity_asc' },
  { label: 'Newest Firing First', value: 'starts_at_desc' },
  { label: 'Oldest Firing First', value: 'starts_at_asc' },
  { label: 'Alert Name (A → Z)', value: 'alert_name_asc' },
  { label: 'Alert Name (Z → A)', value: 'alert_name_desc' },
  { label: 'Instance (A → Z)', value: 'instance_asc' },
  { label: 'Instance (Z → A)', value: 'instance_desc' },
  { label: 'Recently Resolved First', value: 'resolved_at_desc' },
];

const TIME_RANGE_OPTIONS: Array<SelectableValue<HistoryTimeRange>> = [
  { label: 'Time: All', value: '' },
  { label: 'Last 24h', value: '24h' },
  { label: 'Last 7d', value: '7d' },
  { label: 'Last 30d', value: '30d' },
  { label: 'Last 90d', value: '90d' },
];

function buildSelectOptions(
  allLabel: string,
  prefix: string,
  values: string[],
  selectedValue: string,
  extraHead: Array<SelectableValue<string>> = []
): Array<SelectableValue<string>> {
  const unique = Array.from(
    new Set(
      [...values, selectedValue && selectedValue !== '__untagged__' ? selectedValue : '']
        .map((v) => v.trim())
        .filter(Boolean)
    )
  ).sort((a, b) => a.localeCompare(b));

  return [
    { label: allLabel, value: '' },
    ...extraHead,
    ...unique.map((v) => ({ label: `${prefix}${v}`, value: v })),
  ];
}

function buildUniqueValues(values: string[], selected: string[]): string[] {
  return Array.from(
    new Set([...values, ...selected].map((v) => v.trim()).filter(Boolean))
  ).sort((a, b) => a.localeCompare(b));
}

export const FilterToolbar: React.FC<FilterToolbarProps> = ({
  filters,
  filterOptions,
  autoRefresh,
  pollIntervalSeconds,
  refreshing,
  lastSyncedAt,
  showTimeRange = false,
  onChangeFilters,
  onToggleAutoRefresh,
  onManualRefresh,
  onResetAll,
}) => {
  const theme = useTheme2();
  const styles = getStyles(theme);
  const [labelInput, setLabelInput] = useState('');
  const [searchInput, setSearchInput] = useState(filters.search);

  useEffect(() => {
    setSearchInput(filters.search);
  }, [filters.search]);

  useEffect(() => {
    if (searchInput === filters.search) {
      return;
    }
    const timer = setTimeout(() => {
      onChangeFilters({ ...filters, search: searchInput });
    }, 250);
    return () => clearTimeout(timer);
  }, [searchInput, filters, onChangeFilters]);

  const envValues = useMemo(
    () => buildUniqueValues(filterOptions.environments ?? [], filters.environments),
    [filterOptions.environments, filters.environments]
  );

  const siteValues = useMemo(
    () => buildUniqueValues(filterOptions.sites ?? [], filters.sites),
    [filterOptions.sites, filters.sites]
  );

  const teamOptions = useMemo(
    () =>
      buildSelectOptions('Team: All', 'Team: ', filterOptions.teams, filters.team, [
        { label: 'Team: Untagged', value: '__untagged__' },
      ]),
    [filterOptions.teams, filters.team]
  );

  const instanceOptions = useMemo(
    () =>
      buildSelectOptions(
        'Instance: All',
        'Instance: ',
        filterOptions.instances ?? [],
        filters.instance
      ),
    [filterOptions.instances, filters.instance]
  );

  const toggleSeverity = (sev: string) => {
    const exists = filters.severities.includes(sev);
    const next = exists ? filters.severities.filter((s) => s !== sev) : [...filters.severities, sev];
    onChangeFilters({ ...filters, severities: next });
  };

  const toggleEnvironment = (env: string) => {
    const exists = filters.environments.includes(env);
    const next = exists
      ? filters.environments.filter((e) => e !== env)
      : [...filters.environments, env];
    onChangeFilters({ ...filters, environments: next });
  };

  const toggleSite = (site: string) => {
    const exists = filters.sites.includes(site);
    const next = exists ? filters.sites.filter((s) => s !== site) : [...filters.sites, site];
    onChangeFilters({ ...filters, sites: next });
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
    Boolean(searchInput.trim()) ||
    filters.severities.length > 0 ||
    filters.environments.length > 0 ||
    filters.sites.length > 0 ||
    Boolean(filters.team) ||
    Boolean(filters.instance) ||
    filters.labels.length > 0 ||
    Boolean(filters.fingerprint) ||
    Boolean(filters.timeRange);

  return (
    <div className={styles.container}>
      <div className={styles.row}>
        <div className={styles.searchBox}>
          <Input
            placeholder="Search alert name, instance, team, summary, fingerprint, or INC..."
            value={searchInput}
            onChange={(e) => setSearchInput(e.currentTarget.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                e.preventDefault();
                if (searchInput !== filters.search) {
                  onChangeFilters({ ...filters, search: searchInput });
                }
              }
            }}
            aria-label="Search alerts"
          />
        </div>

        <div className={styles.teamSelectBox}>
          <Select
            aria-label="Filter by team"
            options={teamOptions}
            value={filters.team}
            onChange={(opt) =>
              onChangeFilters({
                ...filters,
                team: opt.value ?? '',
              })
            }
          />
        </div>

        <div className={styles.instanceSelectBox}>
          <Select
            aria-label="Filter by instance"
            options={instanceOptions}
            value={filters.instance}
            isSearchable
            onChange={(opt) =>
              onChangeFilters({
                ...filters,
                instance: opt.value ?? '',
              })
            }
          />
        </div>

        {showTimeRange && (
          <div className={styles.compactSelectBox}>
            <Select
              aria-label="Filter by time range"
              options={TIME_RANGE_OPTIONS}
              value={filters.timeRange ?? ''}
              onChange={(opt) =>
                onChangeFilters({
                  ...filters,
                  timeRange: (opt.value ?? '') as HistoryTimeRange,
                })
              }
            />
          </div>
        )}

        <div className={styles.sortSelectBox}>
          <Select
            aria-label="Sort alerts"
            options={SORT_OPTIONS}
            value={filters.sort}
            onChange={(opt) =>
              onChangeFilters({
                ...filters,
                sort: (opt.value ?? 'severity_desc') as AlertSort,
              })
            }
          />
        </div>
      </div>

      <div className={styles.bottomRow}>
        <div className={styles.quickSelectContainer}>
          <div className={styles.chipGroup} role="group" aria-label="Filter by severity">
            <span className={styles.chipLabel}>Severity:</span>
            {SEVERITY_OPTIONS.map((sev) => {
              const active = filters.severities.includes(sev);
              return (
                <button
                  key={sev}
                  type="button"
                  className={`${styles.quickChip} ${styles.sevChip} ${active ? styles.quickChipActive : ''}`}
                  aria-pressed={active}
                  onClick={() => toggleSeverity(sev)}
                >
                  {sev}
                </button>
              );
            })}
          </div>

          <div
            className={styles.chipGroup}
            role="group"
            aria-label="Filter by environment"
            data-testid="env-quick-select"
          >
            <span className={styles.chipLabel}>Env:</span>
            {envValues.length > 0 ? (
              envValues.map((env) => {
                const active = filters.environments.includes(env);
                return (
                  <button
                    key={env}
                    type="button"
                    data-testid={`env-chip-${env}`}
                    className={`${styles.quickChip} ${active ? styles.quickChipActive : ''}`}
                    aria-pressed={active}
                    onClick={() => toggleEnvironment(env)}
                  >
                    {env}
                  </button>
                );
              })
            ) : (
              <span className={styles.emptyChipHint}>None</span>
            )}
          </div>

          <div
            className={styles.chipGroup}
            role="group"
            aria-label="Filter by site"
            data-testid="site-quick-select"
          >
            <span className={styles.chipLabel}>Site:</span>
            {siteValues.length > 0 ? (
              siteValues.map((site) => {
                const active = filters.sites.includes(site);
                return (
                  <button
                    key={site}
                    type="button"
                    data-testid={`site-chip-${site}`}
                    className={`${styles.quickChip} ${active ? styles.quickChipActive : ''}`}
                    aria-pressed={active}
                    onClick={() => toggleSite(site)}
                  >
                    {site}
                  </button>
                );
              })
            ) : (
              <span className={styles.emptyChipHint}>None</span>
            )}
          </div>

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
            <Button
              variant="secondary"
              fill="text"
              size="sm"
              onClick={() => {
                setSearchInput('');
                onResetAll();
              }}
            >
              Clear filters
            </Button>
          )}
        </div>

        <div className={styles.refreshControls}>
          <div className={styles.labelInputGroup}>
            <Input
              placeholder="Label key=value"
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
            <Button
              variant="secondary"
              size="sm"
              onClick={addLabelFilter}
              disabled={!labelInput.includes('=')}
            >
              Add Label
            </Button>
          </div>
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

const getStyles = (theme: GrafanaTheme2) => ({
  container: css`
    display: flex;
    flex-direction: column;
    gap: ${theme.spacing(1.25)};
    padding: ${theme.spacing(1.5)};
    margin-bottom: ${theme.spacing(1.5)};
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
    min-width: 220px;
  `,
  compactSelectBox: css`
    flex: 0 1 160px;
    min-width: 135px;
  `,
  teamSelectBox: css`
    flex: 0 1 180px;
    min-width: 145px;
  `,
  instanceSelectBox: css`
    flex: 0 1 200px;
    min-width: 165px;
  `,
  sortSelectBox: css`
    flex: 0 1 205px;
    min-width: 170px;
  `,
  labelInputGroup: css`
    display: flex;
    gap: ${theme.spacing(0.75)};
    align-items: center;
    width: 220px;
    max-width: 100%;
  `,
  bottomRow: css`
    display: flex;
    flex-wrap: wrap;
    justify-content: space-between;
    align-items: center;
    gap: ${theme.spacing(1.25)};
  `,
  quickSelectContainer: css`
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: ${theme.spacing(1.5)};
  `,
  chipGroup: css`
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: ${theme.spacing(0.625)};
  `,
  chipLabel: css`
    font-size: ${theme.typography.bodySmall.fontSize};
    color: ${theme.colors.text.secondary};
    margin-right: ${theme.spacing(0.25)};
  `,
  emptyChipHint: css`
    font-size: ${theme.typography.bodySmall.fontSize};
    color: ${theme.colors.text.disabled};
  `,
  quickChip: css`
    padding: 3px 10px;
    border-radius: 12px;
    border: 1px solid ${theme.colors.border.medium};
    background: ${theme.colors.background.primary};
    color: ${theme.colors.text.secondary};
    font-size: ${theme.typography.bodySmall.fontSize};
    cursor: pointer;

    &:hover {
      border-color: ${theme.colors.border.strong};
      color: ${theme.colors.text.primary};
    }
  `,
  sevChip: css`
    text-transform: capitalize;
  `,
  quickChipActive: css`
    background: ${theme.colors.primary.main};
    border-color: ${theme.colors.primary.main};
    color: ${theme.colors.primary.contrastText};

    &:hover {
      border-color: ${theme.colors.primary.main};
      color: ${theme.colors.primary.contrastText};
    }
  `,
  refreshControls: css`
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: ${theme.spacing(1.25)};
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
