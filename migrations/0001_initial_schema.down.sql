-- Rollback initial schema.

DROP TRIGGER IF EXISTS trg_servicenow_incidents_updated_at ON servicenow_incidents;
DROP TRIGGER IF EXISTS trg_alerts_updated_at ON alerts;
DROP FUNCTION IF EXISTS set_updated_at();

DROP TABLE IF EXISTS webhook_ingestion_log;
DROP TABLE IF EXISTS snow_user_cache;
DROP TABLE IF EXISTS alert_audit_log;
DROP TABLE IF EXISTS servicenow_incidents;
DROP TABLE IF EXISTS alerts;

DROP TYPE IF EXISTS actor_type;
DROP TYPE IF EXISTS audit_event_type;
DROP TYPE IF EXISTS snow_caller_resolution_status;
DROP TYPE IF EXISTS snow_caller_mode;
DROP TYPE IF EXISTS snow_sync_status;
DROP TYPE IF EXISTS alert_state;
