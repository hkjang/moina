package store

import (
	"strings"
	"testing"
)

func TestNetworkRateMigrationDefinesSharedBucket(t *testing.T) {
	body, err := migrationFiles.ReadFile("migrations/006_network_rate_limits.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, fragment := range []string{"rate_limit_buckets", "PRIMARY KEY(key_hash, window_seconds)", "expires_at"} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("migration missing %q", fragment)
		}
	}
}
