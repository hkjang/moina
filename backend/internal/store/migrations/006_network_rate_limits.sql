CREATE TABLE IF NOT EXISTS rate_limit_buckets (
    key_hash text NOT NULL,
    window_seconds integer NOT NULL CHECK(window_seconds BETWEEN 1 AND 86400),
    request_count integer NOT NULL DEFAULT 1 CHECK(request_count >= 0),
    started_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    PRIMARY KEY(key_hash, window_seconds)
);

CREATE INDEX IF NOT EXISTS rate_limit_buckets_expiry_idx
    ON rate_limit_buckets(expires_at);
