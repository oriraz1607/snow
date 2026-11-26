-- Promote instance to a first-class alert attribute (PostgreSQL 15+ compatible).

ALTER TABLE alerts
    ADD COLUMN instance TEXT NOT NULL DEFAULT '';

UPDATE alerts
SET instance = COALESCE(
    NULLIF(BTRIM(labels->>'instance'), ''),
    NULLIF(BTRIM(labels->>'host'), ''),
    NULLIF(BTRIM(labels->>'hostname'), ''),
    NULLIF(BTRIM(labels->>'node'), ''),
    NULLIF(BTRIM(labels->>'pod'), ''),
    ''
)
WHERE instance = '';

CREATE INDEX idx_alerts_instance_lower_asc
    ON alerts ((NULLIF(LOWER(instance), '')) ASC NULLS LAST, id ASC)
    WHERE severity IN ('critical', 'warning');

CREATE INDEX idx_alerts_instance_lower_desc
    ON alerts ((NULLIF(LOWER(instance), '')) DESC NULLS LAST, id DESC)
    WHERE severity IN ('critical', 'warning');

CREATE INDEX idx_alerts_instance_exact
    ON alerts (instance)
    WHERE instance <> '' AND severity IN ('critical', 'warning');
