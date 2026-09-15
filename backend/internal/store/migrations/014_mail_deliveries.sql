-- Every mail attempt is recorded so an administrator can answer "it never
-- arrived" without guessing: when, which event, to whom, which subject, and
-- whether it went out. The body is deliberately not stored — with it the log
-- would be a second copy of everything that left the building.
--
-- One row per message. A retried delivery updates the same row (attempts,
-- status, last error) instead of adding one, so the list stays readable.
CREATE TABLE IF NOT EXISTS mail_deliveries (
    id text PRIMARY KEY,
    event text NOT NULL,
    user_id text NOT NULL DEFAULT '',
    actor_id text NOT NULL DEFAULT '',
    recipient text NOT NULL,
    subject text NOT NULL,
    status text NOT NULL CHECK (status IN ('queued','sent','failed')),
    attempts integer NOT NULL DEFAULT 0,
    error_message text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS mail_deliveries_created_idx
    ON mail_deliveries(created_at DESC);
