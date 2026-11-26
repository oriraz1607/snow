import { Alert, PLUGIN_ID, TeamSource } from '../types/api';

const PRIORITY_LABELS: Record<number, string> = {
  1: '1 - Critical',
  2: '2 - High',
  3: '3 - Moderate',
  4: '4 - Low',
  5: '5 - Planning',
};

const SEVERITY_RANK: Record<string, number> = {
  critical: 1,
  warning: 2,
  info: 3,
  unknown: 4,
};

export function getAlertEnvironment(alert: Alert): string {
  return (
    alert.environment?.trim() ||
    alert.labels?.environment?.trim() ||
    alert.labels?.env?.trim() ||
    ''
  );
}

export function getAlertSite(alert: Alert): string {
  return alert.site?.trim() || alert.labels?.site?.trim() || '';
}

export function getAlertInstance(alert: Alert): string {
  const direct = alert.instance?.trim();
  if (direct) {
    return direct;
  }
  const labels = alert.labels ?? {};
  for (const key of ['instance', 'host', 'hostname', 'node', 'pod']) {
    const val = labels[key]?.trim();
    if (val) {
      return val;
    }
  }
  return '';
}

export function getAlertTeam(alert: Alert): {
  team: string;
  source: TeamSource;
  taggedBy: string | null;
} {
  const labelTeam = alert.labels?.team?.trim();
  if (labelTeam) {
    return { team: labelTeam, source: 'label', taggedBy: null };
  }
  const assignedTeam = alert.assigned_team?.trim();
  if (assignedTeam) {
    return { team: assignedTeam, source: 'operator', taggedBy: alert.assigned_team_by ?? null };
  }
  const derivedTeam = alert.team?.trim();
  if (derivedTeam) {
    return {
      team: derivedTeam,
      source: alert.team_source ?? 'operator',
      taggedBy: alert.assigned_team_by ?? null,
    };
  }
  return { team: '', source: 'none', taggedBy: null };
}

export function isAlertSelectableForIncident(alert: Alert): boolean {
  if (alert.state !== 'firing' && alert.state !== 'acknowledged') {
    return false;
  }
  const sync = alert.servicenow_incident?.sync_status;
  return !sync || sync === 'failed';
}

export function isAlertSelectableForBulk(alert: Alert): boolean {
  return alert.state === 'firing' || alert.state === 'acknowledged';
}

export function hasOpenServiceNowIncident(alert?: Alert | null): boolean {
  const inc = alert?.servicenow_incident;
  if (!inc) {
    return false;
  }
  if (inc.sync_status !== 'created' && inc.sync_status !== 'linked') {
    return false;
  }
  return inc.snow_state === 1 || inc.snow_state === 2 || inc.snow_state === 3;
}

export function getOpenIncidentDismissBlockReason(alert?: Alert | null): string | undefined {
  if (!hasOpenServiceNowIncident(alert)) {
    return undefined;
  }
  const incNum = alert?.servicenow_incident?.incident_number?.trim() || 'linked ticket';
  return `Cannot dismiss while ServiceNow incident ${incNum} is open`;
}

export function formatDurationSeconds(totalSeconds?: number | null): string {
  if (typeof totalSeconds !== 'number' || Number.isNaN(totalSeconds) || totalSeconds < 0) {
    return '—';
  }
  const secs = Math.floor(totalSeconds);
  if (secs < 60) {
    return `${secs}s`;
  }
  const mins = Math.floor(secs / 60);
  const remSecs = secs % 60;
  if (mins < 60) {
    return remSecs > 0 ? `${mins}m ${remSecs}s` : `${mins}m`;
  }
  const hrs = Math.floor(mins / 60);
  const remMins = mins % 60;
  if (hrs < 24) {
    return remMins > 0 ? `${hrs}h ${remMins}m` : `${hrs}h`;
  }
  const days = Math.floor(hrs / 24);
  const remHrs = hrs % 24;
  return remHrs > 0 ? `${days}d ${remHrs}h` : `${days}d`;
}

export function formatDurationBetween(startsAt?: string | null, endsAt?: string | null): string {
  if (!startsAt || !endsAt) {
    return '—';
  }
  const startMs = new Date(startsAt).getTime();
  const endMs = new Date(endsAt).getTime();
  if (Number.isNaN(startMs) || Number.isNaN(endMs)) {
    return '—';
  }
  return formatDurationSeconds(Math.max(0, Math.floor((endMs - startMs) / 1000)));
}

export function highestSeverity(alerts: Alert[]): string {
  let best = 'unknown';
  let bestRank = 99;
  for (const alert of alerts) {
    const sev = (alert.severity || 'unknown').toLowerCase();
    const rank = SEVERITY_RANK[sev] ?? 4;
    if (rank < bestRank) {
      bestRank = rank;
      best = sev;
    }
  }
  return best;
}

export function buildBulkShortDescription(alerts: Alert[]): string {
  if (alerts.length <= 1) {
    return alerts[0] ? buildDefaultShortDescription(alerts[0]) : '';
  }
  const sev = highestSeverity(alerts).toUpperCase();
  const names = alerts.map((a) => a.alert_name).join(', ');
  const raw = `[${sev}] ${alerts.length} alerts: ${names}`;
  return raw.length <= 160 ? raw : `${raw.slice(0, 157)}...`;
}

export function buildBulkDescription(alerts: Alert[]): string {
  if (alerts.length <= 1) {
    return alerts[0] ? buildDefaultDescription(alerts[0]) : '';
  }
  const origin = typeof window !== 'undefined' && window.location?.origin ? window.location.origin : '';
  const blocks = alerts.map((alert, idx) => {
    const env = getAlertEnvironment(alert) || '—';
    const site = getAlertSite(alert) || '—';
    const { team } = getAlertTeam(alert);
    const lines = [
      `${idx + 1}. ${alert.alert_name} [${alert.severity}]`,
      `   Environment: ${env} | Site: ${site} | Team: ${team || 'Untagged'}`,
      `   Starts At (UTC): ${formatUtcTimestamp(alert.starts_at)}`,
      `   Grafana Alert Link: ${origin}/a/${PLUGIN_ID}/alerts/${alert.id}`,
    ];
    if (alert.annotations?.summary) {
      lines.push(`   Summary: ${alert.annotations.summary}`);
    }
    return lines.join('\n');
  });

  return [`Combined Incident (${alerts.length} alerts selected)`, '', ...blocks].join('\n');
}

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
