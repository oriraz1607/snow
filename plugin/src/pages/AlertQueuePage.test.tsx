import React from 'react';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { AppPluginMeta } from '@grafana/data';
import { config } from '@grafana/runtime';
import { AlertQueuePage } from './AlertQueuePage';
import { ConfigPage } from './ConfigPage';
import { alertApi, ApiClientError } from '../api/client';
import {
  Alert,
  AlertDetail,
  ListAlertsResponse,
  RBAC_ACTIONS,
  ServiceNowMetadataResponse,
} from '../types/api';

jest.mock('@grafana/runtime', () => {
  const actual = jest.requireActual('@grafana/runtime');
  return {
    ...actual,
    config: {
      bootData: {
        user: {
          login: 'alice.smith',
          email: 'alice.smith@corp.example',
          name: 'Alice Smith',
          orgRole: 'Editor',
          permissions: {},
        },
      },
    },
    getBackendSrv: jest.fn(() => ({
      fetch: jest.fn(() => ({
        subscribe: jest.fn(() => ({ unsubscribe: jest.fn() })),
      })),
    })),
  };
});

jest.mock('../api/client', () => {
  const actual = jest.requireActual('../api/client');
  return {
    ...actual,
    alertApi: {
      listAlerts: jest.fn(),
      getAlert: jest.fn(),
      getAlertHistory: jest.fn(),
      acknowledgeAlert: jest.fn(),
      unacknowledgeAlert: jest.fn(),
      dismissAlert: jest.fn(),
      reopenAlert: jest.fn(),
      addNote: jest.fn(),
      tagAlertTeam: jest.fn(),
      getServiceNowMetadata: jest.fn(),
      createIncident: jest.fn(),
      createBulkIncident: jest.fn(),
      linkIncident: jest.fn(),
      syncIncident: jest.fn(),
      postWorkNote: jest.fn(),
      savePluginSettings: jest.fn(),
      checkPluginHealth: jest.fn(),
    },
  };
});

const mockedApi = alertApi as jest.Mocked<typeof alertApi>;

function setMockGrafanaUser(orgRole: string, permissions: Record<string, boolean> = {}) {
  (config as unknown as { bootData: { user: Record<string, unknown> } }).bootData.user = {
    login: `${orgRole.toLowerCase()}.user`,
    email: `${orgRole.toLowerCase()}@corp.example`,
    name: `${orgRole} User`,
    orgRole,
    permissions,
  };
}

function makeAlert(overrides: Partial<Alert> = {}): Alert {
  return {
    id: '11111111-1111-4111-8111-111111111111',
    fingerprint: 'fp-db-lag',
    starts_at: '2026-11-25T16:10:00Z',
    ends_at: null,
    state: 'firing',
    version: 1,
    alert_name: 'HighDatabaseReplicationLag',
    severity: 'critical',
    labels: {
      alertname: 'HighDatabaseReplicationLag',
      severity: 'critical',
      environment: 'production',
      site: 'eu-west-1',
    },
    annotations: {
      summary: 'Replication lag > 30s on replica-2',
    },
    environment: 'production',
    site: 'eu-west-1',
    team: '',
    team_source: 'none',
    generator_url: 'https://prom.example/graph',
    acknowledged_by: null,
    acknowledged_by_email: null,
    acknowledged_at: null,
    dismissed_by: null,
    dismissed_at: null,
    dismiss_reason: null,
    resolved_at: null,
    previous_episode_count: 1,
    incident_drift_status: 'none',
    servicenow_incident: null,
    ...overrides,
  };
}

function makeDetail(alert: Alert): AlertDetail {
  return {
    ...alert,
    group_key: 'gk',
    receiver: 'alert-api',
    delivery_count: 1,
    first_received_at: alert.starts_at,
    last_received_at: alert.starts_at,
    recent_audit_events: [],
  };
}

function makeMetadata(): ServiceNowMetadataResponse {
  return {
    default_assignment_group: 'Infrastructure',
    default_category: 'Infrastructure',
    categories: ['Infrastructure', 'Database'],
    default_caller_mode: 'sso_user',
    default_caller_username: 'noc',
    assignment_groups: [
      { sys_id: 'grp_1', name: 'Infrastructure' },
      { sys_id: 'grp_2', name: 'Database SRE' },
    ],
    snow_reachable: true,
  };
}

function makeQueueResponse(items: Alert[]): ListAlertsResponse {
  return {
    summary: {
      firing: items.filter((a) => a.state === 'firing').length,
      acknowledged: items.filter((a) => a.state === 'acknowledged').length,
      acknowledged_without_incident: items.filter(
        (a) => a.state === 'acknowledged' && !a.servicenow_incident
      ).length,
      non_active_acknowledged: items.filter(
        (a) => a.state === 'resolved' && Boolean(a.acknowledged_at)
      ).length,
      dismissed: items.filter((a) => a.state === 'dismissed').length,
      resolved_open_ticket: items.filter((a) => a.incident_drift_status === 'resolved_open_ticket')
        .length,
    },
    items,
  };
}

describe('AlertQueuePage triage, RBAC, bulk ticket creation, team tagging, and modal resilience', () => {
  beforeEach(() => {
    jest.clearAllMocks();
    mockedApi.listAlerts.mockReset();
    mockedApi.acknowledgeAlert.mockReset();
    mockedApi.createIncident.mockReset();
    mockedApi.createBulkIncident.mockReset();
    mockedApi.tagAlertTeam.mockReset();
    mockedApi.getAlert.mockReset();
    mockedApi.getAlertHistory.mockReset();
    setMockGrafanaUser('Editor');
    mockedApi.getServiceNowMetadata.mockResolvedValue(makeMetadata());
  });

  it('renders the 4 KPI tabs and Environment, Site, Severity, and Team table columns', async () => {
    const alert = makeAlert({
      team: 'observability',
      team_source: 'label',
      labels: {
        alertname: 'HighDatabaseReplicationLag',
        severity: 'critical',
        environment: 'production',
        site: 'eu-west-1',
        team: 'observability',
      },
    });
    mockedApi.listAlerts.mockResolvedValueOnce(makeQueueResponse([alert]));

    render(
      <AlertQueuePage
        pluginSettings={{ pollIntervalSeconds: 60 }}
        selectedAlertId={null}
        onSelectAlertId={jest.fn()}
      />
    );

    await waitFor(() => expect(screen.getByText('HighDatabaseReplicationLag')).toBeInTheDocument());

    expect(screen.getByTestId('kpi-card-firing')).toBeInTheDocument();
    expect(screen.getByTestId('kpi-card-acknowledged')).toBeInTheDocument();
    expect(screen.getByTestId('kpi-card-non_active_acknowledged')).toBeInTheDocument();
    expect(screen.getByTestId('kpi-card-dismissed')).toBeInTheDocument();

    expect(screen.getByRole('columnheader', { name: 'Environment' })).toBeInTheDocument();
    expect(screen.getByRole('columnheader', { name: 'Site' })).toBeInTheDocument();
    expect(screen.getByRole('columnheader', { name: 'Severity' })).toBeInTheDocument();
    expect(screen.getByRole('columnheader', { name: 'Team' })).toBeInTheDocument();

    const row = screen.getByTestId(`alert-row-${alert.id}`);
    expect(row).toHaveTextContent('production');
    expect(row).toHaveTextContent('eu-west-1');
    expect(row).toHaveTextContent('observability');
  });

  it('displays 409 Conflict toast with latest actor details and refreshes the queue', async () => {
    const user = userEvent.setup();
    const firingAlert = makeAlert({ state: 'firing', version: 1 });
    const ackedByBob = makeAlert({
      state: 'acknowledged',
      version: 2,
      acknowledged_by: 'bob.jones',
      acknowledged_at: '2026-11-25T16:12:00Z',
    });

    mockedApi.listAlerts
      .mockResolvedValueOnce(makeQueueResponse([firingAlert]))
      .mockResolvedValueOnce(makeQueueResponse([ackedByBob]));

    mockedApi.acknowledgeAlert.mockRejectedValueOnce(
      new ApiClientError(409, 'CONFLICT_STALE_VERSION', 'Stale version', {
        current_version: 2,
        current_state: 'acknowledged',
        acknowledged_by: 'bob.jones',
      })
    );

    render(
      <AlertQueuePage
        pluginSettings={{ pollIntervalSeconds: 60 }}
        selectedAlertId={null}
        onSelectAlertId={jest.fn()}
      />
    );

    await waitFor(() => expect(screen.getByText('HighDatabaseReplicationLag')).toBeInTheDocument());

    await user.click(screen.getByRole('button', { name: 'Acknowledge' }));

    await waitFor(() => expect(screen.getByTestId('conflict-toast')).toBeInTheDocument());
    expect(screen.getByTestId('conflict-toast')).toHaveTextContent('bob.jones');
    await waitFor(() => expect(mockedApi.listAlerts).toHaveBeenCalledTimes(2));
  });

  it('reveals Action Bar after Acknowledge and shows fallback_noc warning banner on creation', async () => {
    const user = userEvent.setup();
    const firingAlert = makeAlert({ state: 'firing', version: 1 });
    const ackedAlert = makeAlert({
      state: 'acknowledged',
      version: 2,
      acknowledged_by: 'editor.user',
      acknowledged_at: '2026-11-25T16:12:00Z',
    });
    const linkedAlert = makeAlert({
      ...ackedAlert,
      servicenow_incident: {
        id: 'inc-rec-1',
        sync_status: 'created',
        snow_sys_id: 'snow_123',
        incident_number: 'INC0000001',
        incident_url: 'http://localhost:8085/nav_to.do?uri=incident.do?sys_id=snow_123',
        snow_state: 1,
        snow_state_label: 'New',
        priority: 1,
        assignment_group: 'Infrastructure',
        caller_mode: 'sso_user',
        caller_resolution_status: 'fallback_noc',
        caller_fallback_reason: 'User not found in ServiceNow; fell back to noc.',
        snow_state_synced_at: '2026-11-25T16:13:00Z',
      },
    });

    mockedApi.listAlerts
      .mockResolvedValueOnce(makeQueueResponse([firingAlert]))
      .mockResolvedValueOnce(makeQueueResponse([ackedAlert]))
      .mockResolvedValueOnce(makeQueueResponse([linkedAlert]));
    mockedApi.acknowledgeAlert.mockResolvedValueOnce(ackedAlert);
    mockedApi.createIncident.mockResolvedValueOnce({
      alert_id: ackedAlert.id,
      servicenow_incident: linkedAlert.servicenow_incident!,
      warnings: ['Caller fell back to generic NOC user because editor@corp.example was not found.'],
    });

    render(
      <AlertQueuePage
        pluginSettings={{ pollIntervalSeconds: 60 }}
        selectedAlertId={null}
        onSelectAlertId={jest.fn()}
      />
    );

    await waitFor(() => expect(screen.getByRole('button', { name: 'Acknowledge' })).toBeInTheDocument());
    await user.click(screen.getByRole('button', { name: 'Acknowledge' }));

    await waitFor(() =>
      expect(screen.getByTestId(`action-bar-${firingAlert.id}`)).toBeInTheDocument()
    );
    await user.click(screen.getByRole('button', { name: 'Create ServiceNow Incident' }));

    await waitFor(() => expect(screen.getByLabelText('Short Description')).toBeInTheDocument());
    await user.click(
      within(screen.getByRole('dialog')).getByRole('button', { name: 'Create ServiceNow Incident' })
    );

    await waitFor(() => expect(screen.getByTestId('fallback-warning-banner')).toBeInTheDocument());
    expect(screen.getByTestId('fallback-warning-banner')).toHaveTextContent('INC0000001');
  });

  it('creates one ServiceNow ticket for multiple selected firing and acknowledged alerts', async () => {
    const user = userEvent.setup();
    const alertA = makeAlert({
      id: '11111111-1111-4111-8111-111111111111',
      alert_name: 'HighReplicationLag',
      state: 'firing',
      team: 'observability',
      team_source: 'label',
    });
    const alertB = makeAlert({
      id: '22222222-2222-4222-8222-222222222222',
      alert_name: 'ApiLatencySpike',
      state: 'acknowledged',
      acknowledged_by: 'editor.user',
      acknowledged_at: '2026-11-25T16:12:00Z',
    });

    mockedApi.listAlerts.mockResolvedValue(makeQueueResponse([alertA, alertB]));
    mockedApi.createBulkIncident.mockResolvedValueOnce({
      alert_ids: [alertA.id, alertB.id],
      servicenow_incident: {
        id: 'inc-bulk-1',
        sync_status: 'created',
        snow_sys_id: 'snow_bulk_1',
        incident_number: 'INC0000099',
        incident_url: 'http://mocksnow:8085/nav_to.do?uri=incident.do?sys_id=snow_bulk_1',
        snow_state: 1,
        snow_state_label: 'New',
        priority: 1,
        assignment_group: 'observability',
        caller_mode: 'sso_user',
        caller_resolution_status: 'sso_matched',
        snow_state_synced_at: '2026-11-25T16:15:00Z',
      },
    });

    render(
      <AlertQueuePage
        pluginSettings={{ pollIntervalSeconds: 60 }}
        selectedAlertId={null}
        onSelectAlertId={jest.fn()}
      />
    );

    await waitFor(() => expect(screen.getByLabelText('Select HighReplicationLag')).toBeInTheDocument());

    await user.click(screen.getByLabelText('Select HighReplicationLag'));
    await user.click(screen.getByLabelText('Select ApiLatencySpike'));

    const bulkBar = screen.getByTestId('bulk-selection-bar');
    expect(bulkBar).toHaveTextContent('2 alerts selected');
    expect(bulkBar).toHaveTextContent('1 firing alert will be auto-acknowledged');

    await user.click(
      within(bulkBar).getByRole('button', { name: 'Create Single ServiceNow Incident (2)' })
    );

    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByTestId('selected-alerts-summary')).toHaveTextContent('Included Alerts (2)');
    await user.click(within(dialog).getByRole('button', { name: 'Create Single Incident (2)' }));

    await waitFor(() => expect(mockedApi.createBulkIncident).toHaveBeenCalledTimes(1));
    expect(mockedApi.createBulkIncident.mock.calls[0][0].alert_ids).toEqual([alertA.id, alertB.id]);
  });

  it('allows Editor to tag missing team in AlertDetailDrawer while disabling tagging for Viewer', async () => {
    const user = userEvent.setup();
    const untagged = makeAlert({
      id: '33333333-3333-4333-8333-333333333333',
      alert_name: 'UntaggedServiceAlert',
      team: '',
      team_source: 'none',
    });
    const tagged = makeAlert({
      ...untagged,
      team: 'observability',
      team_source: 'operator',
      assigned_team: 'observability',
      assigned_team_by: 'editor.user',
    });

    mockedApi.listAlerts.mockResolvedValue(makeQueueResponse([untagged]));
    mockedApi.getAlert
      .mockResolvedValueOnce(makeDetail(untagged))
      .mockResolvedValueOnce(makeDetail(tagged));
    mockedApi.getAlertHistory.mockResolvedValue({
      alert_id: untagged.id,
      fingerprint: untagged.fingerprint,
      audit_events: [],
      previous_episodes: [],
    });
    mockedApi.tagAlertTeam.mockResolvedValueOnce(tagged);

    const { unmount } = render(
      <AlertQueuePage
        pluginSettings={{ pollIntervalSeconds: 60 }}
        selectedAlertId={untagged.id}
        onSelectAlertId={jest.fn()}
      />
    );

    const teamSection = await screen.findByTestId('drawer-team-section');
    expect(teamSection).toHaveTextContent('No team connected');

    await user.type(within(teamSection).getByLabelText('Tag team'), 'observability');
    await user.click(within(teamSection).getByRole('button', { name: 'Tag Team' }));

    await waitFor(() =>
      expect(mockedApi.tagAlertTeam).toHaveBeenCalledWith(untagged.id, { team: 'observability' })
    );
    unmount();

    // Viewer sees tagging control disabled
    setMockGrafanaUser('Viewer', { [RBAC_ACTIONS.ALERTS_READ]: true });
    mockedApi.getAlert.mockResolvedValueOnce(makeDetail(untagged));
    render(
      <AlertQueuePage
        pluginSettings={{ pollIntervalSeconds: 60 }}
        selectedAlertId={untagged.id}
        onSelectAlertId={jest.fn()}
      />
    );

    const viewerTeamSection = await screen.findByTestId('drawer-team-section');
    expect(within(viewerTeamSection).getByLabelText('Tag team')).toBeDisabled();
    expect(within(viewerTeamSection).getByRole('button', { name: 'Tag Team' })).toBeDisabled();
  });

  it('preserves modal inputs across background polling and reuses Idempotency-Key on 503 retry', async () => {
    const user = userEvent.setup();
    const ackedAlert = makeAlert({
      state: 'acknowledged',
      version: 2,
      acknowledged_by: 'editor.user',
      acknowledged_at: '2026-11-25T16:12:00Z',
    });

    mockedApi.listAlerts.mockResolvedValue(makeQueueResponse([ackedAlert]));
    mockedApi.createIncident
      .mockRejectedValueOnce(new ApiClientError(503, 'SNOW_UNAVAILABLE', 'ServiceNow down'))
      .mockResolvedValueOnce({
        alert_id: ackedAlert.id,
        servicenow_incident: {
          id: 'inc-rec-2',
          sync_status: 'created',
          snow_sys_id: 'snow_456',
          incident_number: 'INC0000002',
          incident_url: null,
          snow_state: 1,
          snow_state_label: 'New',
          priority: 1,
          assignment_group: 'Infrastructure',
          caller_mode: 'sso_user',
          caller_resolution_status: 'sso_matched',
          snow_state_synced_at: '2026-11-25T16:13:00Z',
        },
      });

    render(
      <AlertQueuePage
        pluginSettings={{ pollIntervalSeconds: 60 }}
        selectedAlertId={null}
        onSelectAlertId={jest.fn()}
      />
    );

    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Create ServiceNow Incident' })).toBeInTheDocument()
    );
    await user.click(screen.getByRole('button', { name: 'Create ServiceNow Incident' }));

    const shortDescInput = screen.getByLabelText('Short Description');
    await user.clear(shortDescInput);
    await user.type(shortDescInput, 'Custom operator incident title');

    // Trigger manual background refresh while modal is open
    await user.click(screen.getByRole('button', { name: 'Refresh' }));
    expect(screen.getByLabelText('Short Description')).toHaveValue('Custom operator incident title');

    // Submit -> fails with 503
    await user.click(
      within(screen.getByRole('dialog')).getByRole('button', { name: 'Create ServiceNow Incident' })
    );
    await waitFor(() => expect(screen.getByRole('button', { name: 'Retry Creation' })).toBeInTheDocument());
    expect(screen.getByLabelText('Short Description')).toHaveValue('Custom operator incident title');

    // Retry -> must send identical Idempotency-Key
    await user.click(screen.getByRole('button', { name: 'Retry Creation' }));
    await waitFor(() => expect(mockedApi.createIncident).toHaveBeenCalledTimes(2));

    const firstIdemKey = mockedApi.createIncident.mock.calls[0][2];
    const secondIdemKey = mockedApi.createIncident.mock.calls[1][2];
    expect(firstIdemKey).toBeTruthy();
    expect(secondIdemKey).toBe(firstIdemKey);
  });

  it('disables triage and incident controls for Viewer and blocks ConfigPage for non-Admins', async () => {
    setMockGrafanaUser('Viewer', { [RBAC_ACTIONS.ALERTS_READ]: true });
    const firingAlert = makeAlert({ state: 'firing', version: 1 });
    mockedApi.listAlerts.mockResolvedValue(makeQueueResponse([firingAlert]));

    const { unmount } = render(
      <AlertQueuePage
        pluginSettings={{ pollIntervalSeconds: 60 }}
        selectedAlertId={null}
        onSelectAlertId={jest.fn()}
      />
    );

    await waitFor(() => expect(screen.getByRole('button', { name: 'Acknowledge' })).toBeInTheDocument());
    expect(screen.getByRole('button', { name: 'Acknowledge' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Dismiss' })).toBeDisabled();
    unmount();

    const fakeMeta = {
      id: 'noc-alert-manager-app',
      name: 'Alert Manager',
      enabled: true,
      jsonData: { apiBaseUrl: 'http://api:8080', pollIntervalSeconds: 15 },
      secureJsonFields: { apiJwtSecret: true },
    } as unknown as AppPluginMeta;

    const { rerender } = render(<ConfigPage meta={fakeMeta} />);
    expect(screen.getByTestId('config-access-denied')).toBeInTheDocument();

    setMockGrafanaUser('Admin');
    rerender(<ConfigPage meta={fakeMeta} />);
    expect(screen.queryByTestId('config-access-denied')).not.toBeInTheDocument();
    expect(screen.getByLabelText('Go API Base URL')).toHaveValue('http://api:8080');
  });
});
