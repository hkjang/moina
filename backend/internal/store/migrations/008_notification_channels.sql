ALTER TABLE notifications
    ADD COLUMN IF NOT EXISTS in_app boolean NOT NULL DEFAULT true;

CREATE INDEX IF NOT EXISTS notifications_in_app_unread_idx
    ON notifications(user_id,created_at DESC)
    WHERE in_app AND read_at IS NULL;
