-- Add learned team->assignment_group mappings, ServiceNow auto-reopen tracking,
-- audit event for auto-reopen, and partial index for History (resolved) browsing.

ALTER TYPE audit_event_type ADD VALUE IF NOT EXISTS 'snow_incident_reopened';

ALTER TABLE servicenow_incidents
    ADD COLUMN auto_reopen_count INTEGER NOT NULL DEFAULT 0 CHECK (auto_reopen_count >= 0),
    ADD COLUMN last_auto_reopened_at TIMESTAMPTZ NULL;

CREATE TABLE team_assignment_group_mappings (
    team             VARCHAR(128) PRIMARY KEY,
    assignment_group VARCHAR(128) NOT NULL,
    updated_by       VARCHAR(255) NOT NULL,
    updated_by_email VARCHAR(255) NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TRIGGER trg_team_assignment_group_mappings_updated_at
    BEFORE UPDATE ON team_assignment_group_mappings
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

CREATE INDEX idx_alerts_resolved_at_desc
    ON alerts (resolved_at DESC NULLS LAST, id DESC)
    WHERE state = 'resolved' AND severity IN ('critical', 'warning');
