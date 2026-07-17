-- Billing persistence schema (Postgres). Mirrors store.Repository and the value
-- types in internal/model. No PII: accounts are referenced by opaque anon_user_id.
--
-- Apply once at deploy time (out of band, not on app start — concurrent instances
-- would otherwise race on migration). All statements are idempotent.

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

-- Open invoices are polled every cycle; index the hot predicate.
CREATE INDEX IF NOT EXISTS invoices_status_idx ON invoices (status);

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
ALTER TABLE settlements ADD COLUMN IF NOT EXISTS claimed_at             TIMESTAMPTZ NOT NULL DEFAULT now();
ALTER TABLE settlements ADD COLUMN IF NOT EXISTS activated_at           TIMESTAMPTZ;
ALTER TABLE settlements ADD COLUMN IF NOT EXISTS reconcile_leased_until TIMESTAMPTZ;

-- LeaseStuckSettlements filters claimed-but-old, unleased rows every recovery pass.
CREATE INDEX IF NOT EXISTS settlements_recover_idx ON settlements (claimed_at, reconcile_leased_until);

-- Billing-owned expiry index for subscriptions (no PII, no entitlement copy).
CREATE TABLE IF NOT EXISTS schedules (
    sub_id       TEXT        PRIMARY KEY,
    anon_user_id TEXT        NOT NULL,
    status       TEXT        NOT NULL,               -- contracts.SubscriptionStatus
    expires_at   TIMESTAMPTZ NOT NULL,
    grace_until  TIMESTAMPTZ NOT NULL
);

-- DueSchedules filters on status and expiry every sweep cycle.
CREATE INDEX IF NOT EXISTS schedules_due_idx ON schedules (status, expires_at);
