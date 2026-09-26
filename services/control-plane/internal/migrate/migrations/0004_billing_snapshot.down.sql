BEGIN;
ALTER TABLE subscriptions DROP COLUMN billing_state_hash;
COMMIT;
