package store

import (
	"strings"
	"testing"
)

func TestInitialMigrationContainsSecurityAndDomainInvariants(t *testing.T) {
	body, err := migrationFiles.ReadFile("migrations/001_initial.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	required := []string{
		"CREATE TABLE IF NOT EXISTS users", "CREATE TABLE IF NOT EXISTS sessions",
		"csrf_hash text NOT NULL", "CREATE TABLE IF NOT EXISTS api_keys", "last_used_at timestamptz",
		"CREATE TABLE IF NOT EXISTS audit_events", "CREATE TABLE IF NOT EXISTS posts",
		"CREATE TABLE IF NOT EXISTS approval_requests", "snapshot bytea NOT NULL",
		"CREATE TABLE IF NOT EXISTS notifications", "CREATE TABLE IF NOT EXISTS ai_usage_events",
		"('member','mcp:use')", "('super_admin','*')",
	}
	for _, fragment := range required {
		if !strings.Contains(sql, fragment) {
			t.Errorf("migration invariant missing: %s", fragment)
		}
	}
}

func TestNotificationChannelMigrationSeparatesInAppVisibility(t *testing.T) {
	body, err := migrationFiles.ReadFile("migrations/008_notification_channels.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, fragment := range []string{
		"ADD COLUMN IF NOT EXISTS in_app boolean NOT NULL DEFAULT true",
		"notifications_in_app_unread_idx",
		"WHERE in_app AND read_at IS NULL",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("008 migration invariant missing: %s", fragment)
		}
	}
}

func TestNotificationDeliveryMigrationSupportsDigestCursor(t *testing.T) {
	body, err := migrationFiles.ReadFile("migrations/009_notification_delivery_time.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, fragment := range []string{
		"ADD COLUMN IF NOT EXISTS delivered_at timestamptz NOT NULL DEFAULT now()",
		"notifications_digest_delivery_idx",
		"ON notifications(user_id,delivered_at)",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("009 migration invariant missing: %s", fragment)
		}
	}
}

func TestNotificationDigestMarkerMigrationPreventsCommitRaces(t *testing.T) {
	body, err := migrationFiles.ReadFile("migrations/010_notification_digest_marker.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, fragment := range []string{
		"ADD COLUMN IF NOT EXISTS digested_at timestamptz",
		"notifications_digest_pending_idx",
		"WHERE digested_at IS NULL AND type <> 'digest'",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("010 migration invariant missing: %s", fragment)
		}
	}
}

func TestNotificationDigestConfigMigrationTracksSubscriptionTransitions(t *testing.T) {
	body, err := migrationFiles.ReadFile("migrations/011_notification_digest_config.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, fragment := range []string{
		"ADD COLUMN IF NOT EXISTS config_signature text NOT NULL DEFAULT ''",
		"COALESCE(preferences.payload#>>'{notifications,digest,mode}', 'off')",
		"WHEN 'hourly' THEN 'hourly'",
		"WHEN 'daily' THEN 'daily@' || COALESCE(preferences.payload#>>'{notifications,digest,time}', '08:00')",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("011 migration invariant missing: %s", fragment)
		}
	}
}

func TestNotificationEmailMigrationTracksDelivery(t *testing.T) {
	body, err := migrationFiles.ReadFile("migrations/012_notification_email.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"ADD COLUMN IF NOT EXISTS emailed_at", "notifications_email_delivery_idx"} {
		if !strings.Contains(string(body), fragment) {
			t.Fatalf("notification email migration missing %q", fragment)
		}
	}
}

func TestSearchCursorMigrationContainsOfflineIndexesAndMediaAlt(t *testing.T) {
	body, err := migrationFiles.ReadFile("migrations/002_search_cursor.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	required := []string{
		"CREATE EXTENSION IF NOT EXISTS pg_trgm",
		"users_username_trgm_idx", "topics_slug_trgm_idx", "posts_content_trgm_idx",
		"posts_published_cursor_idx", "published_at DESC, id DESC",
		"ADD COLUMN IF NOT EXISTS alt_text", "char_length(alt_text) <= 500",
	}
	for _, fragment := range required {
		if !strings.Contains(sql, fragment) {
			t.Errorf("002 migration invariant missing: %s", fragment)
		}
	}
}

func TestPostMediaAltMigrationBackfillsContextualDescriptions(t *testing.T) {
	body, err := migrationFiles.ReadFile("migrations/005_post_media_alt_text.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	required := []string{
		"ALTER TABLE post_media", "ADD COLUMN IF NOT EXISTS alt_text text NOT NULL DEFAULT ''",
		"FROM media_assets AS asset", "asset.id = association.media_id",
		"post_media_alt_text_length_check", "char_length(alt_text) <= 500",
	}
	for _, fragment := range required {
		if !strings.Contains(sql, fragment) {
			t.Errorf("005 migration invariant missing: %s", fragment)
		}
	}
}
