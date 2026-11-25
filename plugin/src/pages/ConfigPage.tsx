import React, { useEffect, useState } from 'react';
import { css } from '@emotion/css';
import { AppPluginMeta, GrafanaTheme2, SelectableValue } from '@grafana/data';
import { getBackendSrv } from '@grafana/runtime';
import {
  Alert as GrafanaAlert,
  Button,
  Field,
  FieldSet,
  Input,
  SecretInput,
  Select,
  useTheme2,
} from '@grafana/ui';
import { alertApi, ApiClientError } from '../api/client';
import { AppPluginSettings, PLUGIN_ID, SnowCallerMode } from '../types/api';
import { clampPollIntervalSeconds } from '../utils/format';
import { canWriteSettings } from '../utils/permissions';

export interface ConfigPageProps {
  meta?: AppPluginMeta<AppPluginSettings>;
}

const CALLER_MODE_OPTIONS: Array<SelectableValue<SnowCallerMode>> = [
  { label: 'SSO User (with automatic fallback)', value: 'sso_user' },
  { label: 'Fallback Service Account (noc)', value: 'generic_noc' },
];

export const ConfigPage: React.FC<ConfigPageProps> = ({ meta }) => {
  const theme = useTheme2();
  const styles = getStyles(theme);
  const allowed = canWriteSettings();

  const initialJson = meta?.jsonData ?? {};
  const [apiBaseUrl, setApiBaseUrl] = useState(initialJson.apiBaseUrl ?? 'http://api:8080');
  const [pollIntervalSeconds, setPollIntervalSeconds] = useState<number>(
    clampPollIntervalSeconds(initialJson.pollIntervalSeconds)
  );
  const [defaultCallerMode, setDefaultCallerMode] = useState<SnowCallerMode>(
    initialJson.defaultCallerMode ?? 'sso_user'
  );
  const [genericNocDisplayLabel, setGenericNocDisplayLabel] = useState(
    initialJson.genericNocDisplayLabel ?? 'Fallback Service Account (noc)'
  );
  const [isSecretConfigured, setIsSecretConfigured] = useState<boolean>(
    Boolean(meta?.secureJsonFields?.apiJwtSecret)
  );
  const [apiJwtSecret, setApiJwtSecret] = useState('');

  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState(false);
  const [saveFeedback, setSaveFeedback] = useState<{
    severity: 'success' | 'error';
    message: string;
  } | null>(null);
  const [healthFeedback, setHealthFeedback] = useState<{
    severity: 'success' | 'error';
    message: string;
  } | null>(null);

  useEffect(() => {
    if (!allowed) {
      return;
    }
    const observable = getBackendSrv()?.fetch<AppPluginMeta<AppPluginSettings>>({
      url: `/api/plugins/${PLUGIN_ID}/settings`,
      method: 'GET',
      showErrorAlert: false,
      showSuccessAlert: false,
    });
    const sub = observable?.subscribe({
      next: (resp) => {
        const meta = resp.data;
        if (meta?.jsonData) {
          if (meta.jsonData.apiBaseUrl) {
            setApiBaseUrl(meta.jsonData.apiBaseUrl);
          }
          if (meta.jsonData.pollIntervalSeconds) {
            setPollIntervalSeconds(clampPollIntervalSeconds(meta.jsonData.pollIntervalSeconds));
          }
          if (meta.jsonData.defaultCallerMode) {
            setDefaultCallerMode(meta.jsonData.defaultCallerMode);
          }
          if (meta.jsonData.genericNocDisplayLabel) {
            setGenericNocDisplayLabel(meta.jsonData.genericNocDisplayLabel);
          }
        }
        if (meta?.secureJsonFields) {
          setIsSecretConfigured(Boolean(meta.secureJsonFields.apiJwtSecret));
        }
      },
      error: () => {
        // Keep props values if settings fetch fails.
      },
    });
    return () => sub?.unsubscribe();
  }, [allowed]);

  if (!allowed) {
    return (
      <div className={styles.page}>
        <GrafanaAlert
          severity="error"
          title="Access Denied"
          data-testid="config-access-denied"
        >
          Administrator role (noc-alert-manager-app.settings:write permission) is required to view or modify Alert Manager plugin configuration.
        </GrafanaAlert>
      </div>
    );
  }

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    setSaving(true);
    setSaveFeedback(null);

    try {
      const clampedPoll = clampPollIntervalSeconds(pollIntervalSeconds);
      const trimmedSecret = apiJwtSecret.trim();
      await alertApi.savePluginSettings(
        true,
        {
          apiBaseUrl: apiBaseUrl.trim().replace(/\/+$/, ''),
          pollIntervalSeconds: clampedPoll,
          defaultCallerMode,
          genericNocDisplayLabel:
            genericNocDisplayLabel.trim() || 'Fallback Service Account (noc)',
        },
        trimmedSecret ? { apiJwtSecret: trimmedSecret } : undefined
      );

      setPollIntervalSeconds(clampedPoll);
      if (trimmedSecret) {
        setIsSecretConfigured(true);
        setApiJwtSecret('');
      }
      setSaveFeedback({
        severity: 'success',
        message: 'Plugin settings saved and backend proxy reloaded.',
      });
    } catch (err) {
      const apiErr = err as ApiClientError;
      setSaveFeedback({
        severity: 'error',
        message: apiErr?.message || 'Failed to save plugin settings.',
      });
    } finally {
      setSaving(false);
    }
  };

  const handleTestConnection = async () => {
    setTesting(true);
    setHealthFeedback(null);
    try {
      const res = await alertApi.checkPluginHealth();
      setHealthFeedback({
        severity: res.status.toUpperCase() === 'OK' ? 'success' : 'error',
        message: res.message,
      });
    } catch (err) {
      const apiErr = err as ApiClientError;
      setHealthFeedback({
        severity: 'error',
        message: apiErr?.message || 'Health check failed.',
      });
    } finally {
      setTesting(false);
    }
  };

  return (
    <div className={styles.page}>
      <h1 className={styles.title}>Alert Manager Configuration</h1>
      <p className={styles.subtitle}>
        Configure the connection between the thin Grafana CallResource proxy and the standalone Alert Manager Go API.
      </p>

      {saveFeedback && (
        <div className={styles.alertBox}>
          <GrafanaAlert
            severity={saveFeedback.severity}
            title={saveFeedback.severity === 'success' ? 'Settings saved' : 'Save failed'}
          >
            {saveFeedback.message}
          </GrafanaAlert>
        </div>
      )}

      {healthFeedback && (
        <div className={styles.alertBox}>
          <GrafanaAlert
            severity={healthFeedback.severity}
            title={healthFeedback.severity === 'success' ? 'Connection healthy' : 'Connection check failed'}
          >
            {healthFeedback.message}
          </GrafanaAlert>
        </div>
      )}

      <form onSubmit={handleSave} className={styles.form}>
        <FieldSet label="API Proxy & Polling">
          <Field
            label="Go API Base URL"
            required
            description="Internal URL reachable by the Grafana server backend (e.g., http://api:8080)."
          >
            <Input
              value={apiBaseUrl}
              onChange={(e) => setApiBaseUrl(e.currentTarget.value)}
              placeholder="http://api:8080"
              aria-label="Go API Base URL"
            />
          </Field>

          <Field
            label="Queue Auto-Refresh Interval (seconds)"
            description="Allowed range: 5 to 120 seconds (default 15)."
          >
            <Input
              type="number"
              min={5}
              max={120}
              value={pollIntervalSeconds}
              onChange={(e) => setPollIntervalSeconds(Number(e.currentTarget.value))}
              aria-label="Poll Interval Seconds"
            />
          </Field>
        </FieldSet>

        <FieldSet label="ServiceNow Caller Defaults">
          <Field label="Default Caller Mode">
            <Select
              aria-label="Default Caller Mode"
              options={CALLER_MODE_OPTIONS}
              value={defaultCallerMode}
              onChange={(opt) => setDefaultCallerMode((opt.value ?? 'sso_user') as SnowCallerMode)}
            />
          </Field>

          <Field
            label="Fallback Service Account Display Label"
            description="Displayed in the Create Incident caller picker."
          >
            <Input
              value={genericNocDisplayLabel}
              onChange={(e) => setGenericNocDisplayLabel(e.currentTarget.value)}
              aria-label="Fallback Display Label"
            />
          </Field>
        </FieldSet>

        <FieldSet label="Internal JWT Signing Secret (Encrypted)">
          <Field
            label="API JWT Secret"
            description="Must match GRAFANA_PLUGIN_JWT_SECRET on the standalone Go API service."
          >
            <SecretInput
              isConfigured={isSecretConfigured}
              value={apiJwtSecret}
              placeholder="Enter shared HS256 secret"
              onChange={(e) => setApiJwtSecret(e.currentTarget.value)}
              onReset={() => {
                setIsSecretConfigured(false);
                setApiJwtSecret('');
              }}
              aria-label="API JWT Secret"
            />
          </Field>
        </FieldSet>

        <div className={styles.actions}>
          <Button variant="primary" type="submit" disabled={saving || !apiBaseUrl.trim()}>
            {saving ? 'Saving...' : 'Save Settings'}
          </Button>
          <Button variant="secondary" type="button" onClick={handleTestConnection} disabled={testing}>
            {testing ? 'Testing...' : 'Test Connection'}
          </Button>
        </div>
      </form>
    </div>
  );
};

const getStyles = (theme: GrafanaTheme2) => ({
  page: css`
    padding: ${theme.spacing(3)};
    max-width: 760px;
  `,
  title: css`
    margin: 0 0 ${theme.spacing(0.5)} 0;
    font-size: ${theme.typography.h2.fontSize};
  `,
  subtitle: css`
    margin: 0 0 ${theme.spacing(2)} 0;
    color: ${theme.colors.text.secondary};
  `,
  alertBox: css`
    margin-bottom: ${theme.spacing(2)};
  `,
  form: css`
    display: flex;
    flex-direction: column;
    gap: ${theme.spacing(2)};
  `,
  actions: css`
    display: flex;
    gap: ${theme.spacing(1.5)};
    margin-top: ${theme.spacing(1)};
  `,
});
