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
