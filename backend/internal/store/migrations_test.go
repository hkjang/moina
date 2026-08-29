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
