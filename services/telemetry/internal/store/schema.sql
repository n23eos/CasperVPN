-- CasperVPN telemetry — time-series schema (Postgres).
--
-- Anonymity by construction: NO user id, NO client IP, NO precise geo. Only what
-- contracts.FieldSignal carries — coarse region/ASN/ISP + transport + verdict.
-- Works on vanilla Postgres; on TimescaleDB add the hypertable line noted below.

CREATE TABLE IF NOT EXISTS telemetry_signals (
    signal_id         TEXT        NOT NULL,          -- client dedup key (random, non-identifying)
    node_id           TEXT        NOT NULL,
    transport_type    TEXT        NOT NULL,
    transport_version TEXT        NOT NULL,
    region            TEXT        NOT NULL,          -- coarse, e.g. 'RU-MOW'
    asn               INTEGER     NOT NULL DEFAULT 0,
    isp               TEXT        NOT NULL DEFAULT '',
    signal_type       TEXT        NOT NULL,
    success           BOOLEAN     NOT NULL DEFAULT FALSE,
    rtt_ms            INTEGER     NOT NULL DEFAULT 0,
    loss_pct          DOUBLE PRECISION NOT NULL DEFAULT 0,
    client_version    TEXT        NOT NULL DEFAULT '',
    platform          TEXT        NOT NULL DEFAULT '',
    observed_at       TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (signal_id)                          -- server-side dedup / anti-replay
);

-- Query pattern: "everything in the last window, sliced by (region, transport)".
CREATE INDEX IF NOT EXISTS idx_signals_observed_at
    ON telemetry_signals (observed_at);
CREATE INDEX IF NOT EXISTS idx_signals_region_transport_time
    ON telemetry_signals (region, transport_type, observed_at);
CREATE INDEX IF NOT EXISTS idx_signals_node_time
    ON telemetry_signals (node_id, observed_at);

-- TimescaleDB (optional): SELECT create_hypertable('telemetry_signals','observed_at');
-- Retention there: SELECT add_retention_policy('telemetry_signals', INTERVAL '72 hours');
-- On vanilla Postgres, retention is enforced by the service's Prune loop.

CREATE TABLE IF NOT EXISTS telemetry_health (
    id                   BIGINT      GENERATED ALWAYS AS IDENTITY,
    node_id              TEXT        NOT NULL,
    status               TEXT        NOT NULL,
    probe_source         TEXT        NOT NULL,
    blocked_from_regions TEXT[]      NOT NULL DEFAULT '{}',
    latency_ms           INTEGER     NOT NULL DEFAULT 0,
    detail               TEXT        NOT NULL DEFAULT '',
    observed_at          TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_health_observed_at
    ON telemetry_health (observed_at);
CREATE INDEX IF NOT EXISTS idx_health_node_time
    ON telemetry_health (node_id, observed_at);
