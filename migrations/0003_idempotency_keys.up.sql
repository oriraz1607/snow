-- Persist client Idempotency-Key responses for single and bulk ServiceNow incident creation.

CREATE TABLE idempotency_keys (
    idempotency_key VARCHAR(128) PRIMARY KEY,
    endpoint        VARCHAR(128) NOT NULL,
    request_hash    VARCHAR(64) NOT NULL,
    actor_login     VARCHAR(255) NOT NULL,
    status          VARCHAR(16) NOT NULL CHECK (status IN ('in_progress', 'completed')),
    response_status SMALLINT NULL,
    response_body   JSONB NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at      TIMESTAMPTZ NOT NULL
);

CREATE TRIGGER trg_idempotency_keys_updated_at
    BEFORE UPDATE ON idempotency_keys
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

CREATE INDEX idx_idempotency_keys_expires_at
    ON idempotency_keys (expires_at);
