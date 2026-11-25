import React from 'react';
import { css } from '@emotion/css';
import { GrafanaTheme2 } from '@grafana/data';
import { useTheme2 } from '@grafana/ui';
import { SummaryCounts } from '../types/api';

export type QuickFilterKey =
  | 'all'
  | 'firing'
  | 'acknowledged'
  | 'non_active_acknowledged'
  | 'dismissed';

interface KpiFilterStripProps {
  summary: SummaryCounts;
  activeFilter: QuickFilterKey;
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
  const nonActiveAckCount = summary.non_active_acknowledged ?? summary.resolved_open_ticket;

  const cards: Array<{
    key: QuickFilterKey;
    label: string;
    count: number;
    subtext: string;
    accentColor: string;
  }> = [
    {
      key: 'firing',
      label: 'Firing',
      count: summary.firing,
      subtext: 'Unacknowledged active alerts',
      accentColor: theme.colors.error.main,
    },
    {
      key: 'acknowledged',
      label: 'Acknowledged',
      count: summary.acknowledged,
      subtext: `${summary.acknowledged_without_incident} without ticket · ${ticketOpen} ticket open`,
      accentColor: theme.colors.warning.main,
    },
    {
      key: 'non_active_acknowledged',
      label: 'Non active Acknowledged',
      count: nonActiveAckCount,
      subtext:
        summary.resolved_open_ticket > 0
          ? `${summary.resolved_open_ticket} open ServiceNow ticket`
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
    <div className={styles.strip} role="region" aria-label="Alert queue summary filters">
      {cards.map((card) => {
        const isActive = activeFilter === card.key;
        return (
          <button
            key={card.key}
            type="button"
            className={`${styles.card} ${isActive ? styles.cardActive : ''}`}
            style={{ borderTopColor: card.accentColor }}
            aria-pressed={isActive}
            onClick={() => onSelectFilter(isActive ? 'all' : card.key)}
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
            <div className={styles.cardValue}>{card.count}</div>
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
    grid-template-columns: repeat(4, minmax(180px, 1fr));
    gap: ${theme.spacing(1.5)};
    margin-bottom: ${theme.spacing(2)};

    @media (max-width: 900px) {
      grid-template-columns: repeat(2, minmax(160px, 1fr));
    }
  `,
  card: css`
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    text-align: left;
    padding: ${theme.spacing(1.5)} ${theme.spacing(2)};
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
    margin-bottom: ${theme.spacing(0.5)};
  `,
  cardLabel: css`
    font-size: ${theme.typography.bodySmall.fontSize};
    font-weight: ${theme.typography.fontWeightMedium};
    color: ${theme.colors.text.primary};
  `,
  dot: css`
    width: 8px;
    height: 8px;
    border-radius: 50%;
    display: inline-block;
  `,
  cardValue: css`
    font-size: ${theme.typography.h2.fontSize};
    font-weight: ${theme.typography.fontWeightBold};
    color: ${theme.colors.text.maxContrast};
    line-height: 1.2;
    margin-bottom: ${theme.spacing(0.5)};
  `,
  cardSubtext: css`
    font-size: ${theme.typography.bodySmall.fontSize};
    color: ${theme.colors.text.secondary};
  `,
});
