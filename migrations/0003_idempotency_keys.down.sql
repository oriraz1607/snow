DROP INDEX IF EXISTS idx_idempotency_keys_expires_at;
DROP TRIGGER IF EXISTS trg_idempotency_keys_updated_at ON idempotency_keys;
DROP TABLE IF EXISTS idempotency_keys;
