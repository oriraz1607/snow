-- Note: PostgreSQL does not support ALTER TYPE ... DROP VALUE, so 'snow_incident_reopened'
-- remains in audit_event_type after rolling back 0005 unless the enum is manually recreated.

DROP INDEX IF EXISTS idx_alerts_resolved_at_desc;
DROP TRIGGER IF EXISTS trg_team_assignment_group_mappings_updated_at ON team_assignment_group_mappings;
DROP TABLE IF EXISTS team_assignment_group_mappings;

ALTER TABLE servicenow_incidents
    DROP COLUMN IF EXISTS last_auto_reopened_at,
    DROP COLUMN IF EXISTS auto_reopen_count;
