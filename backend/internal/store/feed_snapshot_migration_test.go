package store

import (
	"strings"
	"testing"
)

func TestFeedSnapshotMigrationContainsPagingAndPermissionInvariants(t *testing.T) {
	body, err := migrationFiles.ReadFile("migrations/004_feed_snapshots.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, fragment := range []string{
		"CREATE TABLE IF NOT EXISTS feed_snapshots",
		"user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE",
		"preference_hash text NOT NULL",
		"preferences jsonb NOT NULL",
		"reuse_bucket bigint NOT NULL",
		"CHECK(expires_at > as_of)",
		"feed_snapshots_reuse_idx",
		"ON feed_snapshots(user_id,ranking_version,preference_hash,reuse_bucket)",
		"CREATE TABLE IF NOT EXISTS feed_snapshot_items",
		"PRIMARY KEY(snapshot_id,post_id)",
		"CHECK(score NOT IN ('Infinity'::float8,'-Infinity'::float8) AND score <> 'NaN'::float8)",
		"link_score double precision NOT NULL",
		"topic_score double precision NOT NULL",
		"discovery_score double precision NOT NULL",
		"recency_score double precision NOT NULL",
		"followed_topics integer NOT NULL",
		"CHECK(link_score NOT IN ('Infinity'::float8,'-Infinity'::float8) AND link_score <> 'NaN'::float8)",
		"CHECK(topic_score NOT IN ('Infinity'::float8,'-Infinity'::float8) AND topic_score <> 'NaN'::float8)",
		"CHECK(discovery_score NOT IN ('Infinity'::float8,'-Infinity'::float8) AND discovery_score <> 'NaN'::float8)",
		"CHECK(recency_score NOT IN ('Infinity'::float8,'-Infinity'::float8) AND recency_score <> 'NaN'::float8)",
		"CHECK(followed_topics >= 0)",
		"ON feed_snapshot_items(snapshot_id,score DESC,post_id DESC)",
		"VALUES ('admin','outbox:manage')",
		"ON CONFLICT(role_name,permission) DO NOTHING",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("004 migration invariant missing: %s", fragment)
		}
	}
}
