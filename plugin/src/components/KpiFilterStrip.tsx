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
      : 'No active unacknowledged alerts';

  let ackSubtext = 'Active alerts with ServiceNow tickets';
  if (summary.acknowledged_without_incident > 0) {
    ackSubtext = `${summary.acknowledged_without_incident.toLocaleString()} missing ticket · ${ticketOpen.toLocaleString()} linked`;
  } else if (summary.acknowledged > 0 && activeClosedTickets > 0) {
    ackSubtext = `${Math.max(0, summary.acknowledged - activeClosedTickets).toLocaleString()} open · ${activeClosedTickets.toLocaleString()} closed ticket`;
  } else if (summary.acknowledged > 0) {
    ackSubtext = `${summary.acknowledged.toLocaleString()} with open ServiceNow ticket${summary.acknowledged === 1 ? '' : 's'}`;
  }

  const cards: Array<{
    key: QuickFilterKey;
    label: string;
    count: number;
    subtext: string;
    accent: string;
    valueColor: string;
    tintRgb: string;
    warningBadge?: string;
    warningTitle?: string;
  }> = [
    {
      key: 'firing',
      label: 'Firing',
      count: summary.firing,
      subtext: firingSubtext,
      accent: theme.isDark ? '#F2495C' : '#D10E5A',
      valueColor: theme.isDark ? '#FF6B7B' : '#C4162A',
      tintRgb: theme.isDark ? '242, 73, 92' : '209, 14, 90',
    },
    {
      key: 'acknowledged',
      label: 'Acknowledged',
      count: summary.acknowledged,
      subtext: ackSubtext,
      accent: theme.isDark ? '#FF9830' : '#D97706',
      valueColor: theme.isDark ? '#FFB357' : '#B45309',
      tintRgb: theme.isDark ? '255, 152, 48' : '217, 119, 6',
      warningBadge:
        activeClosedTickets > 0
          ? `⚠ ${activeClosedTickets.toLocaleString()} closed`
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
      accent: theme.isDark ? '#73BF69' : '#2E8540',
      valueColor: theme.isDark ? '#73BF69' : '#1F7A33',
      tintRgb: theme.isDark ? '115, 191, 105' : '46, 133, 64',
    },
    {
      key: 'dismissed',
      label: 'Dismissed',
      count: summary.dismissed,
      subtext: 'Suppressed for episode',
      accent: theme.isDark ? '#8E99A8' : '#64748B',
      valueColor: theme.isDark ? '#C7D0D9' : '#475569',
      tintRgb: theme.isDark ? '142, 153, 168' : '100, 116, 139',
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
            style={{ '--card-rgb': card.tintRgb } as React.CSSProperties}
            aria-selected={isActive}
            onClick={() => onSelectFilter(card.key)}
            data-testid={`kpi-card-${card.key}`}
          >
            <span
              className={styles.topAccentBar}
              style={{
                background: card.accent,
                opacity: isActive ? 1 : 0.75,
              }}
              aria-hidden="true"
            />
            <div className={styles.cardHeader}>
              <span className={styles.titleCluster}>
                <span
                  className={styles.statusDot}
                  style={{
                    backgroundColor: card.accent,
                    boxShadow: isActive ? `0 0 8px rgba(${card.tintRgb}, 0.75)` : 'none',
                  }}
                  aria-hidden="true"
                />
                <span className={`${styles.cardLabel} ${isActive ? styles.cardLabelActive : ''}`}>
                  {card.label}
                </span>
              </span>
              {card.warningBadge ? (
                <span
                  className={styles.closedWarningBadge}
                  data-testid="kpi-active-closed-ticket-badge"
                  title={card.warningTitle}
                >
                  {card.warningBadge}
                </span>
              ) : isActive ? (
                <span
                  className={styles.viewingPill}
                  style={{
                    color: card.valueColor,
                    backgroundColor: `rgba(${card.tintRgb}, 0.16)`,
                    borderColor: `rgba(${card.tintRgb}, 0.42)`,
                  }}
                >
                  Viewing
                </span>
              ) : null}
            </div>
            <div
              className={styles.cardValue}
              style={{
                color: card.valueColor,
                textShadow: theme.isDark ? `0 2px 12px rgba(${card.tintRgb}, 0.28)` : 'none',
              }}
            >
              {card.count.toLocaleString()}
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
    position: relative;
    overflow: hidden;
    display: flex;
    flex-direction: column;
    justify-content: space-between;
    min-width: 0;
    min-height: 108px;
    padding: 14px 18px;
    text-align: left;
    border-radius: 6px;
    background-color: ${theme.colors.background.secondary};
    background-image: linear-gradient(
      180deg,
      rgba(var(--card-rgb), 0.11) 0%,
      rgba(var(--card-rgb), 0.03) 100%
    );
    border: 1px solid rgba(var(--card-rgb), 0.25);
    cursor: pointer;
    transition: transform 0.14s ease, border-color 0.14s ease, box-shadow 0.14s ease,
      background-image 0.14s ease;

    &:hover {
      transform: translateY(-1px);
      border-color: rgba(var(--card-rgb), 0.55);
    }

    &:focus-visible {
      outline: 2px solid ${theme.colors.primary.border};
      outline-offset: 2px;
    }
  `,
  cardActive: css`
    border-color: rgba(var(--card-rgb), 0.8);
    background-image:
      radial-gradient(ellipse 90% 80% at 100% 0%, rgba(var(--card-rgb), 0.24) 0%, transparent 70%),
      linear-gradient(180deg, rgba(var(--card-rgb), 0.2) 0%, rgba(var(--card-rgb), 0.06) 100%);
    box-shadow: 0 6px 20px rgba(var(--card-rgb), 0.18);

    &:hover {
      border-color: rgba(var(--card-rgb), 0.92);
    }
  `,
  topAccentBar: css`
    position: absolute;
    top: 0;
    left: 0;
    right: 0;
    height: 3px;
    pointer-events: none;
  `,
  cardHeader: css`
    display: flex;
    width: 100%;
    justify-content: space-between;
    align-items: center;
    gap: ${theme.spacing(1)};
  `,
  titleCluster: css`
    display: inline-flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
  `,
  statusDot: css`
    width: 8px;
    height: 8px;
    border-radius: 50%;
    flex-shrink: 0;
  `,
  cardLabel: css`
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: 11px;
    font-weight: ${theme.typography.fontWeightBold};
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: ${theme.colors.text.secondary};
  `,
  cardLabelActive: css`
    color: ${theme.colors.text.primary};
  `,
  viewingPill: css`
    display: inline-flex;
    align-items: center;
    flex-shrink: 0;
    padding: 1px 7px;
    border-radius: 10px;
    border: 1px solid transparent;
    font-size: 10px;
    font-weight: ${theme.typography.fontWeightMedium};
    line-height: 1.4;
    white-space: nowrap;
  `,
  cardValue: css`
    font-size: 42px;
    font-weight: 600;
    font-variant-numeric: tabular-nums;
    letter-spacing: -0.025em;
    line-height: 1;
    margin: ${theme.spacing(1)} 0 ${theme.spacing(0.75)};
  `,
  closedWarningBadge: css`
    display: inline-flex;
    align-items: center;
    flex-shrink: 0;
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
    font-weight: ${theme.typography.fontWeightRegular};
    color: ${theme.colors.text.secondary};
  `,
});
