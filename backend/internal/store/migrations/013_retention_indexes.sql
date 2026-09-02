-- Retention sweeps delete by age across a whole table, which the existing
-- per-user and pending-only indexes cannot serve. These two make the hourly
-- purge an index range scan instead of a sequential scan.

CREATE INDEX IF NOT EXISTS notifications_created_idx
    ON notifications(created_at);

CREATE INDEX IF NOT EXISTS outbox_delivered_idx
    ON outbox_events(delivered_at)
    WHERE delivered_at IS NOT NULL;
