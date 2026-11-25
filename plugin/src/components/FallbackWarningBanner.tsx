import React from 'react';
import { Alert as GrafanaAlert } from '@grafana/ui';

export interface FallbackBannerInfo {
  incidentNumber: string;
  userEmail: string;
  reason?: string | null;
  warnings?: string[];
}

interface FallbackWarningBannerProps {
  info: FallbackBannerInfo | null;
  onDismiss: () => void;
}

export const FallbackWarningBanner: React.FC<FallbackWarningBannerProps> = ({ info, onDismiss }) => {
  if (!info) {
    return null;
  }

  const accountLabel = info.userEmail || 'your current Grafana account';
  const detailText =
    info.warnings && info.warnings.length > 0
      ? info.warnings.join(' ')
      : info.reason ||
        `Incident ${info.incidentNumber} was created using the fallback service account (noc) because ${accountLabel} could not be matched in ServiceNow.`;

  return (
    <div style={{ marginBottom: '12px' }} data-testid="fallback-warning-banner">
      <GrafanaAlert
        severity="warning"
        title={`Incident ${info.incidentNumber} created with Fallback Service Account`}
        onRemove={onDismiss}
      >
        {detailText}
      </GrafanaAlert>
    </div>
  );
};
