ALTER TABLE notifications
    ADD COLUMN IF NOT EXISTS emailed_at timestamptz;

CREATE INDEX IF NOT EXISTS notifications_email_delivery_idx
    ON notifications(user_id,created_at DESC)
    WHERE emailed_at IS NOT NULL;
