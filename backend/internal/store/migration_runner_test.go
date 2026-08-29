package store

import (
	"errors"
	"strings"
	"testing"
)

func TestLoadMigrationsSortedAndChecksummed(t *testing.T) {
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) < 2 {
		t.Fatalf("expected at least initial and outbox migrations, got %d", len(migrations))
	}
	for index, item := range migrations {
		if len(item.checksum) != 64 {
			t.Errorf("%s checksum length = %d", item.version, len(item.checksum))
		}
		if index > 0 && migrations[index-1].version >= item.version {
			t.Errorf("migrations not sorted: %s before %s", migrations[index-1].version, item.version)
		}
	}
}

func TestValidateAppliedChecksumsBackfillsLegacyRows(t *testing.T) {
	migrations := []migration{{version: "001.sql", checksum: strings.Repeat("a", 64)}}
	backfill, err := validateAppliedChecksums(map[string]string{"001.sql": ""}, migrations)
	if err != nil {
		t.Fatal(err)
	}
	if backfill["001.sql"] != migrations[0].checksum {
		t.Fatalf("unexpected backfill: %#v", backfill)
	}
}

func TestValidateAppliedChecksumsRejectsMutation(t *testing.T) {
	migrations := []migration{{version: "001.sql", checksum: strings.Repeat("a", 64)}}
	_, err := validateAppliedChecksums(map[string]string{"001.sql": strings.Repeat("b", 64)}, migrations)
	if !errors.Is(err, ErrMigrationChecksumMismatch) {
		t.Fatalf("expected checksum mismatch, got %v", err)
	}
}

func TestValidateAppliedChecksumsRejectsNewerDatabaseSchema(t *testing.T) {
	migrations := []migration{{version: "001.sql", checksum: strings.Repeat("a", 64)}}
	_, err := validateAppliedChecksums(map[string]string{
		"001.sql": strings.Repeat("a", 64),
		"002.sql": strings.Repeat("b", 64),
	}, migrations)
	if !errors.Is(err, ErrMigrationNewerSchema) {
		t.Fatalf("expected newer-schema rejection, got %v", err)
	}
}

func TestOutboxMigrationContainsReliabilityPrimitives(t *testing.T) {
	body, err := migrationFiles.ReadFile("migrations/003_outbox_observability.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, fragment := range []string{
		"idempotency_key text", "attempts integer", "max_attempts integer",
		"dead_lettered_at timestamptz", "outbox_idempotency_uidx",
		"outbox_ready_idx", "CREATE TABLE IF NOT EXISTS outbox_attempts", "pg_notify('moina_outbox'", "media_orphan_cleanup_idx",
		"large_object_oid oid", "ALTER COLUMN data DROP NOT NULL", "lo_unlink(OLD.large_object_oid)",
		"CONSTRAINT media_payload_present", "media_large_object_oid_uidx",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("003 migration missing %q", fragment)
		}
	}
}
