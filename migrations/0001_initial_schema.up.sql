-- Initial schema for the alert-management system (PostgreSQL 15+).

-- ============================================================================
-- 1. ENUM TYPES
-- ============================================================================

CREATE TYPE alert_state AS ENUM (
    'firing',
    'acknowledged',
    'dismissed',
    'resolved'
);

CREATE TYPE snow_sync_status AS ENUM (
    'pending',
    'created',
    'linked',
    'failed'
);

CREATE TYPE snow_caller_mode AS ENUM (
    'sso_user',
    'generic_noc'
);

CREATE TYPE snow_caller_resolution_status AS ENUM (
    'pending',
    'sso_matched',
    'generic_noc_selected',
    'fallback_noc'
);

CREATE TYPE audit_event_type AS ENUM (
    'alert_fired',
    'alert_updated',
    'alert_resolved',
    'alert_acknowledged',
    'alert_unacknowledged',
    'alert_dismissed',
    'alert_reopened',
    'operator_note_added',
    'snow_incident_requested',
    'snow_incident_created',
    'snow_incident_failed',
    'snow_incident_linked',
    'snow_caller_fallback',
    'snow_state_synced',
    'snow_work_note_posted'
);

CREATE TYPE actor_type AS ENUM (
    'operator',
    'alertmanager',
    'servicenow',
    'system'
);

-- ============================================================================
-- 2. ALERTS TABLE (ONE ROW PER FIRING EPISODE)
-- ============================================================================

CREATE TABLE alerts (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    fingerprint           VARCHAR(64) NOT NULL,
    starts_at             TIMESTAMPTZ NOT NULL,
    ends_at               TIMESTAMPTZ NULL,
    state                 alert_state NOT NULL DEFAULT 'firing',
    version               INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),

    -- Alertmanager metadata
    alert_name            TEXT NOT NULL,
    severity              VARCHAR(32) NOT NULL DEFAULT 'unknown',
    group_key             TEXT NOT NULL,
    receiver              TEXT NOT NULL,
    labels                JSONB NOT NULL DEFAULT '{}'::jsonb,
    annotations           JSONB NOT NULL DEFAULT '{}'::jsonb,
    generator_url         TEXT NULL,
    external_url          TEXT NULL,

    -- Operator triage state
    acknowledged_by       VARCHAR(255) NULL,
    acknowledged_by_email VARCHAR(255) NULL,
    acknowledged_at       TIMESTAMPTZ NULL,
    dismissed_by          VARCHAR(255) NULL,
    dismissed_by_email    VARCHAR(255) NULL,
    dismissed_at          TIMESTAMPTZ NULL,
    dismiss_reason        TEXT NULL,
    resolved_at           TIMESTAMPTZ NULL,

    -- Ingestion bookkeeping
    first_received_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_received_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    delivery_count        INTEGER NOT NULL DEFAULT 1 CHECK (delivery_count >= 1),
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    -- Episode deduplication key: same label set + same start timestamp = same episode
    CONSTRAINT uq_alerts_fingerprint_starts_at UNIQUE (fingerprint, starts_at),

    -- State integrity checks
    CONSTRAINT chk_alerts_ack_fields CHECK (
        state <> 'acknowledged' OR (acknowledged_by IS NOT NULL AND acknowledged_at IS NOT NULL)
    ),
    CONSTRAINT chk_alerts_dismiss_fields CHECK (
        state <> 'dismissed' OR (dismissed_by IS NOT NULL AND dismissed_at IS NOT NULL)
    ),
    CONSTRAINT chk_alerts_resolved_fields CHECK (
        state <> 'resolved' OR resolved_at IS NOT NULL
    )
);

-- ============================================================================
-- 3. SERVICENOW INCIDENTS TABLE (1:0..1 PER ALERT EPISODE)
-- ============================================================================

CREATE TABLE servicenow_incidents (
    id                        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    alert_id                  UUID NOT NULL REFERENCES alerts(id) ON DELETE CASCADE,
    correlation_id            VARCHAR(128) NOT NULL,
    sync_status               snow_sync_status NOT NULL DEFAULT 'pending',

    -- ServiceNow record identity & URL
    snow_sys_id               VARCHAR(64) NULL,
    incident_number           VARCHAR(40) NULL,
    incident_url              TEXT NULL,

    -- ServiceNow lifecycle state (1=New, 2=In Progress, 3=On Hold, 6=Resolved, 7=Closed, 8=Canceled)
    snow_state                SMALLINT NULL CHECK (snow_state IS NULL OR snow_state IN (1, 2, 3, 6, 7, 8)),
    snow_state_label          VARCHAR(32) NULL,
    impact                    SMALLINT NOT NULL DEFAULT 2 CHECK (impact BETWEEN 1 AND 3),
    urgency                   SMALLINT NOT NULL DEFAULT 2 CHECK (urgency BETWEEN 1 AND 3),
    priority                  SMALLINT NULL CHECK (priority IS NULL OR priority BETWEEN 1 AND 5),

    -- Submitted ticket metadata
    short_description         VARCHAR(160) NOT NULL,
    description               TEXT NULL,
    assignment_group          VARCHAR(128) NULL,
    category                  VARCHAR(64) NULL,

    -- Caller attribution
    caller_mode               snow_caller_mode NOT NULL DEFAULT 'sso_user',
    caller_resolution_status  snow_caller_resolution_status NOT NULL DEFAULT 'pending',
    caller_sys_id             VARCHAR(64) NULL,
    caller_fallback_reason    TEXT NULL,

    -- Audit & sync tracking
    created_by_login          VARCHAR(255) NOT NULL,
    created_by_email          VARCHAR(255) NULL,
    snow_opened_at            TIMESTAMPTZ NULL,
    snow_resolved_at          TIMESTAMPTZ NULL,
    snow_closed_at            TIMESTAMPTZ NULL,
    snow_state_synced_at      TIMESTAMPTZ NULL,
    resolution_note_posted_at TIMESTAMPTZ NULL,
    last_error                TEXT NULL,
    retry_count               INTEGER NOT NULL DEFAULT 0 CHECK (retry_count >= 0),
    created_at                TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at                TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    -- Guarantees at most one primary ticket record per alert episode
    CONSTRAINT uq_snow_incidents_alert_id UNIQUE (alert_id),
    -- Guarantees unique correlation_id for newly created incidents
    CONSTRAINT uq_snow_incidents_correlation_id UNIQUE (correlation_id),
    CONSTRAINT chk_snow_created_has_sys_id CHECK (
        sync_status NOT IN ('created', 'linked') OR (snow_sys_id IS NOT NULL AND incident_number IS NOT NULL)
    )
);

-- ============================================================================
-- 4. IMMUTABLE AUDIT LOG TABLE
-- ============================================================================

CREATE TABLE alert_audit_log (
    id             BIGSERIAL PRIMARY KEY,
    alert_id       UUID NOT NULL REFERENCES alerts(id) ON DELETE CASCADE,
    event_type     audit_event_type NOT NULL,
    actor_type     actor_type NOT NULL,
    actor_login    VARCHAR(255) NOT NULL,
    actor_email    VARCHAR(255) NULL,
    actor_role     VARCHAR(64) NULL,
    previous_state alert_state NULL,
    new_state      alert_state NULL,
    comment        TEXT NULL,
    metadata       JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ============================================================================
-- 5. SERVICENOW SSO USER LOOKUP CACHE
-- ============================================================================

CREATE TABLE snow_user_cache (
    email          VARCHAR(255) PRIMARY KEY,
    snow_sys_id    VARCHAR(64) NULL,
    snow_user_name VARCHAR(128) NULL,
    is_active      BOOLEAN NOT NULL DEFAULT FALSE,
    fetched_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at     TIMESTAMPTZ NOT NULL
);

-- ============================================================================
-- 6. WEBHOOK INGESTION LOG
-- ============================================================================

CREATE TABLE webhook_ingestion_log (
    id               BIGSERIAL PRIMARY KEY,
    source           VARCHAR(32) NOT NULL CHECK (source IN ('alertmanager', 'servicenow')),
    group_key        TEXT NULL,
    payload_hash     VARCHAR(64) NOT NULL,
    alert_count      INTEGER NOT NULL DEFAULT 0,
    truncated_alerts INTEGER NOT NULL DEFAULT 0,
    http_status      SMALLINT NOT NULL,
    processing_ms    INTEGER NOT NULL,
    error_message    TEXT NULL,
    received_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ============================================================================
-- 7. UPDATED_AT TRIGGERS
-- ============================================================================

CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_alerts_updated_at
    BEFORE UPDATE ON alerts
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_servicenow_incidents_updated_at
    BEFORE UPDATE ON servicenow_incidents
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

-- ============================================================================
-- 8. INDEXES
-- ============================================================================

-- 1. Enforce at most ONE open episode per Alertmanager fingerprint
CREATE UNIQUE INDEX uq_alerts_active_fingerprint
    ON alerts (fingerprint)
    WHERE state IN ('firing', 'acknowledged', 'dismissed');

-- 2. Primary queue listing & keyset pagination
CREATE INDEX idx_alerts_state_starts_at
    ON alerts (state, starts_at DESC, id DESC);

-- 3. Severity filtering on active queue
CREATE INDEX idx_alerts_active_severity
    ON alerts (severity, starts_at DESC)
    WHERE state IN ('firing', 'acknowledged');

-- 4. Historical episode lookup by fingerprint
CREATE INDEX idx_alerts_fingerprint_starts_at_desc
    ON alerts (fingerprint, starts_at DESC);

-- 5. JSONB label containment filtering
CREATE INDEX idx_alerts_labels_gin
    ON alerts USING GIN (labels jsonb_path_ops);

-- 6. Open ServiceNow ticket sync & drift lookup
CREATE INDEX idx_snow_incidents_open_sync
    ON servicenow_incidents (snow_state_synced_at ASC NULLS FIRST)
    WHERE sync_status IN ('created', 'linked') AND snow_state IN (1, 2, 3);

-- 7. Fast lookup for inbound ServiceNow webhook updates
CREATE INDEX idx_snow_incidents_sys_id
    ON servicenow_incidents (snow_sys_id)
    WHERE snow_sys_id IS NOT NULL;

CREATE INDEX idx_snow_incidents_number
    ON servicenow_incidents (incident_number)
    WHERE incident_number IS NOT NULL;

-- 8. Audit timeline lookup per alert episode
CREATE INDEX idx_audit_log_alert_id_created
    ON alert_audit_log (alert_id, created_at ASC, id ASC);

-- 9. Cache cleanup / expiry scan
CREATE INDEX idx_snow_user_cache_expires_at
    ON snow_user_cache (expires_at);

-- 10. Retention cleanup for webhook_ingestion_log
CREATE INDEX idx_webhook_ingestion_log_received_at
    ON webhook_ingestion_log (received_at);
