import { config } from '@grafana/runtime';
import { RBAC_ACTIONS } from '../types/api';

export interface CurrentGrafanaUser {
  login: string;
  email: string;
  name: string;
  orgRole: string;
  permissions: Record<string, boolean>;
}

const ALL_PLUGIN_ACTIONS = new Set<string>(Object.values(RBAC_ACTIONS));

export function getCurrentUser(): CurrentGrafanaUser {
  const user = (config as unknown as { bootData?: { user?: Partial<CurrentGrafanaUser> } })?.bootData?.user;
  return {
    login: user?.login ?? '',
    email: user?.email ?? '',
    name: user?.name ?? '',
    orgRole: user?.orgRole ?? '',
    permissions: user?.permissions ?? {},
  };
}

export function hasPluginPermission(action: string, user: CurrentGrafanaUser = getCurrentUser()): boolean {
  const perms = user.permissions ?? {};
  const hasExplicitPluginPermissions = Object.keys(perms).some((key) => ALL_PLUGIN_ACTIONS.has(key));

  if (hasExplicitPluginPermissions) {
    return Boolean(perms[action]);
  }

  switch (user.orgRole.toLowerCase()) {
    case 'admin':
      return ALL_PLUGIN_ACTIONS.has(action);
    case 'editor':
      return (
        action === RBAC_ACTIONS.ALERTS_READ ||
        action === RBAC_ACTIONS.ALERTS_TRIAGE ||
        action === RBAC_ACTIONS.INCIDENTS_WRITE
      );
    case 'viewer':
      return action === RBAC_ACTIONS.ALERTS_READ;
    default:
      return false;
  }
}

export function canReadAlerts(user?: CurrentGrafanaUser): boolean {
  return hasPluginPermission(RBAC_ACTIONS.ALERTS_READ, user);
}

export function canTriageAlerts(user?: CurrentGrafanaUser): boolean {
  return hasPluginPermission(RBAC_ACTIONS.ALERTS_TRIAGE, user);
}

export function canWriteIncidents(user?: CurrentGrafanaUser): boolean {
  return hasPluginPermission(RBAC_ACTIONS.INCIDENTS_WRITE, user);
}

export function canWriteSettings(user?: CurrentGrafanaUser): boolean {
  return hasPluginPermission(RBAC_ACTIONS.SETTINGS_WRITE, user);
}
