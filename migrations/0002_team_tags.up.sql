-- Add operator team tagging (episode + fingerprint inheritance) and index for Non-Active Acknowledged view.

ALTER TYPE audit_event_type ADD VALUE IF NOT EXISTS 'alert_team_tagged';

ALTER TABLE alerts
    ADD COLUMN assigned_team VARCHAR(128) NULL,
    ADD COLUMN assigned_team_by VARCHAR(255) NULL,
    ADD COLUMN assigned_team_at TIMESTAMPTZ NULL;

CREATE TABLE alert_fingerprint_teams (
    fingerprint     VARCHAR(64) PRIMARY KEY,
    team            VARCHAR(128) NOT NULL,
    tagged_by       VARCHAR(255) NOT NULL,
    tagged_by_email VARCHAR(255) NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TRIGGER trg_alert_fingerprint_teams_updated_at
    BEFORE UPDATE ON alert_fingerprint_teams
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

CREATE INDEX idx_alerts_non_active_ack
    ON alerts (resolved_at DESC, id DESC)
    WHERE state = 'resolved' AND acknowledged_at IS NOT NULL;
