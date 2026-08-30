CREATE TABLE IF NOT EXISTS notification_digest_state (
    user_id text PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    last_sent_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
