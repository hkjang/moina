ALTER TABLE notifications
    ADD COLUMN IF NOT EXISTS digested_at timestamptz;

CREATE INDEX IF NOT EXISTS notifications_digest_pending_idx
    ON notifications(user_id,delivered_at,id)
    WHERE digested_at IS NULL AND type <> 'digest';
