CREATE TABLE IF NOT EXISTS feed_snapshots (
    id text PRIMARY KEY,
    user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    ranking_version text NOT NULL,
	preference_hash text NOT NULL,
	preferences jsonb NOT NULL,
	reuse_bucket bigint NOT NULL,
    as_of timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK(expires_at > as_of)
);

CREATE UNIQUE INDEX IF NOT EXISTS feed_snapshots_reuse_idx
	ON feed_snapshots(user_id,ranking_version,preference_hash,reuse_bucket);

CREATE INDEX IF NOT EXISTS feed_snapshots_user_idx
    ON feed_snapshots(user_id,created_at DESC);
CREATE INDEX IF NOT EXISTS feed_snapshots_expiry_idx
    ON feed_snapshots(expires_at);

CREATE TABLE IF NOT EXISTS feed_snapshot_items (
    snapshot_id text NOT NULL REFERENCES feed_snapshots(id) ON DELETE CASCADE,
    post_id text NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
    score double precision NOT NULL,
	link_score double precision NOT NULL,
	topic_score double precision NOT NULL,
	discovery_score double precision NOT NULL,
	recency_score double precision NOT NULL,
	followed_topics integer NOT NULL,
    PRIMARY KEY(snapshot_id,post_id),
	CHECK(score NOT IN ('Infinity'::float8,'-Infinity'::float8) AND score <> 'NaN'::float8),
	CHECK(link_score NOT IN ('Infinity'::float8,'-Infinity'::float8) AND link_score <> 'NaN'::float8),
	CHECK(topic_score NOT IN ('Infinity'::float8,'-Infinity'::float8) AND topic_score <> 'NaN'::float8),
	CHECK(discovery_score NOT IN ('Infinity'::float8,'-Infinity'::float8) AND discovery_score <> 'NaN'::float8),
	CHECK(recency_score NOT IN ('Infinity'::float8,'-Infinity'::float8) AND recency_score <> 'NaN'::float8),
	CHECK(followed_topics >= 0)
);

CREATE INDEX IF NOT EXISTS feed_snapshot_items_page_idx
    ON feed_snapshot_items(snapshot_id,score DESC,post_id DESC);

INSERT INTO role_permissions(role_name,permission)
VALUES ('admin','outbox:manage')
ON CONFLICT(role_name,permission) DO NOTHING;
