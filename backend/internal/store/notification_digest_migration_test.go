package store

import (
	"strings"
	"testing"
)

func TestNotificationDigestMigrationDefinesDurableScheduleState(t *testing.T) {
	body, err := migrationFiles.ReadFile("migrations/007_notification_digests.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, fragment := range []string{"notification_digest_state", "user_id text PRIMARY KEY", "last_sent_at timestamptz NOT NULL"} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("migration missing %q", fragment)
		}
	}
}
