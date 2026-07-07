BEGIN;

DROP TABLE IF EXISTS node_signal_aggregates;
DROP TABLE IF EXISTS subscription_sets;
DROP TABLE IF EXISTS user_secret_rotations;
DROP TABLE IF EXISTS node_rotation_history;
DROP TABLE IF EXISTS transports;
DROP TABLE IF EXISTS nodes;
ALTER TABLE IF EXISTS users DROP CONSTRAINT IF EXISTS fk_users_subscription;
DROP TABLE IF EXISTS subscriptions;
DROP TABLE IF EXISTS users;

COMMIT;
