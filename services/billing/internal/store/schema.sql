-- Billing persistence schema (Postgres). Mirrors store.Repository and the value
-- types in internal/model. No PII: accounts are referenced by opaque anon_user_id.
--
-- Apply once at deploy time (out of band, not on app start — concurrent instances
-- would otherwise race on migration). All statements are idempotent.
--
-- DEPLOYMENT ORDER: apply this DDL FIRST, then roll out the new binary. The new
-- predicates (last_negative_check_at, poll_leases) and columns are additive and the
-- old binary ignores them, so DDL-before-binary is safe.
-- ROLLBACK ORDER (reverse): roll back to the old binary FIRST, then optionally drop
-- the additions (DROP TABLE poll_leases; ALTER TABLE invoices DROP COLUMN
-- last_negative_check_at). The old binary tolerates the extra column/table, so the
-- drop is optional and never required for a rollback.

-- Crypto payment requests bound to an anonymous account.
CREATE TABLE IF NOT EXISTS invoices (
    id                  TEXT        PRIMARY KEY,
    provider            TEXT        NOT NULL,
    anon_user_id        TEXT        NOT NULL,
    plan                TEXT        NOT NULL,
    currency            TEXT        NOT NULL,
    amount              TEXT        NOT NULL,          -- exact decimal string
    pay_address         TEXT        NOT NULL,
    provider_invoice_id TEXT        NOT NULL,
    status              TEXT        NOT NULL,          -- pending|settled|expired|invalid
    created_at          TIMESTAMPTZ NOT NULL,
    expires_at          TIMESTAMPTZ NOT NULL
);

-- Durable client idempotency reservation. The stable order_id is chosen before
-- contacting the provider and is used to recover an ambiguous remote result.
CREATE TABLE IF NOT EXISTS invoice_intents (
    idempotency_key TEXT        PRIMARY KEY,
    request_hash    TEXT        NOT NULL,
    order_id        TEXT        NOT NULL UNIQUE,
    provider        TEXT        NOT NULL,
    anon_user_id    TEXT        NOT NULL,
    plan            TEXT        NOT NULL,
    currency        TEXT        NOT NULL,
    amount          TEXT        NOT NULL,
    state           TEXT        NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL
);

-- Open invoices are polled every cycle; index the hot predicate.
CREATE INDEX IF NOT EXISTS invoices_status_idx ON invoices (status);

-- last_negative_check_at records the time of the most recent DEFINITIVE negative
-- on-chain check (chain reachable, payment absent/insufficient) — NOT a chain API
-- error. An on-chain invoice may be expired only once a negative check taken at or
-- after its effective deadline exists, so a payment confirmed in the grace window is
-- never buried by a sweep that raced ahead of the poll.
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS last_negative_check_at TIMESTAMPTZ;

-- poll_leases: a durable per-invoice lease held by the poller across the whole
-- chain-check-through-durable-handoff window, so a concurrent sweep on another
-- instance cannot expire an invoice whose payment is being checked/credited. Each
-- acquire mints a fresh opaque lease_token; renew/release are CAS on that token so a
-- stale owner can never touch a lease that was reclaimed after its lease_until
-- lapsed. owner is observability only, never an ownership mechanism.
CREATE TABLE IF NOT EXISTS poll_leases (
    invoice_id  TEXT PRIMARY KEY REFERENCES invoices (id) ON DELETE CASCADE,
    lease_token TEXT        NOT NULL,
    owner       TEXT        NOT NULL,
    lease_until TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS poll_leases_until_idx ON poll_leases (lease_until);

-- Webhook-delivery dedup: a (provider, external_id) processed to completion.
CREATE TABLE IF NOT EXISTS seen_events (
    provider    TEXT NOT NULL,
    external_id TEXT NOT NULL,
    PRIMARY KEY (provider, external_id)
);

-- Settlement latch: presence of a row means the invoice is claimed/credited.
-- The primary key makes ClaimSettlement an atomic insert-if-absent. The recovery
-- columns let a reconciler finish a settlement whose process died mid-flight:
--   claimed_at             — when the credit was claimed (recover once older than a threshold)
--   activated_at           — remote activation applied (recovery then only flips status, no re-activate)
--   reconcile_leased_until — cross-process lease so two reconcilers never race the same invoice
CREATE TABLE IF NOT EXISTS settlements (
    invoice_id             TEXT PRIMARY KEY,
    claimed_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    activated_at           TIMESTAMPTZ,
    reconcile_leased_until TIMESTAMPTZ
);

-- Additive migration for an existing settlements table (idempotent).
-- NOTE: on a table that already holds rows, claimed_at back-stamps them with the
-- migration time and leaves activated_at NULL. Any pre-fix settlement that crashed
-- AFTER activating would then look "stuck, not activated" and be re-activated
-- ~SETTLEMENT_STALE_THRESHOLD later (a one-time double-period for those legacy rows).
-- The durable store is new (no such rows yet); if applying to a populated table,
-- back-fill activated_at for already-settled invoices first, or audit once at deploy.
ALTER TABLE settlements ADD COLUMN IF NOT EXISTS claimed_at             TIMESTAMPTZ NOT NULL DEFAULT now();
ALTER TABLE settlements ADD COLUMN IF NOT EXISTS activated_at           TIMESTAMPTZ;
ALTER TABLE settlements ADD COLUMN IF NOT EXISTS reconcile_leased_until TIMESTAMPTZ;

-- LeaseStuckSettlements filters claimed-but-old, unleased rows every recovery pass.
CREATE INDEX IF NOT EXISTS settlements_recover_idx ON settlements (claimed_at, reconcile_leased_until);

-- Billing-owned expiry index for subscriptions (no PII, no entitlement copy).
CREATE TABLE IF NOT EXISTS schedules (
    sub_id       TEXT        PRIMARY KEY,
    anon_user_id TEXT        NOT NULL,
    revision     BIGINT      NOT NULL DEFAULT 0,
    plan         TEXT        NOT NULL DEFAULT '',
    status       TEXT        NOT NULL,               -- contracts.SubscriptionStatus
    expires_at   TIMESTAMPTZ NOT NULL,
    grace_until  TIMESTAMPTZ NOT NULL
);
ALTER TABLE schedules ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 0;
ALTER TABLE schedules ADD COLUMN IF NOT EXISTS plan TEXT NOT NULL DEFAULT '';

-- Durable absolute control-plane writes. A credit row and the corresponding
-- schedule revision are inserted in one transaction. invoice_id uniqueness makes
-- one invoice one period; (sub_id, revision) makes the revision stream monotonic.
CREATE TABLE IF NOT EXISTS billing_deliveries (
    id           TEXT        PRIMARY KEY,
    invoice_id   TEXT        UNIQUE REFERENCES invoices (id) ON DELETE CASCADE,
    sub_id       TEXT        NOT NULL,
    anon_user_id TEXT        NOT NULL,
    revision     BIGINT      NOT NULL,
    plan         TEXT        NOT NULL DEFAULT '',
    status       TEXT        NOT NULL,
    expires_at   TIMESTAMPTZ NOT NULL,
    grace_until  TIMESTAMPTZ NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL,
    delivered_at TIMESTAMPTZ,
    leased_until TIMESTAMPTZ,
    UNIQUE (sub_id, revision)
);
ALTER TABLE billing_deliveries ADD COLUMN IF NOT EXISTS plan TEXT NOT NULL DEFAULT '';

-- A rolling upgrade may already have invoice deliveries with a plan while the
-- older schedule row has none. Preserve the newest known full billing snapshot so
-- a later expiry revision cannot erase that paid plan at the control-plane fence.
UPDATE schedules AS s
SET plan = latest.plan
FROM (
    SELECT DISTINCT ON (sub_id) sub_id, plan
    FROM billing_deliveries
    WHERE plan <> ''
    ORDER BY sub_id, revision DESC
) AS latest
WHERE s.sub_id = latest.sub_id AND s.plan = '';
CREATE INDEX IF NOT EXISTS billing_deliveries_pending_idx
    ON billing_deliveries (created_at, leased_until) WHERE delivered_at IS NULL;

-- DueSchedules filters on status and expiry every sweep cycle.
CREATE INDEX IF NOT EXISTS schedules_due_idx ON schedules (status, expires_at);
