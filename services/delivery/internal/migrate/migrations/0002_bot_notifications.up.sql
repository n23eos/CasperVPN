CREATE TABLE IF NOT EXISTS delivery_bot_users (
    telegram_id BIGINT PRIMARY KEY CHECK (telegram_id > 0),
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS delivery_bot_notifications (
    telegram_id BIGINT NOT NULL REFERENCES delivery_bot_users(telegram_id) ON DELETE CASCADE,
    event_key TEXT NOT NULL CHECK (event_key <> ''),
    kind TEXT NOT NULL CHECK (kind IN ('activation', 'expiry', 'grace')),
    message TEXT NOT NULL CHECK (message <> ''),
    status TEXT NOT NULL CHECK (status IN ('pending', 'sent', 'canceled')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    sent_at TIMESTAMPTZ,
    PRIMARY KEY (telegram_id, event_key)
);

CREATE TABLE IF NOT EXISTS delivery_notification_cursor (
    singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    last_telegram_id BIGINT NOT NULL DEFAULT 0 CHECK (last_telegram_id >= 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO delivery_notification_cursor (singleton, last_telegram_id)
VALUES (TRUE, 0)
ON CONFLICT (singleton) DO NOTHING;
