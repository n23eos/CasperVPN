CREATE TABLE IF NOT EXISTS delivery_telegram_updates (
    update_id BIGINT PRIMARY KEY,
    status TEXT NOT NULL CHECK (status IN ('pending', 'done')),
    attempts INT NOT NULL DEFAULT 1 CHECK (attempts > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS delivery_telegram_cursor (
    singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    last_update_id BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO delivery_telegram_cursor (singleton, last_update_id)
VALUES (TRUE, 0)
ON CONFLICT (singleton) DO NOTHING;
