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
      unacknowledgeAlert: jest.fn(),
      dismissAlert: jest.fn(),
      bulkDismissAlerts: jest.fn(),
      reopenAlert: jest.fn(),
      addNote: jest.fn(),
      tagAlertTeam: jest.fn(),
      updateConfluenceUrl: jest.fn(),
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

function makeMetadata(overrides: Partial<ServiceNowMetadataResponse> = {}): ServiceNowMetadataResponse {
  return {
    default_assignment_group: 'Infrastructure',
    default_category: 'Infrastructure',
    categories: ['Infrastructure', 'Database'],
    default_caller_mode: 'sso_user',
    default_caller_username: 'noc',
    assignment_groups: [
      { sys_id: 'grp_1', name: 'Infrastructure' },
      { sys_id: 'grp_2', name: 'Database SRE' },
      { sys_id: 'grp_3', name: 'Platform Engineering' },
    ],
    team_assignment_groups: {},
    snow_reachable: true,
    ...overrides,
  };
}

function makeQueueResponse(
  items: Alert[],
  overrides: Partial<ListAlertsResponse> = {}
): ListAlertsResponse {
  const firingItems = items.filter((a) => a.state === 'firing');
  const envs = Array.from(
    new Set(items.map((a) => a.environment || a.labels?.environment || a.labels?.env || '').filter(Boolean))
  ).sort();
  const sites = Array.from(
    new Set(items.map((a) => a.site || a.labels?.site || '').filter(Boolean))
  ).sort();
  const teams = Array.from(
    new Set(items.map((a) => a.labels?.team || a.assigned_team || a.team || '').filter(Boolean))
  ).sort();
  const instances = Array.from(
    new Set(items.map((a) => a.instance || a.labels?.instance || a.labels?.host || '').filter(Boolean))
  ).sort();

  return {
    summary: {
      firing: firingItems.length,
      firing_critical: firingItems.filter((a) => a.severity.toLowerCase() === 'critical').length,
      firing_warning: firingItems.filter((a) => a.severity.toLowerCase() === 'warning').length,
      acknowledged: items.filter((a) => a.state === 'acknowledged').length,
      acknowledged_without_incident: items.filter(
        (a) => a.state === 'acknowledged' && !a.servicenow_incident
      ).length,
      non_active_acknowledged: items.filter(
        (a) => a.state === 'resolved' && Boolean(a.acknowledged_at) && !a.dismissed_at
      ).length,
      dismissed: items.filter((a) => a.state === 'dismissed').length,
      resolved: items.filter((a) => a.state === 'resolved').length,
      resolved_open_ticket: items.filter((a) => a.incident_drift_status === 'resolved_open_ticket')
        .length,
      active_closed_ticket: items.filter((a) => a.incident_drift_status === 'active_closed_ticket')
        .length,
    },
    filter_options: {
      environments: envs,
      sites,
      teams,
      instances,
    },
    filtered_total: items.length,
    items,
    ...overrides,
  };
}

describe('AlertQueuePage redesign, filters, bulk dismiss, RBAC, 3-squares actions, and pagination', () => {
  beforeEach(() => {
    jest.clearAllMocks();
    mockedApi.listAlerts.mockReset();
    mockedApi.dismissAlert.mockReset();
    mockedApi.bulkDismissAlerts.mockReset();
    mockedApi.createIncident.mockReset();
    mockedApi.createBulkIncident.mockReset();
    mockedApi.tagAlertTeam.mockReset();
    mockedApi.updateConfluenceUrl.mockReset();
    mockedApi.getAlert.mockReset();
    mockedApi.getAlertHistory.mockReset();
    setMockGrafanaUser('Editor');
    mockedApi.getServiceNowMetadata.mockResolvedValue(makeMetadata());
  });

  it('defaults to Firing-only sorted Critical-first, hides State/Owner/ServiceNow on Firing, and exposes Critical/Warning chips only', async () => {
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

    expect(mockedApi.listAlerts).toHaveBeenCalledWith(
      expect.objectContaining({
        states: ['firing'],
        sort: 'severity_desc',
        limit: 50,
      }),
      expect.any(AbortSignal)
    );

    expect(screen.getByTestId('kpi-card-firing')).toHaveTextContent('1 critical · 0 warning');
    expect(screen.getByTestId('kpi-card-acknowledged')).toBeInTheDocument();
    expect(screen.getByTestId('kpi-card-acknowledged')).not.toHaveTextContent('without ticket');
    expect(screen.getByTestId('kpi-card-non_active_acknowledged')).toBeInTheDocument();
    expect(screen.getByTestId('kpi-card-dismissed')).toBeInTheDocument();

    // Fixed columns without State, Owner, or ServiceNow on Firing
    expect(screen.getByRole('columnheader', { name: 'Severity' })).toBeInTheDocument();
    expect(screen.getByRole('columnheader', { name: 'Alert' })).toBeInTheDocument();
    expect(screen.getByRole('columnheader', { name: 'Instance' })).toBeInTheDocument();
    expect(screen.getByRole('columnheader', { name: 'Env' })).toBeInTheDocument();
    expect(screen.getByRole('columnheader', { name: 'Site' })).toBeInTheDocument();
    expect(screen.getByRole('columnheader', { name: 'Team' })).toBeInTheDocument();
    expect(screen.getByRole('columnheader', { name: 'Duration' })).toBeInTheDocument();
    expect(screen.getByRole('columnheader', { name: 'Actions' })).toBeInTheDocument();

    expect(screen.queryByRole('columnheader', { name: 'State' })).not.toBeInTheDocument();
    expect(screen.queryByRole('columnheader', { name: 'Owner' })).not.toBeInTheDocument();
    expect(screen.queryByRole('columnheader', { name: 'ServiceNow' })).not.toBeInTheDocument();

    // Severity chips restricted to critical + warning only
    expect(screen.getByRole('button', { name: 'critical' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'warning' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'info' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'unknown' })).not.toBeInTheDocument();

    // Env, Site, Team, Instance filters present; ServiceNow status dropdown removed
    expect(screen.getByLabelText('Filter by environment')).toBeInTheDocument();
    expect(screen.getByLabelText('Filter by site')).toBeInTheDocument();
    expect(screen.getByLabelText('Filter by team')).toBeInTheDocument();
    expect(screen.getByLabelText('Filter by instance')).toBeInTheDocument();
    expect(screen.queryByLabelText('ServiceNow status filter')).not.toBeInTheDocument();
    expect(
      screen.getByPlaceholderText('Search alert name, instance, team, summary, fingerprint, or INC...')
    ).toBeInTheDocument();

    const row = screen.getByTestId(`alert-row-${alert.id}`);
    expect(row).toHaveTextContent('production');
    expect(row).toHaveTextContent('eu-west-1');
    expect(row).toHaveTextContent('observability');
    expect(within(row).queryByText('v1')).not.toBeInTheDocument();
    expect(within(screen.getByTestId('env-quick-select')).getByRole('button', { name: 'production' })).toBeInTheDocument();
    expect(within(screen.getByTestId('site-quick-select')).getByRole('button', { name: 'eu-west-1' })).toBeInTheDocument();
  });

  it('exposes Acknowledge & Create/Link actions on firing rows (no standalone Acknowledge) and handles 409 conflict with queue refresh', async () => {
    const user = userEvent.setup();
    const firingAlert = makeAlert({ state: 'firing', version: 1 });
    const linkedByBob = makeAlert({
      state: 'acknowledged',
      version: 2,
      acknowledged_by: 'bob.jones',
      acknowledged_at: '2026-11-25T16:12:00Z',
    });

    mockedApi.listAlerts
      .mockResolvedValueOnce(makeQueueResponse([firingAlert]))
      .mockResolvedValueOnce(makeQueueResponse([linkedByBob]));

    mockedApi.createIncident.mockRejectedValueOnce(
      new ApiClientError(409, 'INCIDENT_ALREADY_LINKED', 'Alert already has a linked ServiceNow incident.', {
        alert_id: firingAlert.id,
        incident_number: 'INC0008888',
        current_state: 'acknowledged',
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

    await user.click(screen.getByTestId(`row-actions-trigger-${firingAlert.id}`));
    const menu = await screen.findByTestId(`row-actions-menu-${firingAlert.id}`);
    expect(within(menu).queryByRole('menuitem', { name: 'Acknowledge' })).not.toBeInTheDocument();
    expect(within(menu).getByRole('menuitem', { name: 'Acknowledge & Link Incident...' })).toBeEnabled();
    await user.click(within(menu).getByRole('menuitem', { name: 'Acknowledge & Create Incident' }));

    const dialog = await screen.findByRole('dialog');
    await user.click(within(dialog).getByRole('button', { name: 'Acknowledge & Create Incident' }));

    await waitFor(() =>
      expect(within(dialog).getByTestId('create-modal-conflict-error')).toBeInTheDocument()
    );
    expect(within(dialog).getByTestId('create-modal-conflict-error')).toHaveTextContent('INC0008888');
    await waitFor(() => expect(mockedApi.listAlerts).toHaveBeenCalledTimes(2));
  });

  it('shows ServiceNow column on Acknowledged tab only, creates incident directly from firing row via 3-squares menu, and surfaces fallback banner', async () => {
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
      .mockResolvedValueOnce(makeQueueResponse([linkedAlert]))
      .mockResolvedValueOnce(makeQueueResponse([linkedAlert]));
    mockedApi.createIncident.mockResolvedValueOnce({
      alert_id: firingAlert.id,
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

    await waitFor(() =>
      expect(screen.getByTestId(`row-actions-trigger-${firingAlert.id}`)).toBeInTheDocument()
    );
    expect(screen.queryByRole('columnheader', { name: 'ServiceNow' })).not.toBeInTheDocument();

    // Acknowledge & Create Incident directly on firing alert via 3-squares menu (uses single endpoint)
    await user.click(screen.getByTestId(`row-actions-trigger-${firingAlert.id}`));
    await user.click(
      within(screen.getByRole('menu')).getByRole('menuitem', { name: 'Acknowledge & Create Incident' })
    );

    await waitFor(() => expect(screen.getByLabelText('Short Description')).toBeInTheDocument());
    await user.click(
      within(screen.getByRole('dialog')).getByRole('button', { name: 'Acknowledge & Create Incident' })
    );

    await waitFor(() => expect(mockedApi.createIncident).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(screen.getByTestId('fallback-warning-banner')).toBeInTheDocument());
    expect(screen.getByTestId('fallback-warning-banner')).toHaveTextContent('INC0000001');

    // Click Acknowledged KPI tab -> ServiceNow column appears and inline action-bar row stays absent
    await user.click(screen.getByTestId('kpi-card-acknowledged'));
    await waitFor(() => expect(screen.getByRole('columnheader', { name: 'ServiceNow' })).toBeInTheDocument());
    expect(screen.queryByTestId(`action-bar-${linkedAlert.id}`)).not.toBeInTheDocument();
  });

  it('creates one ServiceNow ticket for multiple selected firing alerts', async () => {
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
      state: 'firing',
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
    expect(bulkBar).toHaveTextContent('2 firing alerts acknowledged on ticket creation');

    await user.click(
      within(bulkBar).getByRole('button', { name: 'Acknowledge & Create Single Incident (2)' })
    );

    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByTestId('selected-alerts-summary')).toHaveTextContent('Included Alerts (2)');
    await user.click(
      within(dialog).getByRole('button', { name: 'Acknowledge & Create Single Incident (2)' })
    );

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
    expect(within(teamSection).queryByText('Quick select:')).not.toBeInTheDocument();

    await user.type(within(teamSection).getByLabelText('Tag team'), 'observability');
    await user.click(within(teamSection).getByRole('button', { name: 'Tag Team' }));

    await waitFor(() =>
      expect(mockedApi.tagAlertTeam).toHaveBeenCalledWith(untagged.id, { team: 'observability' })
    );
    unmount();

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

  it('preserves modal inputs across background polling and reuses Idempotency-Key on 503 retry for a firing alert', async () => {
    const user = userEvent.setup();
    const firingAlert = makeAlert({
      state: 'firing',
      version: 1,
    });

    mockedApi.listAlerts.mockResolvedValue(makeQueueResponse([firingAlert]));
    mockedApi.createIncident
      .mockRejectedValueOnce(new ApiClientError(503, 'SNOW_UNAVAILABLE', 'ServiceNow down'))
      .mockResolvedValueOnce({
        alert_id: firingAlert.id,
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
      expect(screen.getByTestId(`row-actions-trigger-${firingAlert.id}`)).toBeInTheDocument()
    );
    await user.click(screen.getByTestId(`row-actions-trigger-${firingAlert.id}`));
    await user.click(
      within(screen.getByRole('menu')).getByRole('menuitem', { name: 'Acknowledge & Create Incident' })
    );

    const shortDescInput = screen.getByLabelText('Short Description');
    await user.clear(shortDescInput);
    await user.type(shortDescInput, 'Custom operator incident title');

    await user.click(screen.getByRole('button', { name: 'Refresh' }));
    expect(screen.getByLabelText('Short Description')).toHaveValue('Custom operator incident title');

    await user.click(
      within(screen.getByRole('dialog')).getByRole('button', { name: 'Acknowledge & Create Incident' })
    );
    await waitFor(() => expect(screen.getByRole('button', { name: 'Retry Creation' })).toBeInTheDocument());
    expect(screen.getByTestId('create-modal-error')).toHaveTextContent('remain firing');
    expect(screen.getByLabelText('Short Description')).toHaveValue('Custom operator incident title');

    await user.click(screen.getByRole('button', { name: 'Retry Creation' }));
    await waitFor(() => expect(mockedApi.createIncident).toHaveBeenCalledTimes(2));

    const firstIdemKey = mockedApi.createIncident.mock.calls[0][2];
    const secondIdemKey = mockedApi.createIncident.mock.calls[1][2];
    expect(firstIdemKey).toBeTruthy();
    expect(secondIdemKey).toBe(firstIdemKey);
  });

  it('shows Acknowledge & Create Incident and Link Existing Incident in AlertDetailDrawer for firing alerts without standalone Acknowledge', async () => {
    const user = userEvent.setup();
    const firingAlert = makeAlert({ state: 'firing', version: 1 });
    mockedApi.listAlerts.mockResolvedValue(makeQueueResponse([firingAlert]));
    mockedApi.getAlert.mockResolvedValue(makeDetail(firingAlert));
    mockedApi.getAlertHistory.mockResolvedValue({
      alert_id: firingAlert.id,
      fingerprint: firingAlert.fingerprint,
      audit_events: [],
      previous_episodes: [],
    });

    render(
      <AlertQueuePage
        pluginSettings={{ pollIntervalSeconds: 60 }}
        selectedAlertId={firingAlert.id}
        onSelectAlertId={jest.fn()}
      />
    );

    const drawerBar = await screen.findByTestId('drawer-action-bar');
    expect(within(drawerBar).queryByRole('button', { name: 'Acknowledge' })).not.toBeInTheDocument();
    expect(within(drawerBar).getByRole('button', { name: 'Acknowledge & Create Incident' })).toBeEnabled();
    expect(within(drawerBar).getByRole('button', { name: 'Link Existing Incident' })).toBeEnabled();

    await user.click(within(drawerBar).getByRole('button', { name: 'Link Existing Incident' }));
    expect(await screen.findByTestId('link-modal-ack-notice')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Acknowledge & Link Incident' })).toBeInTheDocument();
  });

  it('disables triage/incident controls in 3-squares menu for Viewer and blocks ConfigPage for non-Admins', async () => {
    const user = userEvent.setup();
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

    await waitFor(() =>
      expect(screen.getByTestId(`row-actions-trigger-${firingAlert.id}`)).toBeInTheDocument()
    );
    await user.click(screen.getByTestId(`row-actions-trigger-${firingAlert.id}`));
    const menu = await screen.findByRole('menu');
    expect(within(menu).queryByRole('menuitem', { name: 'Acknowledge' })).not.toBeInTheDocument();
    expect(within(menu).getByRole('menuitem', { name: 'Acknowledge & Create Incident' })).toBeDisabled();
    expect(within(menu).getByRole('menuitem', { name: 'Acknowledge & Link Incident...' })).toBeDisabled();
    expect(within(menu).getByRole('menuitem', { name: 'Dismiss...' })).toBeDisabled();
    expect(within(menu).getByRole('menuitem', { name: 'View Details' })).toBeEnabled();
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

  it('REV-05 hides ServiceNow on Dismissed tab and shows dismissed_by attribution in AlertDetailDrawer', async () => {
    const user = userEvent.setup();
    const dismissedAfterAck = makeAlert({
      id: 'aaaa1111-1111-4111-8111-111111111111',
      alert_name: 'DismissedAfterAckAlert',
      state: 'dismissed',
      acknowledged_by: 'alice.smith',
      acknowledged_at: '2026-11-25T16:12:00Z',
      dismissed_by: 'bob.jones',
      dismissed_at: '2026-11-25T16:15:00Z',
    });

    mockedApi.listAlerts.mockResolvedValue(makeQueueResponse([dismissedAfterAck]));
    mockedApi.getAlert.mockResolvedValueOnce(makeDetail(dismissedAfterAck));
    mockedApi.getAlertHistory.mockResolvedValueOnce({
      alert_id: dismissedAfterAck.id,
      fingerprint: dismissedAfterAck.fingerprint,
      audit_events: [],
      previous_episodes: [],
    });

    const Wrapper: React.FC = () => {
      const [selectedId, setSelectedId] = React.useState<string | null>(null);
      return (
        <AlertQueuePage
          pluginSettings={{ pollIntervalSeconds: 60 }}
          selectedAlertId={selectedId}
          onSelectAlertId={setSelectedId}
        />
      );
    };

    render(<Wrapper />);
    await waitFor(() => expect(screen.getByTestId('kpi-card-dismissed')).toBeInTheDocument());
    await user.click(screen.getByTestId('kpi-card-dismissed'));

    await waitFor(() => expect(screen.getByText('DismissedAfterAckAlert')).toBeInTheDocument());
    expect(screen.queryByRole('columnheader', { name: 'ServiceNow' })).not.toBeInTheDocument();
    expect(screen.queryByRole('columnheader', { name: 'Owner' })).not.toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'DismissedAfterAckAlert' }));
    const dismissedNote = await screen.findByTestId('drawer-dismissed-by');
    expect(dismissedNote).toHaveTextContent('Dismissed by bob.jones');
  });

  it('REV-10 enforces 50-alert bulk cap, handles modal 409 conflict, and supports keyset page navigation at 1,000 alerts', async () => {
    const user = userEvent.setup();
    const manyAlerts: Alert[] = Array.from({ length: 52 }, (_, i) =>
      makeAlert({
        id: `11111111-1111-4111-8111-${String(i + 1).padStart(12, '0')}`,
        alert_name: `BulkAlert${i + 1}`,
        state: 'firing',
      })
    );

    mockedApi.listAlerts
      .mockResolvedValueOnce(
        makeQueueResponse(manyAlerts, {
          filtered_total: 1000,
          next_cursor: 'cur_page_2',
          summary: {
            firing: 1000,
            firing_critical: 220,
            firing_warning: 780,
            acknowledged: 0,
            acknowledged_without_incident: 0,
            non_active_acknowledged: 0,
            dismissed: 0,
            resolved_open_ticket: 0,
          },
        })
      )
      .mockResolvedValueOnce(
        makeQueueResponse([manyAlerts[50]], {
          filtered_total: 1000,
          next_cursor: 'cur_page_3',
        })
      );

    const { unmount } = render(
      <AlertQueuePage
        pluginSettings={{ pollIntervalSeconds: 60 }}
        selectedAlertId={null}
        onSelectAlertId={jest.fn()}
      />
    );

    await waitFor(() => expect(screen.getByText('BulkAlert1')).toBeInTheDocument());

    // Firing card displays Critical/Warning breakdown for 1,000 alerts
    const firingCard = screen.getByTestId('kpi-card-firing');
    expect(firingCard).toHaveTextContent('1,000');
    expect(firingCard).toHaveTextContent('220 critical · 780 warning');

    // Pagination bar shows page 1 of 20
    const pager = screen.getByTestId('pagination-bar');
    expect(pager).toHaveTextContent('Showing 1–52 of 1,000 · Page 1 of 20');

    // Select all -> capped at 50
    await user.click(screen.getByLabelText('Select all eligible alerts'));
    const bulkBar = screen.getByTestId('bulk-selection-bar');
    expect(bulkBar).toHaveTextContent('50 alerts selected');
    expect(screen.getByTestId('bulk-selection-cap-notice')).toHaveTextContent('Maximum 50 alerts');
    expect(screen.getByLabelText('Select BulkAlert51')).toBeDisabled();
    expect(screen.getByLabelText('Select BulkAlert50')).toBeChecked();

    // Navigate to Page 2 using Next ›
    await user.click(within(pager).getByRole('button', { name: 'Next ›' }));
    await waitFor(() =>
      expect(mockedApi.listAlerts).toHaveBeenLastCalledWith(
        expect.objectContaining({ cursor: 'cur_page_2' }),
        expect.any(AbortSignal)
      )
    );
    unmount();

    mockedApi.listAlerts.mockReset();

    const firingA = makeAlert({
      id: 'aaaa0000-0000-4000-8000-000000000001',
      alert_name: 'FiringAlertA',
      state: 'firing',
      version: 1,
    });
    const ackedByServer = makeAlert({
      ...firingA,
      state: 'acknowledged',
      version: 2,
      acknowledged_by: 'editor.user',
      acknowledged_at: '2026-11-26T08:00:00Z',
    });

    mockedApi.listAlerts
      .mockResolvedValueOnce(makeQueueResponse([firingA]))
      .mockResolvedValueOnce(makeQueueResponse([ackedByServer]));
    mockedApi.createIncident.mockRejectedValueOnce(
      new ApiClientError(409, 'INCIDENT_ALREADY_LINKED', 'Alert already has a linked ServiceNow incident.', {
        alert_id: firingA.id,
        incident_number: 'INC0099001',
        current_state: 'acknowledged',
      })
    );

    render(
      <AlertQueuePage
        pluginSettings={{ pollIntervalSeconds: 60 }}
        selectedAlertId={null}
        onSelectAlertId={jest.fn()}
      />
    );

    await waitFor(() => expect(screen.getByLabelText('Select FiringAlertA')).toBeInTheDocument());
    await user.click(screen.getByLabelText('Select FiringAlertA'));
    await user.click(
      within(screen.getByTestId('bulk-selection-bar')).getByRole('button', {
        name: 'Acknowledge & Create Incident',
      })
    );

    const dialog = await screen.findByRole('dialog');
    await user.click(within(dialog).getByRole('button', { name: 'Acknowledge & Create Incident' }));

    await waitFor(() =>
      expect(within(dialog).getByTestId('create-modal-conflict-error')).toBeInTheDocument()
    );
    expect(within(dialog).getByTestId('create-modal-conflict-error')).toHaveTextContent('FiringAlertA');
    expect(within(dialog).getByTestId('create-modal-conflict-error')).toHaveTextContent('INC0099001');
    await waitFor(() => expect(mockedApi.listAlerts).toHaveBeenCalledTimes(2));
  });

  it('REV-14 renders 0 for non_active_acknowledged when count is 0 even if resolved_open_ticket is positive', async () => {
    mockedApi.listAlerts.mockResolvedValueOnce({
      summary: {
        firing: 0,
        firing_critical: 0,
        firing_warning: 0,
        acknowledged: 0,
        acknowledged_without_incident: 0,
        non_active_acknowledged: 0,
        dismissed: 0,
        resolved_open_ticket: 2,
      },
      filtered_total: 0,
      items: [],
    });

    render(
      <AlertQueuePage
        pluginSettings={{ pollIntervalSeconds: 60 }}
        selectedAlertId={null}
        onSelectAlertId={jest.fn()}
      />
    );

    await waitFor(() => expect(screen.getByTestId('kpi-card-non_active_acknowledged')).toBeInTheDocument());
    expect(screen.getByTestId('kpi-card-non_active_acknowledged')).toHaveTextContent('0');
  });

  it('filters by Env, Site, Team, searches by team, and bulk-dismisses selected alerts', async () => {
    const user = userEvent.setup();
    const alertA = makeAlert({
      id: '11111111-1111-4111-8111-111111111111',
      alert_name: 'HighReplicationLag',
      environment: 'prod',
      site: 'eu-west-1',
      team: 'core-db',
      team_source: 'label',
      labels: {
        alertname: 'HighReplicationLag',
        severity: 'critical',
        environment: 'prod',
        site: 'eu-west-1',
        team: 'core-db',
      },
    });
    const alertB = makeAlert({
      id: '22222222-2222-4222-8222-222222222222',
      alert_name: 'ApiLatencySpike',
      environment: 'staging',
      site: 'us-east-1',
      team: 'observability',
      team_source: 'operator',
      labels: {
        alertname: 'ApiLatencySpike',
        severity: 'warning',
        environment: 'staging',
        site: 'us-east-1',
      },
      assigned_team: 'observability',
    });

    mockedApi.listAlerts.mockResolvedValue(makeQueueResponse([alertA, alertB]));
    mockedApi.bulkDismissAlerts.mockResolvedValueOnce({
      dismissed_count: 2,
      alert_ids: [alertA.id, alertB.id],
    });

    render(
      <AlertQueuePage
        pluginSettings={{ pollIntervalSeconds: 60 }}
        selectedAlertId={null}
        onSelectAlertId={jest.fn()}
      />
    );

    await waitFor(() => expect(screen.getByText('HighReplicationLag')).toBeInTheDocument());

    // Filter by Env = staging quick-select chip
    await user.click(
      within(screen.getByLabelText('Filter by environment')).getByRole('button', { name: 'staging' })
    );
    await waitFor(() =>
      expect(mockedApi.listAlerts).toHaveBeenLastCalledWith(
        expect.objectContaining({ environments: ['staging'] }),
        expect.any(AbortSignal)
      )
    );

    // Filter by Site = eu-west-1 quick-select chip
    await user.click(
      within(screen.getByLabelText('Filter by site')).getByRole('button', { name: 'eu-west-1' })
    );
    await waitFor(() =>
      expect(mockedApi.listAlerts).toHaveBeenLastCalledWith(
        expect.objectContaining({ environments: ['staging'], sites: ['eu-west-1'] }),
        expect.any(AbortSignal)
      )
    );

    // Filter by Team = core-db
    await user.click(screen.getByLabelText('Filter by team'));
    await user.click(await screen.findByText('Team: core-db'));
    await waitFor(() =>
      expect(mockedApi.listAlerts).toHaveBeenLastCalledWith(
        expect.objectContaining({ teams: ['core-db'] }),
        expect.any(AbortSignal)
      )
    );

    // Select both alerts and bulk-dismiss them together
    await user.click(screen.getByLabelText('Select HighReplicationLag'));
    await user.click(screen.getByLabelText('Select ApiLatencySpike'));

    const bulkBar = screen.getByTestId('bulk-selection-bar');
    await user.click(within(bulkBar).getByRole('button', { name: 'Dismiss Selected (2)' }));

    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByTestId('dismiss-alerts-summary')).toHaveTextContent('Selected Alerts (2)');
    await user.type(within(dialog).getByLabelText('Dismiss Reason'), 'Planned deployment noise');
    await user.click(within(dialog).getByRole('button', { name: 'Dismiss 2 Alerts' }));

    await waitFor(() =>
      expect(mockedApi.bulkDismissAlerts).toHaveBeenCalledWith({
        alert_ids: [alertA.id, alertB.id],
        reason: 'Planned deployment noise',
      })
    );
  });

  it('displays red active_closed_ticket warning badge on Acknowledged card while on default Firing tab', async () => {
    const firingAlert = makeAlert({ state: 'firing' });
    mockedApi.listAlerts.mockResolvedValueOnce(
      makeQueueResponse([firingAlert], {
        summary: {
          firing: 1,
          firing_critical: 1,
          firing_warning: 0,
          acknowledged: 2,
          acknowledged_without_incident: 0,
          non_active_acknowledged: 0,
          dismissed: 0,
          resolved: 0,
          resolved_open_ticket: 0,
          active_closed_ticket: 2,
        },
      })
    );

    render(
      <AlertQueuePage
        pluginSettings={{ pollIntervalSeconds: 60 }}
        selectedAlertId={null}
        onSelectAlertId={jest.fn()}
      />
    );

    await waitFor(() => expect(screen.getByTestId('kpi-card-firing')).toBeInTheDocument());
    const badge = screen.getByTestId('kpi-active-closed-ticket-badge');
    expect(badge).toHaveTextContent('2 closed');
    expect(screen.getByTestId('kpi-card-acknowledged')).toContainElement(badge);
    expect(screen.getByTestId('kpi-card-acknowledged')).not.toHaveTextContent('without ticket');
  });

  it('uses learned team_assignment_groups instead of blindly copying team label in CreateIncidentModal', async () => {
    const user = userEvent.setup();
    const unmappedTeamAlert = makeAlert({
      id: 'aaaa1111-1111-4111-8111-111111111111',
      state: 'acknowledged',
      alert_name: 'UnmappedTeamAlert',
      team: 'core-db',
      team_source: 'label',
      labels: {
        alertname: 'UnmappedTeamAlert',
        severity: 'critical',
        team: 'core-db',
      },
    });
    const mappedTeamAlert = makeAlert({
      id: 'bbbb2222-2222-4222-8222-222222222222',
      state: 'acknowledged',
      alert_name: 'MappedTeamAlert',
      team: 'observability',
      team_source: 'label',
      labels: {
        alertname: 'MappedTeamAlert',
        severity: 'warning',
        team: 'observability',
      },
    });

    mockedApi.getServiceNowMetadata.mockResolvedValueOnce(
      makeMetadata({
        default_assignment_group: 'Network Operations',
        assignment_groups: [
          { sys_id: 'grp_1', name: 'Network Operations' },
          { sys_id: 'grp_2', name: 'Database SRE' },
          { sys_id: 'grp_3', name: 'Platform Engineering' },
        ],
        team_assignment_groups: {
          observability: 'Platform Engineering',
        },
      })
    );
    mockedApi.listAlerts.mockResolvedValue(makeQueueResponse([unmappedTeamAlert, mappedTeamAlert]));

    render(
      <AlertQueuePage
        pluginSettings={{ pollIntervalSeconds: 60 }}
        selectedAlertId={null}
        onSelectAlertId={jest.fn()}
      />
    );

    await waitFor(() => expect(screen.getByTestId('kpi-card-acknowledged')).toBeInTheDocument());
    await user.click(screen.getByTestId('kpi-card-acknowledged'));
    await waitFor(() => expect(screen.getByText('UnmappedTeamAlert')).toBeInTheDocument());

    // Unmapped team must fall back to default_assignment_group ("Network Operations"), NOT "core-db"
    await user.click(screen.getByTestId(`row-actions-trigger-${unmappedTeamAlert.id}`));
    await user.click(
      within(screen.getByRole('menu')).getByRole('menuitem', { name: 'Create ServiceNow Incident' })
    );
    let dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByText('Network Operations')).toBeInTheDocument();
    expect(within(dialog).queryByText('core-db')).not.toBeInTheDocument();
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }));

    // Mapped team must pre-select learned group "Platform Engineering"
    await user.click(screen.getByTestId(`row-actions-trigger-${mappedTeamAlert.id}`));
    await user.click(
      within(screen.getByRole('menu')).getByRole('menuitem', { name: 'Create ServiceNow Incident' })
    );
    dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByText('Platform Engineering')).toBeInTheDocument();
    expect(
      within(dialog).getByText('Auto-selected from learned mapping for team "observability".')
    ).toBeInTheDocument();
  });

  it('blocks dismissing alerts with open ServiceNow tickets in row menu, drawer, bulk bar, and modal', async () => {
    const user = userEvent.setup();
    const openTicketAlert = makeAlert({
      id: 'aaaa1111-1111-4111-8111-111111111111',
      state: 'acknowledged',
      alert_name: 'OpenTicketAlert',
      servicenow_incident: {
        id: 'inc-open-1',
        sync_status: 'created',
        snow_sys_id: 'snow_open_1',
        incident_number: 'INC0005555',
        incident_url: 'http://snow.example/INC0005555',
        snow_state: 2,
        snow_state_label: 'In Progress',
        priority: 1,
        assignment_group: 'Database SRE',
        caller_mode: 'sso_user',
        caller_resolution_status: 'sso_matched',
        snow_state_synced_at: '2026-11-26T09:00:00Z',
      },
    });
    const closedTicketAlert = makeAlert({
      id: 'bbbb2222-2222-4222-8222-222222222222',
      state: 'acknowledged',
      alert_name: 'ClosedTicketAlert',
      incident_drift_status: 'active_closed_ticket',
      servicenow_incident: {
        id: 'inc-closed-1',
        sync_status: 'created',
        snow_sys_id: 'snow_closed_1',
        incident_number: 'INC0004444',
        incident_url: null,
        snow_state: 7,
        snow_state_label: 'Closed',
        priority: 2,
        assignment_group: 'Infrastructure',
        caller_mode: 'sso_user',
        caller_resolution_status: 'sso_matched',
        snow_state_synced_at: '2026-11-26T09:00:00Z',
      },
    });

    mockedApi.listAlerts.mockResolvedValue(makeQueueResponse([openTicketAlert, closedTicketAlert]));
    mockedApi.getAlert.mockResolvedValue(makeDetail(openTicketAlert));
    mockedApi.getAlertHistory.mockResolvedValue({
      alert_id: openTicketAlert.id,
      fingerprint: openTicketAlert.fingerprint,
      audit_events: [],
      previous_episodes: [],
    });
    mockedApi.dismissAlert.mockRejectedValueOnce(
      new ApiClientError(
        409,
        'ALERT_HAS_OPEN_INCIDENT',
        'Cannot dismiss alert ClosedTicketAlert while ServiceNow incident INC0009999 is open.',
        { alert_id: closedTicketAlert.id, incident_number: 'INC0009999' }
      )
    );

    const Wrapper: React.FC = () => {
      const [selectedId, setSelectedId] = React.useState<string | null>(null);
      return (
        <AlertQueuePage
          pluginSettings={{ pollIntervalSeconds: 60 }}
          selectedAlertId={selectedId}
          onSelectAlertId={setSelectedId}
        />
      );
    };

    render(<Wrapper />);
    await waitFor(() => expect(screen.getByTestId('kpi-card-acknowledged')).toBeInTheDocument());
    await user.click(screen.getByTestId('kpi-card-acknowledged'));
    await waitFor(() => expect(screen.getByText('OpenTicketAlert')).toBeInTheDocument());

    // Row menu: Dismiss is disabled with explanatory title on open ticket, enabled on closed ticket
    await user.click(screen.getByTestId(`row-actions-trigger-${openTicketAlert.id}`));
    const openMenu = await screen.findByTestId(`row-actions-menu-${openTicketAlert.id}`);
    const disabledDismiss = within(openMenu).getByRole('menuitem', { name: 'Dismiss...' });
    expect(disabledDismiss).toBeDisabled();
    expect(disabledDismiss).toHaveAttribute(
      'title',
      'Cannot dismiss while ServiceNow incident INC0005555 is open'
    );

    // Bulk bar: open-ticket alert excluded while closed-ticket alert remains dismiss-eligible
    await user.click(screen.getByLabelText('Select OpenTicketAlert'));
    expect(screen.getByTestId('bulk-dismiss-blocked-notice')).toBeInTheDocument();
    expect(
      within(screen.getByTestId('bulk-selection-bar')).getByRole('button', { name: 'Dismiss Selected' })
    ).toBeDisabled();

    await user.click(screen.getByLabelText('Select ClosedTicketAlert'));
    expect(
      within(screen.getByTestId('bulk-selection-bar')).getByRole('button', {
        name: 'Dismiss Eligible (1)',
      })
    ).toBeEnabled();

    // Drawer: Dismiss button is disabled for open-ticket alert
    await user.click(screen.getByRole('button', { name: 'OpenTicketAlert' }));
    const drawerBar = await screen.findByTestId('drawer-action-bar');
    expect(within(drawerBar).getByRole('button', { name: 'Dismiss' })).toBeDisabled();
    await user.click(screen.getByRole('button', { name: 'Close' }));

    // Closed-ticket alert row menu Dismiss opens modal and surfaces inline 409 error cleanly
    await user.click(screen.getByTestId(`row-actions-trigger-${closedTicketAlert.id}`));
    const closedMenu = await screen.findByTestId(`row-actions-menu-${closedTicketAlert.id}`);
    const enabledDismiss = within(closedMenu).getByRole('menuitem', { name: 'Dismiss...' });
    expect(enabledDismiss).toBeEnabled();
    await user.click(enabledDismiss);

    const dialog = await screen.findByRole('dialog');
    await user.type(within(dialog).getByLabelText('Dismiss Reason'), 'Trying dismiss after race');
    await user.click(within(dialog).getByRole('button', { name: 'Confirm Dismiss' }));
    const conflictBanner = await within(dialog).findByTestId('dismiss-modal-conflict-error');
    expect(conflictBanner).toHaveTextContent('INC0009999');
  });

  it('renders Previous Occurrences with pagination in drawer and supports History view browsing', async () => {
    const user = userEvent.setup();
    const activeAlert = makeAlert({
      id: 'aaaa1111-1111-4111-8111-111111111111',
      alert_name: 'HighCPU',
      previous_episode_count: 2,
    });
    const resolvedAlert = makeAlert({
      id: 'cccc3333-3333-4333-8333-333333333333',
      alert_name: 'ResolvedDiskAlert',
      state: 'resolved',
      starts_at: '2026-11-24T10:00:00Z',
      resolved_at: '2026-11-24T10:35:00Z',
      servicenow_incident: {
        id: 'inc-res-1',
        sync_status: 'created',
        snow_sys_id: 'snow_res_1',
        incident_number: 'INC0003333',
        incident_url: 'http://snow.example/INC0003333',
        snow_state: 7,
        snow_state_label: 'Closed',
        priority: 2,
        assignment_group: 'Infrastructure',
        caller_mode: 'sso_user',
        caller_resolution_status: 'sso_matched',
        snow_state_synced_at: '2026-11-24T11:00:00Z',
      },
    });

    mockedApi.listAlerts
      .mockResolvedValueOnce(makeQueueResponse([activeAlert]))
      .mockResolvedValueOnce(makeQueueResponse([resolvedAlert]))
      .mockResolvedValueOnce(makeQueueResponse([resolvedAlert]));
    mockedApi.getAlert.mockResolvedValue(makeDetail(activeAlert));
    mockedApi.getAlertHistory
      .mockResolvedValueOnce({
        alert_id: activeAlert.id,
        fingerprint: activeAlert.fingerprint,
        audit_events: [],
        previous_episodes_total: 2,
        limit: 20,
        offset: 0,
        has_more: true,
        previous_episodes: [
          {
            id: 'ep-1',
            starts_at: '2026-11-20T08:00:00Z',
            resolved_at: '2026-11-20T08:45:00Z',
            duration_seconds: 2700,
            state: 'resolved',
            severity: 'critical',
            acknowledged_by: 'alice.smith',
            incident_number: 'INC0001111',
            snow_state_label: 'Closed',
            incident_url: 'http://snow.example/INC0001111',
            assignment_group: 'Platform Engineering',
          },
        ],
      })
      .mockResolvedValueOnce({
        alert_id: activeAlert.id,
        fingerprint: activeAlert.fingerprint,
        audit_events: [],
        previous_episodes_total: 2,
        limit: 20,
        offset: 1,
        has_more: false,
        previous_episodes: [
          {
            id: 'ep-2',
            starts_at: '2026-11-18T08:00:00Z',
            resolved_at: '2026-11-18T08:10:00Z',
            duration_seconds: 600,
            state: 'dismissed',
            severity: 'warning',
            dismissed_by: 'bob.jones',
            dismiss_reason: 'Planned reboot',
            incident_number: null,
            snow_state_label: null,
          },
        ],
      });

    const Wrapper: React.FC = () => {
      const [selectedId, setSelectedId] = React.useState<string | null>(null);
      return (
        <AlertQueuePage
          pluginSettings={{ pollIntervalSeconds: 60 }}
          selectedAlertId={selectedId}
          onSelectAlertId={setSelectedId}
        />
      );
    };

    render(<Wrapper />);
    await waitFor(() => expect(screen.getByText('HighCPU')).toBeInTheDocument());

    // Open Drawer and verify Previous Occurrences section + paginated Load more
    await user.click(screen.getByRole('button', { name: 'HighCPU' }));
    const prevSection = await screen.findByTestId('drawer-previous-occurrences');
    expect(prevSection).toHaveTextContent('45m');
    expect(prevSection).toHaveTextContent('Acked by alice.smith');
    expect(prevSection).toHaveTextContent('INC0001111');

    await user.click(within(prevSection).getByRole('button', { name: 'View full history →' }));
    const episodesTab = await screen.findByTestId('drawer-episodes-tab');
    await user.click(within(episodesTab).getByTestId('drawer-episodes-load-more'));
    await waitFor(() => expect(episodesTab).toHaveTextContent('Dismissed by bob.jones'));
    expect(episodesTab).toHaveTextContent('Planned reboot');

    await user.click(screen.getByRole('button', { name: 'Close' }));

    // Switch to History view -> queries state=resolved & shows ServiceNow column + time range
    await user.click(screen.getByTestId('view-tab-history'));
    await waitFor(() =>
      expect(mockedApi.listAlerts).toHaveBeenCalledWith(
        expect.objectContaining({
          states: ['resolved'],
          sort: 'resolved_at_desc',
        }),
        expect.any(AbortSignal)
      )
    );
    await waitFor(() => expect(screen.getByText('ResolvedDiskAlert')).toBeInTheDocument());
    expect(screen.getByRole('columnheader', { name: 'ServiceNow' })).toBeInTheDocument();
    expect(screen.getByText('INC0003333 ↗')).toBeInTheDocument();
    expect(screen.getByLabelText('Filter by time range')).toBeInTheDocument();

    await user.click(screen.getByLabelText('Filter by time range'));
    await user.click(await screen.findByText('Last 7d'));
    await waitFor(() =>
      expect(mockedApi.listAlerts).toHaveBeenLastCalledWith(
        expect.objectContaining({
          states: ['resolved'],
          from: expect.any(String),
        }),
        expect.any(AbortSignal)
      )
    );
  });

  it('renders Instance column, filters by Instance dropdown, and toggles interactive column-header sorting', async () => {
    const user = userEvent.setup();
    const alertA = makeAlert({
      id: '11111111-1111-4111-8111-111111111111',
      alert_name: 'CpuThrottlingHigh',
      instance: 'app-01.eu1:9100',
      labels: {
        alertname: 'CpuThrottlingHigh',
        severity: 'critical',
        instance: 'app-01.eu1:9100',
      },
    });
    const alertB = makeAlert({
      id: '22222222-2222-4222-8222-222222222222',
      alert_name: 'DiskLatencyHigh',
      instance: 'db-02.eu1:9100',
      labels: {
        alertname: 'DiskLatencyHigh',
        severity: 'warning',
        instance: 'db-02.eu1:9100',
      },
    });

    mockedApi.listAlerts.mockResolvedValue(makeQueueResponse([alertA, alertB]));

    render(
      <AlertQueuePage
        pluginSettings={{ pollIntervalSeconds: 60 }}
        selectedAlertId={null}
        onSelectAlertId={jest.fn()}
      />
    );

    await waitFor(() => expect(screen.getByText('CpuThrottlingHigh')).toBeInTheDocument());
    expect(screen.getByTestId(`alert-row-${alertA.id}`)).toHaveTextContent('app-01.eu1:9100');
    expect(screen.getByTestId(`alert-row-${alertB.id}`)).toHaveTextContent('db-02.eu1:9100');

    // Interactive column-header sorting: Instance -> instance_asc -> instance_desc
    await user.click(screen.getByTestId('sort-header-instance'));
    await waitFor(() =>
      expect(mockedApi.listAlerts).toHaveBeenLastCalledWith(
        expect.objectContaining({ sort: 'instance_asc' }),
        expect.any(AbortSignal)
      )
    );
    expect(screen.getByRole('columnheader', { name: 'Instance' })).toHaveAttribute(
      'aria-sort',
      'ascending'
    );

    await user.click(screen.getByTestId('sort-header-instance'));
    await waitFor(() =>
      expect(mockedApi.listAlerts).toHaveBeenLastCalledWith(
        expect.objectContaining({ sort: 'instance_desc' }),
        expect.any(AbortSignal)
      )
    );
    expect(screen.getByRole('columnheader', { name: 'Instance' })).toHaveAttribute(
      'aria-sort',
      'descending'
    );

    // Interactive column-header sorting: Alert -> alert_name_asc; Severity -> severity_desc
    await user.click(screen.getByTestId('sort-header-alert'));
    await waitFor(() =>
      expect(mockedApi.listAlerts).toHaveBeenLastCalledWith(
        expect.objectContaining({ sort: 'alert_name_asc' }),
        expect.any(AbortSignal)
      )
    );

    await user.click(screen.getByTestId('sort-header-severity'));
    await waitFor(() =>
      expect(mockedApi.listAlerts).toHaveBeenLastCalledWith(
        expect.objectContaining({ sort: 'severity_desc' }),
        expect.any(AbortSignal)
      )
    );

    // Instance dropdown filtering
    await user.click(screen.getByLabelText('Filter by instance'));
    await user.click(await screen.findByText('Instance: db-02.eu1:9100'));
    await waitFor(() =>
      expect(mockedApi.listAlerts).toHaveBeenLastCalledWith(
        expect.objectContaining({ instances: ['db-02.eu1:9100'] }),
        expect.any(AbortSignal)
      )
    );
  });

  it('supports one-click Confluence doc links, unlinked fallback creation, URL validation, and Viewer RBAC', async () => {
    const user = userEvent.setup();
    const linkedAlert = makeAlert({
      id: '11111111-1111-4111-8111-111111111111',
      alert_name: 'LinkedDocAlert',
      confluence_url: 'https://confluence.corp.example/display/NOC/LinkedDoc',
      confluence_url_source: 'noc_override',
      confluence_url_updated_by: 'alice.smith',
    });
    const unlinkedAlert = makeAlert({
      id: '22222222-2222-4222-8222-222222222222',
      alert_name: 'UnlinkedDocAlert',
      confluence_url: null,
      confluence_url_source: 'none',
    });
    const newlyLinked = makeAlert({
      ...unlinkedAlert,
      confluence_url: 'https://confluence.corp.example/display/NOC/UnlinkedDoc',
      confluence_url_source: 'noc_override',
      confluence_url_updated_by: 'editor.user',
    });

    mockedApi.listAlerts.mockResolvedValue(makeQueueResponse([linkedAlert, unlinkedAlert]));
    mockedApi.getAlert
      .mockResolvedValueOnce(makeDetail(unlinkedAlert))
      .mockResolvedValueOnce(makeDetail(newlyLinked))
      .mockResolvedValueOnce(makeDetail(unlinkedAlert));
    mockedApi.getAlertHistory.mockResolvedValue({
      alert_id: unlinkedAlert.id,
      fingerprint: unlinkedAlert.fingerprint,
      audit_events: [],
      previous_episodes: [],
    });
    mockedApi.updateConfluenceUrl.mockResolvedValueOnce(newlyLinked);

    const Wrapper: React.FC = () => {
      const [selectedId, setSelectedId] = React.useState<string | null>(null);
      return (
        <AlertQueuePage
          pluginSettings={{ pollIntervalSeconds: 60 }}
          selectedAlertId={selectedId}
          onSelectAlertId={setSelectedId}
        />
      );
    };

    const { unmount } = render(<Wrapper />);
    await waitFor(() => expect(screen.getByText('LinkedDocAlert')).toBeInTheDocument());

    // Linked alert exposes direct validated link with target="_blank" and rel="noopener noreferrer"
    const directLink = screen.getByTestId(`confluence-link-${linkedAlert.id}`);
    expect(directLink).toHaveAttribute('href', 'https://confluence.corp.example/display/NOC/LinkedDoc');
    expect(directLink).toHaveAttribute('target', '_blank');
    expect(directLink).toHaveAttribute('rel', 'noopener noreferrer');

    // Unlinked alert shows + Add Doc fallback -> opens drawer Confluence form
    const addBtn = screen.getByTestId(`add-confluence-btn-${unlinkedAlert.id}`);
    await user.click(addBtn);

    const confSection = await screen.findByTestId('drawer-confluence-section');
    const urlInput = within(confSection).getByLabelText('Confluence documentation URL');

    // Rejects insecure http:// URL client-side before sending
    await user.type(urlInput, 'http://insecure.example/page');
    await user.click(within(confSection).getByRole('button', { name: 'Save Confluence URL' }));
    expect(await within(confSection).findByTestId('drawer-confluence-error')).toHaveTextContent(
      'https://'
    );
    expect(mockedApi.updateConfluenceUrl).not.toHaveBeenCalled();

    // Accepts valid https:// URL and persists via API
    await user.clear(urlInput);
    await user.type(urlInput, 'https://confluence.corp.example/display/NOC/UnlinkedDoc');
    await user.click(within(confSection).getByRole('button', { name: 'Save Confluence URL' }));

    await waitFor(() =>
      expect(mockedApi.updateConfluenceUrl).toHaveBeenCalledWith(unlinkedAlert.id, {
        confluence_url: 'https://confluence.corp.example/display/NOC/UnlinkedDoc',
      })
    );
    const savedLink = await within(confSection).findByTestId('drawer-confluence-link');
    expect(savedLink).toHaveAttribute(
      'href',
      'https://confluence.corp.example/display/NOC/UnlinkedDoc'
    );
    expect(savedLink).toHaveAttribute('rel', 'noopener noreferrer');
    unmount();

    // Viewer cannot add/update Confluence URLs
    setMockGrafanaUser('Viewer', { [RBAC_ACTIONS.ALERTS_READ]: true });
    render(
      <AlertQueuePage
        pluginSettings={{ pollIntervalSeconds: 60 }}
        selectedAlertId={unlinkedAlert.id}
        onSelectAlertId={jest.fn()}
      />
    );
    await waitFor(() =>
      expect(screen.getByTestId(`add-confluence-btn-${unlinkedAlert.id}`)).toBeDisabled()
    );
    const viewerSection = await screen.findByTestId('drawer-confluence-section');
    expect(within(viewerSection).getByLabelText('Confluence documentation URL')).toBeDisabled();
    expect(
      within(viewerSection).getByRole('button', { name: 'Save Confluence URL' })
    ).toBeDisabled();
  });
});
