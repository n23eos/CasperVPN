BEGIN;
ALTER TABLE subscriptions ADD COLUMN billing_revision BIGINT NOT NULL DEFAULT 0 CHECK (billing_revision >= 0);
ALTER TABLE subscriptions ADD COLUMN grace_until TIMESTAMPTZ;
ALTER TABLE subscriptions ADD CONSTRAINT valid_grace CHECK (grace_until IS NULL OR (expires_at IS NOT NULL AND grace_until >= expires_at));
ALTER TABLE users ADD COLUMN hysteria2_password TEXT NOT NULL DEFAULT '';
-- Two random UUIDs yield independent credentials without requiring pgcrypto.
UPDATE users SET hysteria2_password = replace(gen_random_uuid()::text || gen_random_uuid()::text, '-', '') WHERE hysteria2_password = '';
CREATE TABLE subscription_delivery_tokens (
 subscription_id UUID PRIMARY KEY REFERENCES subscriptions(id) ON DELETE CASCADE,
 token_hash TEXT UNIQUE NOT NULL,
 ciphertext BYTEA NOT NULL,
 key_id TEXT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Existing primary hashes stay authoritative in subscriptions; no token rotation.
COMMIT;
