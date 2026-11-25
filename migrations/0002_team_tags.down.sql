DROP INDEX IF EXISTS idx_alerts_non_active_ack;
DROP TRIGGER IF EXISTS trg_alert_fingerprint_teams_updated_at ON alert_fingerprint_teams;
DROP TABLE IF EXISTS alert_fingerprint_teams;

ALTER TABLE alerts
    DROP COLUMN IF EXISTS assigned_team_at,
    DROP COLUMN IF EXISTS assigned_team_by,
    DROP COLUMN IF EXISTS assigned_team;
