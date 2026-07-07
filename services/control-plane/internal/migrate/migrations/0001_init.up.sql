BEGIN;

-- Users: personal per-user isolation secrets live here (reality_short_id, vless
-- uuid, private key material). Blocking/burning one user never exposes another.
CREATE TABLE users (
    id               UUID PRIMARY KEY,
    telegram_id      BIGINT UNIQUE,
    email            TEXT UNIQUE,
    status           TEXT NOT NULL,
    reality_short_id TEXT NOT NULL UNIQUE,
    vless_uuid       TEXT NOT NULL UNIQUE,
    private_key      TEXT NOT NULL,
    subscription_id  UUID,
    device_limit     INT NOT NULL DEFAULT 3 CHECK (device_limit >= 0),
    quota_bytes      BIGINT NOT NULL DEFAULT 0 CHECK (quota_bytes >= 0),
    used_bytes       BIGINT NOT NULL DEFAULT 0 CHECK (used_bytes >= 0),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Subscriptions: NO card / payment data here (billing is a separate service).
-- The subscription token (sub_token) is stored ONLY as a hash; the plaintext is
-- returned to the caller once at creation and never persisted in the clear.
CREATE TABLE subscriptions (
    id                  UUID PRIMARY KEY,
    user_id             UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    plan                TEXT NOT NULL,
    status              TEXT NOT NULL,
    token_hash          TEXT NOT NULL UNIQUE,
    token_prefix        TEXT NOT NULL DEFAULT '',
    traffic_limit_bytes BIGINT NOT NULL DEFAULT 0 CHECK (traffic_limit_bytes >= 0),
    speed_limit_mbps    INT NOT NULL DEFAULT 0 CHECK (speed_limit_mbps >= 0),
    device_limit        INT NOT NULL DEFAULT 3 CHECK (device_limit >= 0),
    starts_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at          TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_subscriptions_user ON subscriptions(user_id);

ALTER TABLE users
    ADD CONSTRAINT fk_users_subscription
    FOREIGN KEY (subscription_id) REFERENCES subscriptions(id) ON DELETE SET NULL;

-- Nodes: dynamic fleet registry. Nothing hardcoded — IPs, regions, mimicry
-- domains all live in data. Entry/exit split via role + entry_node_id.
CREATE TABLE nodes (
    id                 TEXT PRIMARY KEY,
    role               TEXT NOT NULL,
    status             TEXT NOT NULL,
    entry_node_id      TEXT REFERENCES nodes(id) ON DELETE SET NULL,
    provider           TEXT NOT NULL DEFAULT '',
    cloud              TEXT NOT NULL DEFAULT '',
    region             TEXT NOT NULL DEFAULT '',
    entry_ip           TEXT NOT NULL DEFAULT '',
    ephemeral_entry_ip BOOLEAN NOT NULL DEFAULT false,
    capacity_users     INT NOT NULL DEFAULT 0,
    rotate_after       TIMESTAMPTZ,
    labels             JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at         TIMESTAMPTZ
);
CREATE INDEX idx_nodes_status ON nodes(status);
CREATE INDEX idx_nodes_role ON nodes(role);
CREATE INDEX idx_nodes_region ON nodes(region);

-- Transports: many per node (diversity, not monoculture). Variant params stored
-- as JSONB; common fields are columns.
CREATE TABLE transports (
    id       UUID PRIMARY KEY,
    node_id  TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    tag      TEXT NOT NULL,
    type     TEXT NOT NULL,
    version  TEXT NOT NULL,
    port     INT NOT NULL,
    enabled  BOOLEAN NOT NULL DEFAULT true,
    priority INT NOT NULL DEFAULT 0,
    params   JSONB NOT NULL DEFAULT '{}'::jsonb,
    UNIQUE (node_id, tag)
);
CREATE INDEX idx_transports_node ON transports(node_id);

-- History of node rotations / lifecycle transitions (register/rotate/retire).
CREATE TABLE node_rotation_history (
    id          UUID PRIMARY KEY,
    node_id     TEXT NOT NULL,
    from_status TEXT,
    to_status   TEXT NOT NULL,
    reason      TEXT NOT NULL DEFAULT '',
    entry_ip    TEXT,
    actor       TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_node_rotation_node ON node_rotation_history(node_id, created_at DESC);

-- History of per-user secret rotations (audit for isolation / abuse response).
CREATE TABLE user_secret_rotations (
    id                   UUID PRIMARY KEY,
    user_id              UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    old_reality_short_id TEXT NOT NULL,
    old_vless_uuid       TEXT NOT NULL,
    reason               TEXT NOT NULL DEFAULT '',
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_user_secret_rot_user ON user_secret_rotations(user_id, created_at DESC);

-- Built per-user Node×Transport sets. Source of truth handed to the
-- subscription service (Agent C) and the cache we invalidate on block.
CREATE TABLE subscription_sets (
    user_id      UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    revision     BIGINT NOT NULL DEFAULT 1,
    node_ids     TEXT[] NOT NULL DEFAULT '{}',
    bundle       JSONB NOT NULL,
    dirty        BOOLEAN NOT NULL DEFAULT true,
    generated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_subscription_sets_nodeids ON subscription_sets USING GIN (node_ids);
CREATE INDEX idx_subscription_sets_dirty ON subscription_sets(dirty) WHERE dirty;

-- FieldSignal aggregates from telemetry (the feedback loop input). Per node +
-- coarse region; verdict drives node degraded/blocked marking.
CREATE TABLE node_signal_aggregates (
    id            UUID PRIMARY KEY,
    node_id       TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    region        TEXT NOT NULL DEFAULT '',
    window_start  TIMESTAMPTZ NOT NULL,
    window_end    TIMESTAMPTZ NOT NULL,
    total         INT NOT NULL,
    failures      INT NOT NULL,
    block_signals INT NOT NULL,
    verdict       TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_node_signal_agg_node ON node_signal_aggregates(node_id, created_at DESC);

COMMIT;
