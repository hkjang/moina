ALTER TABLE notifications
    ADD COLUMN IF NOT EXISTS delivered_at timestamptz NOT NULL DEFAULT now();

CREATE INDEX IF NOT EXISTS notifications_digest_delivery_idx
    ON notifications(user_id,delivered_at)
    WHERE type <> 'digest';
