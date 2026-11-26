import React from 'react';
import { css } from '@emotion/css';
import { GrafanaTheme2 } from '@grafana/data';
import { useTheme2 } from '@grafana/ui';
import { SummaryCounts } from '../types/api';

export type QuickFilterKey =
  | 'firing'
  | 'acknowledged'
  | 'non_active_acknowledged'
  | 'dismissed';

interface KpiFilterStripProps {
  summary: SummaryCounts;
  activeFilter: QuickFilterKey | null;
  onSelectFilter: (key: QuickFilterKey) => void;
}

export const KpiFilterStrip: React.FC<KpiFilterStripProps> = ({
  summary,
  activeFilter,
  onSelectFilter,
}) => {
  const theme = useTheme2();
  const styles = getStyles(theme);

  const ticketOpen = Math.max(0, summary.acknowledged - summary.acknowledged_without_incident);
  const nonActiveAckCount = summary.non_active_acknowledged ?? 0;
  const activeClosedTickets = summary.active_closed_ticket ?? 0;
  const firingCrit = summary.firing_critical ?? 0;
  const firingWarn = summary.firing_warning ?? 0;
  const firingSubtext =
    summary.firing > 0 && (firingCrit > 0 || firingWarn > 0)
      ? `${firingCrit.toLocaleString()} critical · ${firingWarn.toLocaleString()} warning`
      : 'Unacknowledged active alerts';

  const cards: Array<{
    key: QuickFilterKey;
    label: string;
    count: number;
    subtext: string;
    accentColor: string;
    warningBadge?: string;
    warningTitle?: string;
  }> = [
    {
      key: 'firing',
      label: 'Firing',
      count: summary.firing,
      subtext: firingSubtext,
      accentColor: theme.colors.error.main,
    },
    {
      key: 'acknowledged',
      label: 'Acknowledged',
      count: summary.acknowledged,
      subtext: `${summary.acknowledged_without_incident.toLocaleString()} without ticket · ${ticketOpen.toLocaleString()} ticket open`,
      accentColor: theme.colors.warning.main,
      warningBadge:
        activeClosedTickets > 0
          ? `⚠ ${activeClosedTickets.toLocaleString()} closed ticket${activeClosedTickets === 1 ? '' : 's'}`
          : undefined,
      warningTitle:
        activeClosedTickets > 0
          ? `${activeClosedTickets} active alert(s) have a Resolved/Closed/Canceled ServiceNow incident.`
          : undefined,
    },
    {
      key: 'non_active_acknowledged',
      label: 'Non active Acknowledged',
      count: nonActiveAckCount,
      subtext:
        summary.resolved_open_ticket > 0
          ? `${summary.resolved_open_ticket.toLocaleString()} open ServiceNow ticket`
          : 'Resolved after acknowledgement',
      accentColor: '#a855f7',
    },
    {
      key: 'dismissed',
      label: 'Dismissed',
      count: summary.dismissed,
      subtext: 'Suppressed for episode',
      accentColor: theme.colors.text.secondary,
    },
  ];

  return (
    <div className={styles.strip} role="tablist" aria-label="Alert queue summary filters">
      {cards.map((card) => {
        const isActive = activeFilter === card.key;
        return (
          <button
            key={card.key}
            type="button"
            role="tab"
            className={`${styles.card} ${isActive ? styles.cardActive : ''}`}
            style={{ borderTopColor: card.accentColor }}
            aria-selected={isActive}
            onClick={() => onSelectFilter(card.key)}
            data-testid={`kpi-card-${card.key}`}
          >
            <div className={styles.cardHeader}>
              <span className={styles.cardLabel}>{card.label}</span>
              <span
                className={styles.dot}
                style={{ backgroundColor: card.accentColor }}
                aria-hidden="true"
              />
            </div>
            <div className={styles.cardValueRow}>
              <div className={styles.cardValue}>{card.count.toLocaleString()}</div>
              {card.warningBadge && (
                <span
                  className={styles.closedWarningBadge}
                  data-testid="kpi-active-closed-ticket-badge"
                  title={card.warningTitle}
                >
                  {card.warningBadge}
                </span>
              )}
            </div>
            <div className={styles.cardSubtext}>{card.subtext}</div>
          </button>
        );
      })}
    </div>
  );
};

const getStyles = (theme: GrafanaTheme2) => ({
  strip: css`
    display: grid;
    width: 100%;
    box-sizing: border-box;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    gap: ${theme.spacing(1.5)};
    margin-bottom: ${theme.spacing(1.5)};

    @media (max-width: 900px) {
      grid-template-columns: repeat(2, minmax(0, 1fr));
    }

    @media (max-width: 520px) {
      grid-template-columns: 1fr;
    }
  `,
  card: css`
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    min-width: 0;
    text-align: left;
    padding: ${theme.spacing(1.25)} ${theme.spacing(1.75)};
    background: ${theme.colors.background.secondary};
    border: 1px solid ${theme.colors.border.weak};
    border-top-width: 3px;
    border-radius: ${theme.shape.radius.default};
    cursor: pointer;
    transition: border-color 0.15s ease, box-shadow 0.15s ease;

    &:hover {
      border-color: ${theme.colors.border.strong};
      background: ${theme.colors.background.elevated};
    }
  `,
  cardActive: css`
    background: ${theme.colors.background.elevated};
    box-shadow: 0 0 0 2px ${theme.colors.primary.main};
  `,
  cardHeader: css`
    display: flex;
    width: 100%;
    justify-content: space-between;
    align-items: center;
    gap: ${theme.spacing(0.75)};
    margin-bottom: ${theme.spacing(0.25)};
  `,
  cardLabel: css`
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: ${theme.typography.bodySmall.fontSize};
    font-weight: ${theme.typography.fontWeightMedium};
    color: ${theme.colors.text.primary};
  `,
  dot: css`
    flex-shrink: 0;
    width: 8px;
    height: 8px;
    border-radius: 50%;
    display: inline-block;
  `,
  cardValueRow: css`
    display: flex;
    width: 100%;
    align-items: center;
    justify-content: space-between;
    gap: ${theme.spacing(1)};
    flex-wrap: wrap;
    margin-bottom: ${theme.spacing(0.25)};
  `,
  cardValue: css`
    font-size: ${theme.typography.h2.fontSize};
    font-weight: ${theme.typography.fontWeightBold};
    font-variant-numeric: tabular-nums;
    color: ${theme.colors.text.maxContrast};
    line-height: 1.2;
  `,
  closedWarningBadge: css`
    display: inline-flex;
    align-items: center;
    padding: 2px 8px;
    border-radius: 12px;
    background: ${theme.colors.error.main};
    color: ${theme.colors.error.contrastText};
    font-size: 11px;
    font-weight: ${theme.typography.fontWeightBold};
    line-height: 1.3;
    white-space: nowrap;
  `,
  cardSubtext: css`
    width: 100%;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: ${theme.typography.bodySmall.fontSize};
    color: ${theme.colors.text.secondary};
  `,
});
