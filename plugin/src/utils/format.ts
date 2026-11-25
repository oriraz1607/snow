import { Alert, PLUGIN_ID } from '../types/api';

const PRIORITY_LABELS: Record<number, string> = {
  1: '1 - Critical',
  2: '2 - High',
  3: '3 - Moderate',
  4: '4 - Low',
  5: '5 - Planning',
};

export function calculateServiceNowPriority(impact: 1 | 2 | 3, urgency: 1 | 2 | 3): {
  value: number;
  label: string;
} {
  const raw = Math.min(5, Math.max(1, impact + urgency - 1));
  return {
    value: raw,
    label: PRIORITY_LABELS[raw] ?? `${raw}`,
  };
}

export function defaultImpactUrgencyForSeverity(severity: string): { impact: 1 | 2 | 3; urgency: 1 | 2 | 3 } {
  switch (severity.toLowerCase()) {
    case 'critical':
      return { impact: 1, urgency: 1 };
    case 'warning':
      return { impact: 2, urgency: 2 };
    default:
      return { impact: 3, urgency: 3 };
  }
}

export function buildDefaultShortDescription(alert: Alert): string {
  const sev = (alert.severity || 'unknown').toUpperCase();
  const summary = alert.annotations?.summary?.trim();
  const raw = summary ? `[${sev}] ${alert.alert_name}: ${summary}` : `[${sev}] ${alert.alert_name}`;
  return raw.length <= 160 ? raw : `${raw.slice(0, 157)}...`;
}

export function buildDefaultDescription(alert: Alert): string {
  const origin = typeof window !== 'undefined' && window.location?.origin ? window.location.origin : '';
  const alertLink = `${origin}/a/${PLUGIN_ID}/alerts/${alert.id}`;
  const lines = [
    `Alert Name: ${alert.alert_name}`,
    `Severity: ${alert.severity}`,
    `Starts At (UTC): ${formatUtcTimestamp(alert.starts_at)}`,
    `Fingerprint: ${alert.fingerprint}`,
    `Grafana Alert Link: ${alertLink}`,
  ];

  if (alert.annotations?.summary) {
    lines.push(`Summary: ${alert.annotations.summary}`);
  }
  if (alert.annotations?.description) {
    lines.push(`Description: ${alert.annotations.description}`);
  }
  if (alert.annotations?.runbook_url) {
    lines.push(`Runbook URL: ${alert.annotations.runbook_url}`);
  }
  if (alert.generator_url) {
    lines.push(`Prometheus Generator URL: ${alert.generator_url}`);
  }

  const labelEntries = Object.entries(alert.labels ?? {}).sort(([a], [b]) => a.localeCompare(b));
  if (labelEntries.length > 0) {
    lines.push('', 'Labels:');
    for (const [k, v] of labelEntries) {
      lines.push(`- ${k}: ${v}`);
    }
  }

  return lines.join('\n');
}

export function formatUtcTimestamp(isoString?: string | null): string {
  if (!isoString) {
    return '—';
  }
  const dt = new Date(isoString);
  if (Number.isNaN(dt.getTime())) {
    return isoString;
  }
  return dt.toISOString().replace('T', ' ').replace(/\.\d{3}Z$/, ' UTC');
}

export function formatRelativeTime(isoString?: string | null, nowMs: number = Date.now()): string {
  if (!isoString) {
    return '—';
  }
  const dt = new Date(isoString);
  if (Number.isNaN(dt.getTime())) {
    return isoString;
  }
  const diffSec = Math.max(0, Math.floor((nowMs - dt.getTime()) / 1000));
  if (diffSec < 60) {
    return `${diffSec}s`;
  }
  const diffMin = Math.floor(diffSec / 60);
  if (diffMin < 60) {
    return `${diffMin}m`;
  }
  const diffHr = Math.floor(diffMin / 60);
  const remMin = diffMin % 60;
  if (diffHr < 24) {
    return remMin > 0 ? `${diffHr}h ${remMin}m` : `${diffHr}h`;
  }
  const diffDays = Math.floor(diffHr / 24);
  return `${diffDays}d`;
}

export function clampPollIntervalSeconds(val?: number): number {
  if (typeof val !== 'number' || Number.isNaN(val) || val <= 0) {
    return 15;
  }
  return Math.min(120, Math.max(5, Math.round(val)));
}

export function generateUuid(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID();
  }
  return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, (c) => {
    const r = Math.floor(Math.random() * 16);
    const v = c === 'x' ? r : (r & 0x3) | 0x8;
    return v.toString(16);
  });
}
