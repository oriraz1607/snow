-- Add NOC-managed Confluence alert documentation mapping table and audit event (PostgreSQL 15+ compatible).

ALTER TYPE audit_event_type ADD VALUE IF NOT EXISTS 'alert_confluence_url_updated';

CREATE TABLE alert_confluence_docs (
    alert_name       TEXT PRIMARY KEY,
    confluence_url   TEXT NOT NULL,
    updated_by       VARCHAR(255) NOT NULL,
    updated_by_email VARCHAR(255) NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TRIGGER trg_alert_confluence_docs_updated_at
    BEFORE UPDATE ON alert_confluence_docs
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();
