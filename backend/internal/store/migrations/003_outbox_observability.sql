ALTER TABLE schema_migrations
    ADD COLUMN IF NOT EXISTS checksum text;

ALTER TABLE outbox_events
    ADD COLUMN IF NOT EXISTS idempotency_key text,
    ADD COLUMN IF NOT EXISTS attempts integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS max_attempts integer NOT NULL DEFAULT 8,
    ADD COLUMN IF NOT EXISTS available_at timestamptz NOT NULL DEFAULT now(),
    ADD COLUMN IF NOT EXISTS last_attempt_at timestamptz,
    ADD COLUMN IF NOT EXISTS last_error text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS locked_at timestamptz,
    ADD COLUMN IF NOT EXISTS locked_by text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS dead_lettered_at timestamptz;

-- v0.1.0 could leave undelivered rows without an idempotency key. Backfill
-- before workers start so Claim can always scan a concrete string and a retry
-- cannot create a duplicate notification for the legacy event.
UPDATE outbox_events
SET idempotency_key='legacy:' || id
WHERE idempotency_key IS NULL;

ALTER TABLE outbox_events
    ALTER COLUMN idempotency_key SET NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS outbox_idempotency_uidx
    ON outbox_events(idempotency_key)
    WHERE idempotency_key IS NOT NULL;

CREATE INDEX IF NOT EXISTS outbox_ready_idx
    ON outbox_events(available_at,created_at,id)
    WHERE delivered_at IS NULL AND dead_lettered_at IS NULL;

CREATE INDEX IF NOT EXISTS outbox_dead_letter_idx
    ON outbox_events(dead_lettered_at DESC,id)
    WHERE dead_lettered_at IS NOT NULL;

CREATE TABLE IF NOT EXISTS outbox_attempts (
    id bigserial PRIMARY KEY,
    event_id text NOT NULL REFERENCES outbox_events(id) ON DELETE CASCADE,
    attempt integer NOT NULL,
    worker_id text NOT NULL,
    error text NOT NULL,
    failed_at timestamptz NOT NULL,
    next_available_at timestamptz NOT NULL,
    dead_lettered boolean NOT NULL DEFAULT false
);
CREATE INDEX IF NOT EXISTS outbox_attempts_event_idx
    ON outbox_attempts(event_id,attempt DESC);
CREATE INDEX IF NOT EXISTS outbox_attempts_failed_idx
    ON outbox_attempts(failed_at DESC);

CREATE INDEX IF NOT EXISTS media_orphan_cleanup_idx
    ON media_assets(created_at,id);

ALTER TABLE media_assets
    ADD COLUMN IF NOT EXISTS large_object_oid oid;

ALTER TABLE media_assets
    ALTER COLUMN data DROP NOT NULL;

ALTER TABLE media_assets
    ADD CONSTRAINT media_payload_present
    CHECK (data IS NOT NULL OR large_object_oid IS NOT NULL);

CREATE UNIQUE INDEX IF NOT EXISTS media_large_object_oid_uidx
    ON media_assets(large_object_oid)
    WHERE large_object_oid IS NOT NULL;

CREATE OR REPLACE FUNCTION moina_unlink_media_large_object()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.large_object_oid IS NOT NULL THEN
        PERFORM lo_unlink(OLD.large_object_oid);
    END IF;
    RETURN OLD;
END;
$$;

DROP TRIGGER IF EXISTS media_large_object_unlink_trigger ON media_assets;
CREATE TRIGGER media_large_object_unlink_trigger
BEFORE DELETE ON media_assets
FOR EACH ROW EXECUTE FUNCTION moina_unlink_media_large_object();

CREATE OR REPLACE FUNCTION moina_notify_outbox_event()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.delivered_at IS NULL AND NEW.dead_lettered_at IS NULL THEN
        PERFORM pg_notify('moina_outbox', NEW.id);
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS outbox_notify_trigger ON outbox_events;
CREATE TRIGGER outbox_notify_trigger
AFTER INSERT OR UPDATE OF available_at,dead_lettered_at ON outbox_events
FOR EACH ROW EXECUTE FUNCTION moina_notify_outbox_event();
