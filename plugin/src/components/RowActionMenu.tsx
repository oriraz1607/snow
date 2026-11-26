import React, { useEffect, useLayoutEffect, useRef, useState } from 'react';
import ReactDOM from 'react-dom';
import { css } from '@emotion/css';
import { GrafanaTheme2 } from '@grafana/data';
import { useTheme2 } from '@grafana/ui';
import { Alert } from '../types/api';
import { getOpenIncidentDismissBlockReason } from '../utils/format';

const DISABLED_TRIAGE_REASON = 'Requires Editor or Admin role (alerts:triage permission)';
const DISABLED_INCIDENT_REASON = 'Requires Editor or Admin role (incidents:write permission)';

interface MenuItemDef {
  id: string;
  label: string;
  allowed: boolean;
  disabledReason?: string;
  danger?: boolean;
  dividerBefore?: boolean;
  onSelect: () => void;
}

export interface RowActionMenuProps {
  alert: Alert;
  hasLinkedIncident: boolean;
  canTriage: boolean;
  canWriteIncidents: boolean;
  busy?: boolean;
  onUnacknowledge: (alert: Alert) => void;
  onReopen: (alert: Alert) => void;
  onOpenDismiss: (alert: Alert) => void;
  onOpenCreateIncident: (alert: Alert) => void;
  onOpenLinkIncident: (alert: Alert) => void;
  onOpenAddNote: (alert: Alert) => void;
  onOpenWorkNote: (alert: Alert) => void;
  onSyncIncident: (alert: Alert) => void;
  onSelectAlert: (alert: Alert) => void;
}

interface MenuCoords {
  top: number;
  left: number;
}

export const RowActionMenu: React.FC<RowActionMenuProps> = ({
  alert,
  hasLinkedIncident,
  canTriage,
  canWriteIncidents,
  busy = false,
  onUnacknowledge,
  onReopen,
  onOpenDismiss,
  onOpenCreateIncident,
  onOpenLinkIncident,
  onOpenAddNote,
  onOpenWorkNote,
  onSyncIncident,
  onSelectAlert,
}) => {
  const theme = useTheme2();
  const styles = getStyles(theme);
  const [open, setOpen] = useState(false);
  const [coords, setCoords] = useState<MenuCoords>({ top: 0, left: 0 });
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  const menuRef = useRef<HTMLDivElement | null>(null);
  const openIncidentDismissReason = getOpenIncidentDismissBlockReason(alert);

  const items: MenuItemDef[] = [];
  if (alert.state === 'firing') {
    const canAckWithTicket = canWriteIncidents && canTriage && !busy;
    const ackWithTicketReason = !canWriteIncidents
      ? DISABLED_INCIDENT_REASON
      : !canTriage
        ? DISABLED_TRIAGE_REASON
        : undefined;
    items.push(
      {
        id: 'create_incident',
        label: 'Acknowledge & Create Incident',
        allowed: canAckWithTicket,
        disabledReason: ackWithTicketReason,
        onSelect: () => onOpenCreateIncident(alert),
      },
      {
        id: 'link_incident',
        label: 'Acknowledge & Link Incident...',
        allowed: canAckWithTicket,
        disabledReason: ackWithTicketReason,
        onSelect: () => onOpenLinkIncident(alert),
      },
      {
        id: 'dismiss',
        label: 'Dismiss...',
        allowed: canTriage && !busy && !openIncidentDismissReason,
        disabledReason: !canTriage ? DISABLED_TRIAGE_REASON : openIncidentDismissReason,
        danger: true,
        onSelect: () => onOpenDismiss(alert),
      }
    );
  } else if (alert.state === 'acknowledged' && !hasLinkedIncident) {
    items.push(
      {
        id: 'create_incident',
        label: 'Create ServiceNow Incident',
        allowed: canWriteIncidents && !busy,
        disabledReason: !canWriteIncidents ? DISABLED_INCIDENT_REASON : undefined,
        onSelect: () => onOpenCreateIncident(alert),
      },
      {
        id: 'link_incident',
        label: 'Link Existing Incident...',
        allowed: canWriteIncidents && !busy,
        disabledReason: !canWriteIncidents ? DISABLED_INCIDENT_REASON : undefined,
        onSelect: () => onOpenLinkIncident(alert),
      },
      {
        id: 'add_note',
        label: 'Add Triage Note...',
        allowed: canTriage && !busy,
        disabledReason: !canTriage ? DISABLED_TRIAGE_REASON : undefined,
        onSelect: () => onOpenAddNote(alert),
      },
      {
        id: 'unacknowledge',
        label: 'Unacknowledge',
        allowed: canTriage && !busy,
        disabledReason: !canTriage ? DISABLED_TRIAGE_REASON : undefined,
        onSelect: () => onUnacknowledge(alert),
      },
      {
        id: 'dismiss',
        label: 'Dismiss...',
        allowed: canTriage && !busy && !openIncidentDismissReason,
        disabledReason: !canTriage ? DISABLED_TRIAGE_REASON : openIncidentDismissReason,
        danger: true,
        onSelect: () => onOpenDismiss(alert),
      }
    );
  } else if (alert.state === 'acknowledged' && hasLinkedIncident) {
    items.push(
      {
        id: 'work_note',
        label: 'Add ServiceNow Work Note...',
        allowed: canWriteIncidents && !busy,
        disabledReason: !canWriteIncidents ? DISABLED_INCIDENT_REASON : undefined,
        onSelect: () => onOpenWorkNote(alert),
      },
      {
        id: 'sync_status',
        label: 'Sync ServiceNow Status',
        allowed: canWriteIncidents && !busy,
        disabledReason: !canWriteIncidents ? DISABLED_INCIDENT_REASON : undefined,
        onSelect: () => onSyncIncident(alert),
      },
      {
        id: 'add_note',
        label: 'Add Triage Note...',
        allowed: canTriage && !busy,
        disabledReason: !canTriage ? DISABLED_TRIAGE_REASON : undefined,
        onSelect: () => onOpenAddNote(alert),
      },
      {
        id: 'dismiss',
        label: 'Dismiss...',
        allowed: canTriage && !busy && !openIncidentDismissReason,
        disabledReason: !canTriage ? DISABLED_TRIAGE_REASON : openIncidentDismissReason,
        danger: true,
        onSelect: () => onOpenDismiss(alert),
      }
    );
  } else if (
    alert.state === 'resolved' &&
    alert.incident_drift_status === 'resolved_open_ticket' &&
    alert.servicenow_incident
  ) {
    items.push(
      {
        id: 'work_note',
        label: 'Post Work Note...',
        allowed: canWriteIncidents && !busy,
        disabledReason: !canWriteIncidents ? DISABLED_INCIDENT_REASON : undefined,
        onSelect: () => onOpenWorkNote(alert),
      },
      {
        id: 'sync_status',
        label: 'Refresh ServiceNow Status',
        allowed: canWriteIncidents && !busy,
        disabledReason: !canWriteIncidents ? DISABLED_INCIDENT_REASON : undefined,
        onSelect: () => onSyncIncident(alert),
      }
    );
  } else if (alert.state === 'dismissed') {
    items.push({
      id: 'reopen',
      label: 'Reopen Alert',
      allowed: canTriage && !busy,
      disabledReason: !canTriage ? DISABLED_TRIAGE_REASON : undefined,
      onSelect: () => onReopen(alert),
    });
  }

  items.push({
    id: 'view_details',
    label: 'View Details',
    allowed: true,
    dividerBefore: items.length > 0,
    onSelect: () => onSelectAlert(alert),
  });

  useLayoutEffect(() => {
    if (!open || !triggerRef.current) {
      return;
    }
    const rect = triggerRef.current.getBoundingClientRect();
    const menuWidth = 224;
    const estimatedHeight = items.length * 34 + 16;
    const viewportHeight = typeof window !== 'undefined' ? window.innerHeight : 900;
    const viewportWidth = typeof window !== 'undefined' ? window.innerWidth : 1440;

    const flipUp = rect.bottom + estimatedHeight > viewportHeight - 12 && rect.top > estimatedHeight + 12;
    const top = flipUp ? Math.max(8, rect.top - estimatedHeight - 4) : rect.bottom + 4;
    const left = Math.max(8, Math.min(viewportWidth - menuWidth - 8, rect.right - menuWidth));
    setCoords({ top, left });
  }, [open, items.length]);

  useEffect(() => {
    if (!open) {
      return;
    }
    const handleMouseDown = (ev: MouseEvent) => {
      const target = ev.target as Node | null;
      if (
        (triggerRef.current && target && triggerRef.current.contains(target)) ||
        (menuRef.current && target && menuRef.current.contains(target))
      ) {
        return;
      }
      setOpen(false);
    };
    const handleKeyDown = (ev: KeyboardEvent) => {
      if (ev.key === 'Escape') {
        setOpen(false);
        triggerRef.current?.focus();
      }
    };
    const handleViewportChange = () => {
      setOpen(false);
    };

    document.addEventListener('mousedown', handleMouseDown);
    document.addEventListener('keydown', handleKeyDown);
    window.addEventListener('scroll', handleViewportChange, true);
    window.addEventListener('resize', handleViewportChange);

    return () => {
      document.removeEventListener('mousedown', handleMouseDown);
      document.removeEventListener('keydown', handleKeyDown);
      window.removeEventListener('scroll', handleViewportChange, true);
      window.removeEventListener('resize', handleViewportChange);
    };
  }, [open]);

  const menuPortal =
    open && typeof document !== 'undefined'
      ? ReactDOM.createPortal(
          <div
            ref={menuRef}
            role="menu"
            aria-label={`Actions menu for ${alert.alert_name}`}
            data-testid={`row-actions-menu-${alert.id}`}
            className={styles.menu}
            style={{ top: `${coords.top}px`, left: `${coords.left}px` }}
          >
            {items.map((item) => (
              <React.Fragment key={item.id}>
                {item.dividerBefore && <div className={styles.divider} role="separator" />}
                <button
                  type="button"
                  role="menuitem"
                  disabled={!item.allowed}
                  title={!item.allowed ? item.disabledReason : undefined}
                  className={`${styles.menuItem} ${item.danger ? styles.dangerItem : ''} ${
                    !item.allowed ? styles.disabledItem : ''
                  }`}
                  onClick={() => {
                    if (!item.allowed) {
                      return;
                    }
                    setOpen(false);
                    item.onSelect();
                  }}
                >
                  {item.label}
                </button>
              </React.Fragment>
            ))}
          </div>,
          document.body
        )
      : null;

  return (
    <>
      <button
        ref={triggerRef}
        type="button"
        className={`${styles.trigger} ${open ? styles.triggerOpen : ''}`}
        aria-label={`Actions for ${alert.alert_name}`}
        aria-haspopup="menu"
        aria-expanded={open}
        data-testid={`row-actions-trigger-${alert.id}`}
        onClick={() => setOpen((prev) => !prev)}
      >
        <svg width="14" height="16" viewBox="0 0 14 16" fill="none" aria-hidden="true">
          <rect x="5" y="1" width="4" height="4" rx="0.9" fill="currentColor" />
          <rect x="5" y="6" width="4" height="4" rx="0.9" fill="currentColor" />
          <rect x="5" y="11" width="4" height="4" rx="0.9" fill="currentColor" />
        </svg>
      </button>
      {menuPortal}
    </>
  );
};

const getStyles = (theme: GrafanaTheme2) => ({
  trigger: css`
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 30px;
    height: 30px;
    padding: 0;
    border: 1px solid transparent;
    border-radius: ${theme.shape.radius.default};
    background: transparent;
    color: ${theme.colors.text.secondary};
    cursor: pointer;
    transition: background 0.12s ease, color 0.12s ease, border-color 0.12s ease;

    &:hover,
    &:focus-visible {
      background: ${theme.colors.background.secondary};
      border-color: ${theme.colors.border.medium};
      color: ${theme.colors.text.primary};
      outline: none;
    }
  `,
  triggerOpen: css`
    background: ${theme.colors.background.secondary};
    border-color: ${theme.colors.primary.border};
    color: ${theme.colors.text.primary};
  `,
  menu: css`
    position: fixed;
    z-index: 9999;
    min-width: 224px;
    padding: ${theme.spacing(0.5)} 0;
    background: ${theme.colors.background.elevated};
    border: 1px solid ${theme.colors.border.medium};
    border-radius: ${theme.shape.radius.default};
    box-shadow: 0 8px 24px rgba(0, 0, 0, 0.45);
  `,
  divider: css`
    height: 1px;
    margin: ${theme.spacing(0.5)} 0;
    background: ${theme.colors.border.weak};
  `,
  menuItem: css`
    display: block;
    width: 100%;
    padding: ${theme.spacing(0.75)} ${theme.spacing(1.5)};
    border: none;
    background: transparent;
    text-align: left;
    font-size: ${theme.typography.bodySmall.fontSize};
    color: ${theme.colors.text.primary};
    cursor: pointer;

    &:hover:not(:disabled),
    &:focus-visible:not(:disabled) {
      background: ${theme.colors.emphasize(theme.colors.background.elevated, 0.08)};
      outline: none;
    }
  `,
  dangerItem: css`
    color: ${theme.colors.error.text};
  `,
  disabledItem: css`
    color: ${theme.colors.text.disabled};
    cursor: not-allowed;
  `,
});
