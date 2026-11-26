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
    bg: string;
    fg: string;
    subFg: string;
    border: string;
    warningBadge?: string;
    warningTitle?: string;
  }> = [
    {
      key: 'firing',
      label: 'Firing',
      count: summary.firing,
      subtext: firingSubtext,
      bg: '#c4162a',
      fg: '#ffffff',
      subFg: 'rgba(255, 255, 255, 0.9)',
      border: '#e02f44',
    },
    {
      key: 'acknowledged',
      label: 'Acknowledged',
      count: summary.acknowledged,
      subtext: `${summary.acknowledged_without_incident.toLocaleString()} without ticket · ${ticketOpen.toLocaleString()} ticket open`,
      bg: '#eab308',
      fg: '#111217',
      subFg: 'rgba(17, 18, 23, 0.85)',
      border: '#facc15',
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
      bg: '#2e8540',
      fg: '#ffffff',
      subFg: 'rgba(255, 255, 255, 0.9)',
      border: '#3fa355',
    },
    {
      key: 'dismissed',
      label: 'Dismissed',
      count: summary.dismissed,
      subtext: 'Suppressed for episode',
      bg: '#3a414c',
      fg: '#ffffff',
      subFg: 'rgba(255, 255, 255, 0.85)',
      border: '#4f5866',
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
            style={{
              backgroundColor: card.bg,
              borderColor: card.border,
              color: card.fg,
            }}
            aria-selected={isActive}
            onClick={() => onSelectFilter(card.key)}
            data-testid={`kpi-card-${card.key}`}
          >
            <div className={styles.cardHeader}>
              <span className={styles.cardLabel}>{card.label}</span>
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
            <div className={styles.cardValue}>{card.count.toLocaleString()}</div>
            <div className={styles.cardSubtext} style={{ color: card.subFg }}>
              {card.subtext}
            </div>
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
    align-items: center;
    justify-content: space-between;
    min-width: 0;
    min-height: 110px;
    text-align: center;
    padding: ${theme.spacing(1.25)} ${theme.spacing(1.5)};
    border: 1px solid transparent;
    border-radius: 3px;
    cursor: pointer;
    opacity: 0.84;
    transition: opacity 0.15s ease, transform 0.12s ease, box-shadow 0.15s ease, filter 0.15s ease;

    &:hover {
      opacity: 1;
      filter: brightness(1.06);
    }
  `,
  cardActive: css`
    opacity: 1;
    box-shadow: 0 0 0 2px ${theme.colors.background.canvas}, 0 0 0 4px ${theme.colors.text.maxContrast};
  `,
  cardHeader: css`
    display: flex;
    width: 100%;
    justify-content: center;
    align-items: center;
    gap: ${theme.spacing(0.75)};
    flex-wrap: wrap;
  `,
  cardLabel: css`
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: 12px;
    font-weight: ${theme.typography.fontWeightBold};
    text-transform: uppercase;
    letter-spacing: 0.04em;
  `,
  cardValue: css`
    font-size: 38px;
    font-weight: ${theme.typography.fontWeightBold};
    font-variant-numeric: tabular-nums;
    line-height: 1.05;
    margin: ${theme.spacing(0.5)} 0;
  `,
  closedWarningBadge: css`
    display: inline-flex;
    align-items: center;
    padding: 2px 7px;
    border-radius: 10px;
    background: #991b1b;
    color: #ffffff;
    font-size: 10px;
    font-weight: ${theme.typography.fontWeightBold};
    line-height: 1.3;
    white-space: nowrap;
  `,
  cardSubtext: css`
    width: 100%;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: 12px;
    font-weight: ${theme.typography.fontWeightMedium};
  `,
});
