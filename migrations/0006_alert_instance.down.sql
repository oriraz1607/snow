-- Rollback first-class alert instance attribute and indexes.

DROP INDEX IF EXISTS idx_alerts_instance_exact;
DROP INDEX IF EXISTS idx_alerts_instance_lower_desc;
DROP INDEX IF EXISTS idx_alerts_instance_lower_asc;

ALTER TABLE alerts
    DROP COLUMN IF EXISTS instance;
