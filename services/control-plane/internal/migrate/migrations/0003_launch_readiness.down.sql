BEGIN;
DROP TABLE subscription_delivery_tokens;
ALTER TABLE users DROP COLUMN hysteria2_password;
ALTER TABLE subscriptions DROP CONSTRAINT valid_grace;
ALTER TABLE subscriptions DROP COLUMN grace_until;
ALTER TABLE subscriptions DROP COLUMN billing_revision;
COMMIT;
