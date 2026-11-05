CREATE TABLE IF NOT EXISTS payment_invoices (
    id           BIGSERIAL PRIMARY KEY,
    bot_user_id  BIGINT NOT NULL REFERENCES bots(bot_user_id) ON DELETE CASCADE,
    user_id      BIGINT NOT NULL DEFAULT 0,
    provider     TEXT NOT NULL,
    currency     TEXT NOT NULL,
    amount       BIGINT NOT NULL,
    title        TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    payload      TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL DEFAULT 'pending',
    external_id  TEXT NOT NULL DEFAULT '',
    payment_url  TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    paid_at      TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS payment_invoices_bot_status_idx ON payment_invoices (bot_user_id, status);

CREATE TABLE IF NOT EXISTS bot_payment_webhooks (
    bot_user_id  BIGINT PRIMARY KEY REFERENCES bots(bot_user_id) ON DELETE CASCADE,
    url          TEXT NOT NULL,
    secret       TEXT NOT NULL DEFAULT '',
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
