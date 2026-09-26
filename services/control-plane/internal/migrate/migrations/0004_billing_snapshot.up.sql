BEGIN;
ALTER TABLE subscriptions ADD COLUMN billing_state_hash TEXT NOT NULL DEFAULT '';
COMMIT;
