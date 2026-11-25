import { getBackendSrv } from '@grafana/runtime';
import {
  AddNoteRequest,
  Alert,
  AlertDetail,
  AlertHistoryResponse,
  ApiErrorEnvelope,
  AppPluginSettings,
  AuditEvent,
  CreateBulkIncidentRequest,
  CreateBulkIncidentResponse,
  CreateIncidentRequest,
  CreateIncidentResponse,
  DismissRequest,
  IncidentSyncResponse,
  LinkIncidentRequest,
  ListAlertsQuery,
  ListAlertsResponse,
  PLUGIN_ID,
  ServiceNowMetadataResponse,
  StateMutationRequest,
  TagTeamRequest,
  WorkNoteRequest,
  WorkNoteResponse,
} from '../types/api';

const RESOURCE_BASE = `/api/plugins/${PLUGIN_ID}/resources/v1`;

export class ApiClientError extends Error {
  readonly status: number;
  readonly code: string;
  readonly details?: Record<string, unknown>;
  readonly requestId?: string;

  constructor(
    status: number,
    code: string,
    message: string,
    details?: Record<string, unknown>,
    requestId?: string
  ) {
    super(message);
    this.name = 'ApiClientError';
    this.status = status;
    this.code = code;
    this.details = details;
    this.requestId = requestId;
  }
}

function parseError(err: unknown): ApiClientError {
  if (err instanceof ApiClientError) {
    return err;
  }
  const obj = err as {
    status?: number;
    statusText?: string;
    message?: string;
    data?: ApiErrorEnvelope | { message?: string } | string;
  };
  const status = typeof obj?.status === 'number' ? obj.status : 503;

  if (obj?.data && typeof obj.data === 'object') {
    if ('error' in obj.data && obj.data.error) {
      const e = obj.data.error;
      return new ApiClientError(
        status,
        e.code || 'API_ERROR',
        e.message || `Request failed with status ${status}`,
        e.details,
        e.request_id
      );
    }
    if ('message' in obj.data && typeof obj.data.message === 'string') {
      return new ApiClientError(status, 'API_ERROR', obj.data.message);
    }
  }

  const msg =
    typeof obj?.data === 'string' && obj.data.trim()
      ? obj.data
      : obj?.message || obj?.statusText || 'Alert Manager API request failed.';
  return new ApiClientError(status, status === 503 ? 'API_UNAVAILABLE' : 'API_ERROR', msg);
}

interface RequestOptions {
  method?: 'GET' | 'POST';
  data?: unknown;
  headers?: Record<string, string>;
  signal?: AbortSignal;
}

function requestResource<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    if (opts.signal?.aborted) {
      reject(new DOMException('Request aborted', 'AbortError'));
      return;
    }

    const observable = getBackendSrv().fetch<T>({
      url: `${RESOURCE_BASE}${path}`,
      method: opts.method ?? 'GET',
      data: opts.data,
      headers: {
        Accept: 'application/json',
        'Content-Type': 'application/json',
        ...opts.headers,
      },
      showErrorAlert: false,
      showSuccessAlert: false,
    });

    let settled = false;
    const subscription = observable.subscribe({
      next: (resp) => {
        if (!settled) {
          settled = true;
          cleanup();
          resolve(resp.data);
        }
      },
      error: (err) => {
        if (!settled) {
          settled = true;
          cleanup();
          reject(parseError(err));
        }
      },
    });

    const onAbort = () => {
      if (!settled) {
        settled = true;
        cleanup();
        subscription.unsubscribe();
        reject(new DOMException('Request aborted', 'AbortError'));
      }
    };

    const cleanup = () => {
      if (opts.signal) {
        opts.signal.removeEventListener('abort', onAbort);
      }
    };

    if (opts.signal) {
      opts.signal.addEventListener('abort', onAbort, { once: true });
    }
  });
}

export function buildAlertsQueryString(query: ListAlertsQuery): string {
  const params = new URLSearchParams();
  if (query.states && query.states.length > 0) {
    params.set('state', query.states.join(','));
  }
  if (query.severities && query.severities.length > 0) {
    params.set('severity', query.severities.join(','));
  }
  if (query.incident_drift && query.incident_drift !== 'all') {
    params.set('incident_drift', query.incident_drift);
  }
  if (typeof query.has_incident === 'boolean') {
    params.set('has_incident', String(query.has_incident));
  }
  if (query.fingerprint) {
    params.set('fingerprint', query.fingerprint);
  }
  if (query.labels) {
    for (const pair of query.labels) {
      if (pair.trim()) {
        params.append('label', pair.trim());
      }
    }
  }
  if (query.search?.trim()) {
    params.set('search', query.search.trim());
  }
  if (query.sort) {
    params.set('sort', query.sort);
  }
  if (query.limit) {
    params.set('limit', String(query.limit));
  }
  if (query.cursor) {
    params.set('cursor', query.cursor);
  }
  const qs = params.toString();
  return qs ? `?${qs}` : '';
}

export const alertApi = {
  listAlerts(query: ListAlertsQuery = {}, signal?: AbortSignal): Promise<ListAlertsResponse> {
    return requestResource<ListAlertsResponse>(`/alerts${buildAlertsQueryString(query)}`, { signal });
  },

  getAlert(alertId: string, signal?: AbortSignal): Promise<AlertDetail> {
    return requestResource<AlertDetail>(`/alerts/${encodeURIComponent(alertId)}`, { signal });
  },

  getAlertHistory(alertId: string, signal?: AbortSignal): Promise<AlertHistoryResponse> {
    return requestResource<AlertHistoryResponse>(`/alerts/${encodeURIComponent(alertId)}/history`, { signal });
  },

  acknowledgeAlert(alertId: string, payload: StateMutationRequest): Promise<Alert> {
    return requestResource<Alert>(`/alerts/${encodeURIComponent(alertId)}/acknowledge`, {
      method: 'POST',
      data: payload,
    });
  },

  unacknowledgeAlert(alertId: string, payload: StateMutationRequest): Promise<Alert> {
    return requestResource<Alert>(`/alerts/${encodeURIComponent(alertId)}/unacknowledge`, {
      method: 'POST',
      data: payload,
    });
  },

  dismissAlert(alertId: string, payload: DismissRequest): Promise<Alert> {
    return requestResource<Alert>(`/alerts/${encodeURIComponent(alertId)}/dismiss`, {
      method: 'POST',
      data: payload,
    });
  },

  reopenAlert(alertId: string, payload: StateMutationRequest): Promise<Alert> {
    return requestResource<Alert>(`/alerts/${encodeURIComponent(alertId)}/reopen`, {
      method: 'POST',
      data: payload,
    });
  },

  addNote(alertId: string, payload: AddNoteRequest): Promise<AuditEvent> {
    return requestResource<AuditEvent>(`/alerts/${encodeURIComponent(alertId)}/notes`, {
      method: 'POST',
      data: payload,
    });
  },

  tagAlertTeam(alertId: string, payload: TagTeamRequest): Promise<Alert> {
    return requestResource<Alert>(`/alerts/${encodeURIComponent(alertId)}/team`, {
      method: 'POST',
      data: payload,
    });
  },

  getServiceNowMetadata(signal?: AbortSignal): Promise<ServiceNowMetadataResponse> {
    return requestResource<ServiceNowMetadataResponse>('/servicenow/metadata', { signal });
  },

  createIncident(
    alertId: string,
    payload: CreateIncidentRequest,
    idempotencyKey: string
  ): Promise<CreateIncidentResponse> {
    return requestResource<CreateIncidentResponse>(`/alerts/${encodeURIComponent(alertId)}/servicenow/incident`, {
      method: 'POST',
      data: payload,
      headers: {
        'Idempotency-Key': idempotencyKey,
      },
    });
  },

  createBulkIncident(
    payload: CreateBulkIncidentRequest,
    idempotencyKey: string
  ): Promise<CreateBulkIncidentResponse> {
    return requestResource<CreateBulkIncidentResponse>('/servicenow/incidents/bulk', {
      method: 'POST',
      data: payload,
      headers: {
        'Idempotency-Key': idempotencyKey,
      },
    });
  },

  linkIncident(alertId: string, payload: LinkIncidentRequest): Promise<IncidentSyncResponse> {
    return requestResource<IncidentSyncResponse>(`/alerts/${encodeURIComponent(alertId)}/servicenow/link`, {
      method: 'POST',
      data: payload,
    });
  },

  syncIncident(alertId: string): Promise<IncidentSyncResponse> {
    return requestResource<IncidentSyncResponse>(`/alerts/${encodeURIComponent(alertId)}/servicenow/sync`, {
      method: 'POST',
      data: {},
    });
  },

  postWorkNote(alertId: string, payload: WorkNoteRequest): Promise<WorkNoteResponse> {
    return requestResource<WorkNoteResponse>(`/alerts/${encodeURIComponent(alertId)}/servicenow/work-notes`, {
      method: 'POST',
      data: payload,
    });
  },

  async savePluginSettings(
    enabled: boolean,
    jsonData: AppPluginSettings,
    secureJsonData?: { apiJwtSecret?: string }
  ): Promise<void> {
    try {
      await new Promise<void>((resolve, reject) => {
        getBackendSrv()
          .fetch({
            url: `/api/plugins/${PLUGIN_ID}/settings`,
            method: 'POST',
            data: {
              enabled,
              jsonData,
              ...(secureJsonData ? { secureJsonData } : {}),
            },
            showErrorAlert: false,
            showSuccessAlert: false,
          })
          .subscribe({
            next: () => resolve(),
            error: (err) => reject(parseError(err)),
          });
      });
    } catch (err) {
      throw parseError(err);
    }
  },

  async checkPluginHealth(): Promise<{ status: string; message: string }> {
    try {
      return await new Promise<{ status: string; message: string }>((resolve, reject) => {
        getBackendSrv()
          .fetch<{ status?: string; message?: string }>({
            url: `/api/plugins/${PLUGIN_ID}/health`,
            method: 'GET',
            showErrorAlert: false,
            showSuccessAlert: false,
          })
          .subscribe({
            next: (resp) =>
              resolve({
                status: resp.data?.status ?? 'UNKNOWN',
                message: resp.data?.message ?? 'Health check completed.',
              }),
            error: (err) => reject(parseError(err)),
          });
      });
    } catch (err) {
      throw parseError(err);
    }
  },
};
