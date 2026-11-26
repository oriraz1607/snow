export type AlertState = 'firing' | 'acknowledged' | 'dismissed' | 'resolved';

export type AlertStateFilter = AlertState | 'non_active_acknowledged';

export type TeamSource = 'label' | 'operator' | 'none';

export type SnowSyncStatus = 'pending' | 'created' | 'linked' | 'failed';

export type SnowCallerMode = 'sso_user' | 'generic_noc';

export type SnowCallerResolutionStatus =
  | 'pending'
  | 'sso_matched'
  | 'generic_noc_selected'
  | 'fallback_noc';

export type IncidentDriftStatus = 'none' | 'resolved_open_ticket' | 'active_closed_ticket';

export type AlertSort =
  | 'starts_at_desc'
  | 'starts_at_asc'
  | 'severity_desc'
  | 'severity_asc'
  | 'alert_name_asc'
  | 'alert_name_desc'
  | 'instance_asc'
  | 'instance_desc'
  | 'resolved_at_desc';

export type AuditEventType =
  | 'alert_fired'
  | 'alert_updated'
  | 'alert_resolved'
  | 'alert_acknowledged'
  | 'alert_unacknowledged'
  | 'alert_dismissed'
  | 'alert_reopened'
  | 'alert_team_tagged'
  | 'operator_note_added'
  | 'snow_incident_requested'
  | 'snow_incident_created'
  | 'snow_incident_failed'
  | 'snow_incident_linked'
  | 'snow_caller_fallback'
  | 'snow_state_synced'
  | 'snow_work_note_posted'
  | 'snow_incident_reopened';

export type ActorType = 'operator' | 'alertmanager' | 'servicenow' | 'system';

export interface ServiceNowIncident {
  id: string;
  sync_status: SnowSyncStatus;
  snow_sys_id: string | null;
  incident_number: string | null;
  incident_url: string | null;
  snow_state: number | null;
  snow_state_label: string | null;
  priority: number | null;
  assignment_group: string | null;
  caller_mode: SnowCallerMode;
  caller_resolution_status: SnowCallerResolutionStatus;
  caller_sys_id?: string | null;
  caller_fallback_reason?: string | null;
  snow_opened_at?: string | null;
  snow_resolved_at?: string | null;
  snow_closed_at?: string | null;
  snow_state_synced_at: string | null;
  resolution_note_posted_at?: string | null;
  last_error?: string | null;
}

export interface Alert {
  id: string;
  fingerprint: string;
  starts_at: string;
  ends_at: string | null;
  state: AlertState;
  version: number;
  alert_name: string;
  instance?: string;
  severity: string;
  group_key?: string;
  receiver?: string;
  labels: Record<string, string>;
  annotations: Record<string, string>;
  environment?: string;
  site?: string;
  team?: string;
  team_source?: TeamSource;
  assigned_team?: string | null;
  assigned_team_by?: string | null;
  assigned_team_at?: string | null;
  generator_url: string | null;
  external_url?: string | null;
  acknowledged_by: string | null;
  acknowledged_by_email: string | null;
  acknowledged_at: string | null;
  dismissed_by: string | null;
  dismissed_by_email?: string | null;
  dismissed_at: string | null;
  dismiss_reason: string | null;
  resolved_at: string | null;
  first_received_at?: string;
  last_received_at?: string;
  delivery_count?: number;
  previous_episode_count: number;
  incident_drift_status: IncidentDriftStatus;
  servicenow_incident: ServiceNowIncident | null;
}

export interface AuditEvent {
  id: number;
  event_type: AuditEventType;
  actor_type: ActorType;
  actor_login: string;
  actor_email: string | null;
  actor_role?: string | null;
  previous_state: AlertState | null;
  new_state: AlertState | null;
  comment: string | null;
  metadata: Record<string, unknown>;
  created_at: string;
}

export interface AlertDetail extends Alert {
  group_key: string;
  receiver: string;
  delivery_count: number;
  first_received_at: string;
  last_received_at: string;
  recent_audit_events: AuditEvent[];
}

export interface PreviousEpisodeSummary {
  id: string;
  starts_at: string;
  ends_at?: string | null;
  resolved_at: string | null;
  duration_seconds?: number;
  state: AlertState;
  severity?: string;
  acknowledged_by?: string | null;
  acknowledged_at?: string | null;
  dismissed_by?: string | null;
  dismissed_at?: string | null;
  dismiss_reason?: string | null;
  incident_number: string | null;
  snow_state_label: string | null;
  incident_url?: string | null;
  assignment_group?: string | null;
}

export interface AlertHistoryResponse {
  alert_id: string;
  fingerprint: string;
  audit_events: AuditEvent[];
  previous_episodes: PreviousEpisodeSummary[];
  previous_episodes_total?: number;
  limit?: number;
  offset?: number;
  has_more?: boolean;
}

export interface SummaryCounts {
  firing: number;
  firing_critical?: number;
  firing_warning?: number;
  acknowledged: number;
  acknowledged_without_incident: number;
  non_active_acknowledged: number;
  dismissed: number;
  resolved?: number;
  resolved_open_ticket: number;
  active_closed_ticket?: number;
}

export interface AlertFilterOptions {
  environments: string[];
  sites: string[];
  teams: string[];
  instances?: string[];
}

export interface ListAlertsResponse {
  summary: SummaryCounts;
  filter_options?: AlertFilterOptions;
  filtered_total?: number;
  items: Alert[];
  next_cursor?: string | null;
}

export interface ListAlertsQuery {
  states?: AlertStateFilter[];
  severities?: string[];
  environments?: string[];
  sites?: string[];
  teams?: string[];
  instances?: string[];
  incident_drift?: 'all' | IncidentDriftStatus;
  has_incident?: boolean;
  fingerprint?: string;
  labels?: string[];
  search?: string;
  from?: string;
  to?: string;
  sort?: AlertSort;
  limit?: number;
  cursor?: string;
}

export interface TagTeamRequest {
  team: string;
}

export interface CreateBulkIncidentRequest {
  alert_ids: string[];
  short_description: string;
  description: string;
  impact: 1 | 2 | 3;
  urgency: 1 | 2 | 3;
  assignment_group: string;
  category: string;
  caller_mode: SnowCallerMode;
  work_notes: string;
}

export interface CreateBulkIncidentResponse {
  alert_ids: string[];
  servicenow_incident: ServiceNowIncident;
  warnings?: string[];
}

export interface StateMutationRequest {
  version: number;
  comment?: string;
}

export interface DismissRequest {
  version: number;
  reason: string;
}

export interface BulkDismissRequest {
  alert_ids: string[];
  reason: string;
}

export interface BulkDismissResponse {
  dismissed_count: number;
  alert_ids: string[];
}

export interface AddNoteRequest {
  comment: string;
}

export interface CreateIncidentRequest {
  short_description: string;
  description: string;
  impact: 1 | 2 | 3;
  urgency: 1 | 2 | 3;
  assignment_group: string;
  category: string;
  caller_mode: SnowCallerMode;
  work_notes: string;
}

export interface CreateIncidentResponse {
  alert_id: string;
  servicenow_incident: ServiceNowIncident;
  warnings?: string[];
}

export interface LinkIncidentRequest {
  incident_number: string;
  post_work_note: boolean;
}

export interface IncidentSyncResponse {
  alert_id: string;
  servicenow_incident: ServiceNowIncident;
  incident_drift_status: IncidentDriftStatus;
}

export interface WorkNoteRequest {
  work_notes: string;
}

export interface WorkNoteResponse {
  alert_id: string;
  audit_event: AuditEvent;
}

export interface AssignmentGroupInfo {
  sys_id: string;
  name: string;
}

export interface ServiceNowMetadataResponse {
  default_assignment_group: string;
  default_category: string;
  categories: string[];
  default_caller_mode: SnowCallerMode;
  default_caller_username: string;
  assignment_groups: AssignmentGroupInfo[];
  team_assignment_groups?: Record<string, string>;
  snow_reachable: boolean;
}

export interface ApiErrorBody {
  code: string;
  message: string;
  request_id?: string;
  details?: Record<string, unknown>;
}

export interface ApiErrorEnvelope {
  error: ApiErrorBody;
}

export interface AppPluginSettings {
  apiBaseUrl?: string;
  pollIntervalSeconds?: number;
  defaultCallerMode?: SnowCallerMode;
  genericNocDisplayLabel?: string;
}

export const PLUGIN_ID = 'noc-alert-manager-app';

export const RBAC_ACTIONS = {
  ALERTS_READ: 'noc-alert-manager-app.alerts:read',
  ALERTS_TRIAGE: 'noc-alert-manager-app.alerts:triage',
  INCIDENTS_WRITE: 'noc-alert-manager-app.incidents:write',
  SETTINGS_WRITE: 'noc-alert-manager-app.settings:write',
} as const;
