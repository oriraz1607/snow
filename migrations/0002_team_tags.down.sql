-- Note: PostgreSQL does not support ALTER TYPE ... DROP VALUE, so 'alert_team_tagged'
-- remains in audit_event_type after rolling back 0002 unless the enum is manually recreated.

DROP INDEX IF EXISTS idx_alerts_non_active_ack;
DROP TRIGGER IF EXISTS trg_alert_fingerprint_teams_updated_at ON alert_fingerprint_teams;
DROP TABLE IF EXISTS alert_fingerprint_teams;

ALTER TABLE alerts
    DROP COLUMN IF EXISTS assigned_team_at,
    DROP COLUMN IF EXISTS assigned_team_by,
    DROP COLUMN IF EXISTS assigned_team;
