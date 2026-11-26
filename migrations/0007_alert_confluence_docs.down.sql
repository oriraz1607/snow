-- Rollback NOC Confluence alert documentation mapping table.
-- Note: PostgreSQL does not support ALTER TYPE ... DROP VALUE, so 'alert_confluence_url_updated'
-- remains in audit_event_type after rolling back 0007 unless the enum is manually recreated.

DROP TRIGGER IF EXISTS trg_alert_confluence_docs_updated_at ON alert_confluence_docs;
DROP TABLE IF EXISTS alert_confluence_docs;
