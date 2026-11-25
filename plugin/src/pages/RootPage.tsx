import React, { useCallback, useEffect, useState } from 'react';
import { AppRootProps } from '@grafana/data';
import { locationService } from '@grafana/runtime';
import { AppPluginSettings, PLUGIN_ID } from '../types/api';
import { AlertQueuePage } from './AlertQueuePage';
import { ConfigPage } from './ConfigPage';

const PLUGIN_BASE = `/a/${PLUGIN_ID}`;

function parsePluginRoute(pathname: string): { page: 'alerts' | 'config'; alertId: string | null } {
  const clean = pathname.replace(/\/+$/, '');
  if (clean === `${PLUGIN_BASE}/config` || clean.endsWith('/config')) {
    return { page: 'config', alertId: null };
  }
  const match = clean.match(/\/alerts\/([^/]+)$/);
  if (match && match[1]) {
    return { page: 'alerts', alertId: decodeURIComponent(match[1]) };
  }
  return { page: 'alerts', alertId: null };
}

export const RootPage: React.FC<AppRootProps<AppPluginSettings>> = ({ meta, path }) => {
  const getInitialPath = () => {
    try {
      return locationService.getLocation().pathname || path || `${PLUGIN_BASE}/alerts`;
    } catch {
      return typeof window !== 'undefined' ? window.location.pathname : path || `${PLUGIN_BASE}/alerts`;
    }
  };

  const [currentPath, setCurrentPath] = useState<string>(getInitialPath);

  useEffect(() => {
    let sub: { unsubscribe: () => void } | undefined;
    try {
      sub = locationService.getLocationObservable().subscribe((loc: { pathname: string }) => {
        if (loc?.pathname) {
          setCurrentPath(loc.pathname);
        }
      });
    } catch {
      // Fallback when locationService observable is unavailable in isolated tests.
    }
    return () => {
      sub?.unsubscribe();
    };
  }, []);

  const handleSelectAlertId = useCallback((alertId: string | null) => {
    const nextPath = alertId
      ? `${PLUGIN_BASE}/alerts/${encodeURIComponent(alertId)}`
      : `${PLUGIN_BASE}/alerts`;
    setCurrentPath(nextPath);
    try {
      locationService.push(nextPath);
    } catch {
      // No-op in isolated tests without Grafana router.
    }
  }, []);

  const route = parsePluginRoute(currentPath);

  if (route.page === 'config') {
    return <ConfigPage meta={meta} />;
  }

  return (
    <AlertQueuePage
      pluginSettings={meta?.jsonData ?? {}}
      selectedAlertId={route.alertId}
      onSelectAlertId={handleSelectAlertId}
    />
  );
};
