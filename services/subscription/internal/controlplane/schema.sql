-- subscription-owned token index (TZ-persistence).
--
-- Maps an opaque per-user subscription token to the user and subscription it
-- belongs to. Only token HASHES are stored (hex SHA-256, controlplane.HashToken)
-- — plaintext tokens never live at rest (TZ-token-revocation §3).
--
-- Apply OUT OF BAND (migration job/step), not on application startup, to avoid a
-- migrate-on-boot race between horizontally-scaled instances (Production Checklist).

CREATE TABLE IF NOT EXISTS subscription_tokens (
    token_hash      TEXT PRIMARY KEY,                 -- hex SHA-256 of the token
    user_id         TEXT NOT NULL,
    subscription_id TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Revocation by user (ban/suspend) and by subscription (cancel/expire/rotate)
-- delete whole sets of rows; index the lookup columns so those stay cheap.
CREATE INDEX IF NOT EXISTS subscription_tokens_user_idx ON subscription_tokens (user_id);
CREATE INDEX IF NOT EXISTS subscription_tokens_sub_idx ON subscription_tokens (subscription_id);
