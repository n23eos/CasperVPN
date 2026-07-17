BEGIN;

-- Durable rebuild queue (TZ-persistence). The "issue a fresh config" loop was
-- in-memory (a Go channel), so pending rebuilds were lost on restart and could
-- not be shared across instances. This table makes the queue durable: workers
-- claim rows with FOR UPDATE SKIP LOCKED, so several control-plane instances can
-- drain one queue safely.
CREATE TABLE rebuild_jobs (
    id           BIGSERIAL PRIMARY KEY,
    kind         TEXT NOT NULL CHECK (kind IN ('node', 'user')),
    target_id    TEXT NOT NULL,               -- node_id or user_id depending on kind
    attempts     INT NOT NULL DEFAULT 0,      -- claims so far (poison-job cap lives in code)
    available_at TIMESTAMPTZ NOT NULL DEFAULT now(), -- earliest claimable time (retry backoff)
    claimed_at   TIMESTAMPTZ,                 -- lease start; NULL = unclaimed
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Fast lookup of due, unclaimed jobs in id order.
CREATE INDEX idx_rebuild_jobs_due ON rebuild_jobs (available_at, id) WHERE claimed_at IS NULL;

COMMIT;
