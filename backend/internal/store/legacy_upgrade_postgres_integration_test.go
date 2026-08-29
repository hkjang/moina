package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const v010InitialMigrationChecksum = "915529410d79d5c370c1f6814bf8b9e80e4aa4d259ad79e6cb3c06a149f92492"

func TestPostgreSQLV010LegacyUpgradeInIsolatedSchema(t *testing.T) {
	dsn := os.Getenv("MOINA_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("MOINA_TEST_POSTGRES_DSN is not set")
	}

	ctx := t.Context()
	adminConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	adminConfig.MinConns = 0
	adminConfig.MaxConns = 2
	adminPool, err := pgxpool.NewWithConfig(ctx, adminConfig)
	if err != nil {
		t.Fatal(err)
	}

	suffix := time.Now().UnixNano()
	legacySchema := fmt.Sprintf("moina_v010_upgrade_%d", suffix)
	freshSchema := fmt.Sprintf("moina_v011_fresh_%d", suffix)
	var legacyPool, freshPool *pgxpool.Pool
	t.Cleanup(func() {
		if legacyPool != nil {
			legacyPool.Close()
		}
		if freshPool != nil {
			freshPool.Close()
		}
		cleanupContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = adminPool.Exec(cleanupContext, "DROP SCHEMA IF EXISTS "+pgx.Identifier{legacySchema}.Sanitize()+" CASCADE")
		_, _ = adminPool.Exec(cleanupContext, "DROP SCHEMA IF EXISTS "+pgx.Identifier{freshSchema}.Sanitize()+" CASCADE")
		adminPool.Close()
	})

	installTrigramExtensionInPublic(t, ctx, adminPool)
	for _, schema := range []string{legacySchema, freshSchema} {
		if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
			t.Fatal(err)
		}
	}
	legacyPool = openSchemaPool(t, ctx, dsn, legacySchema)
	freshPool = openSchemaPool(t, ctx, dsn, freshSchema)

	initialBody, err := migrationFiles.ReadFile("migrations/001_initial.sql")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(initialBody)
	if checksum := hex.EncodeToString(digest[:]); checksum != v010InitialMigrationChecksum {
		t.Fatalf("001_initial.sql is not the immutable v0.1.0 migration: %s", checksum)
	}

	legacyTransaction, err := legacyPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer legacyTransaction.Rollback(ctx)
	if _, err := legacyTransaction.Exec(ctx, string(initialBody), pgx.QueryExecModeSimpleProtocol); err != nil {
		t.Fatal(err)
	}
	if _, err := legacyTransaction.Exec(ctx, `CREATE TABLE schema_migrations(version text PRIMARY KEY,applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	if _, err := legacyTransaction.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES('001_initial.sql')`); err != nil {
		t.Fatal(err)
	}
	if _, err := legacyTransaction.Exec(ctx, `INSERT INTO outbox_events(id,event_type,aggregate_id,payload) VALUES('legacy_event_null_key','notification.create','legacy_user','{"userId":"legacy_user"}'::jsonb)`); err != nil {
		t.Fatal(err)
	}
	if err := legacyTransaction.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	assertColumnPresence(t, ctx, legacyPool, legacySchema, "schema_migrations", "checksum", false)
	assertColumnPresence(t, ctx, legacyPool, legacySchema, "outbox_events", "idempotency_key", false)

	legacyStore := &Store{pool: legacyPool}
	if err := legacyStore.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	// Opening the upgraded schema again must be a checksum-verified no-op.
	if err := legacyStore.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	freshStore := &Store{pool: freshPool}
	if err := freshStore.migrate(ctx); err != nil {
		t.Fatal(err)
	}

	assertMigrationChecksums(t, ctx, legacyPool)
	assertMigrationChecksums(t, ctx, freshPool)

	var idempotencyKey string
	var attempts, maxAttempts int
	if err := legacyPool.QueryRow(ctx, `SELECT idempotency_key,attempts,max_attempts FROM outbox_events WHERE id='legacy_event_null_key'`).Scan(&idempotencyKey, &attempts, &maxAttempts); err != nil {
		t.Fatal(err)
	}
	if idempotencyKey != "legacy:legacy_event_null_key" || attempts != 0 || maxAttempts != 8 {
		t.Fatalf("legacy outbox backfill = %q attempts=%d/%d", idempotencyKey, attempts, maxAttempts)
	}
	assertColumnPresence(t, ctx, legacyPool, legacySchema, "outbox_events", "idempotency_key", true)
	var nullable string
	if err := legacyPool.QueryRow(ctx, `SELECT is_nullable FROM information_schema.columns WHERE table_schema=$1 AND table_name='outbox_events' AND column_name='idempotency_key'`, legacySchema).Scan(&nullable); err != nil {
		t.Fatal(err)
	}
	if nullable != "NO" {
		t.Fatalf("outbox idempotency_key nullable = %s", nullable)
	}
	if _, err := legacyPool.Exec(ctx, `INSERT INTO outbox_events(id,event_type,aggregate_id,idempotency_key) VALUES('legacy_null_rejected','notification.create','legacy_user',NULL)`); err == nil {
		t.Fatal("upgraded outbox accepted a NULL idempotency key")
	}

	var permissionCount int
	if err := legacyPool.QueryRow(ctx, `SELECT count(*) FROM role_permissions WHERE role_name='admin' AND permission='outbox:manage'`).Scan(&permissionCount); err != nil {
		t.Fatal(err)
	}
	if permissionCount != 1 {
		t.Fatalf("admin outbox:manage permission count = %d", permissionCount)
	}

	for _, required := range []struct {
		table  string
		column string
	}{
		{"media_assets", "alt_text"},
		{"media_assets", "large_object_oid"},
		{"outbox_events", "dead_lettered_at"},
		{"feed_snapshots", "preferences"},
		{"feed_snapshots", "reuse_bucket"},
		{"feed_snapshot_items", "recency_score"},
		{"feed_snapshot_items", "followed_topics"},
	} {
		assertColumnPresence(t, ctx, legacyPool, legacySchema, required.table, required.column, true)
	}
	for _, relation := range []string{
		"users_username_trgm_idx", "posts_published_cursor_idx", "outbox_attempts",
		"outbox_ready_idx", "feed_snapshots_reuse_idx", "feed_snapshot_items_page_idx",
	} {
		assertRelationInSchema(t, ctx, legacyPool, legacySchema, relation)
	}

	legacyFingerprint := schemaFingerprint(t, ctx, legacyPool, legacySchema)
	freshFingerprint := schemaFingerprint(t, ctx, freshPool, freshSchema)
	if !slices.Equal(legacyFingerprint, freshFingerprint) {
		t.Fatalf("upgraded schema differs from fresh schema\nupgrade-only: %v\nfresh-only: %v", difference(legacyFingerprint, freshFingerprint), difference(freshFingerprint, legacyFingerprint))
	}
}

func installTrigramExtensionInPublic(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(1297042026)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var extensionSchema string
	if err := pool.QueryRow(ctx, `SELECT namespace.nspname FROM pg_extension extension JOIN pg_namespace namespace ON namespace.oid=extension.extnamespace WHERE extension.extname='pg_trgm'`).Scan(&extensionSchema); err != nil {
		t.Fatal(err)
	}
	if extensionSchema != "public" {
		t.Fatalf("pg_trgm schema = %q, want public", extensionSchema)
	}
}

func openSchemaPool(t *testing.T, ctx context.Context, dsn, schema string) *pgxpool.Pool {
	t.Helper()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.MinConns = 0
	config.MaxConns = 4
	config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	var currentSchema string
	if err := pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&currentSchema); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	if currentSchema != schema {
		pool.Close()
		t.Fatalf("current schema = %q, want %q", currentSchema, schema)
	}
	return pool
}

func assertMigrationChecksums(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	want, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := pool.Query(ctx, `SELECT version,checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := make(map[string]string, len(want))
	for rows.Next() {
		var version, checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			t.Fatal(err)
		}
		got[version] = checksum
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("applied migration count = %d, want %d", len(got), len(want))
	}
	for _, migration := range want {
		if got[migration.version] != migration.checksum {
			t.Fatalf("migration %s checksum = %q, want %q", migration.version, got[migration.version], migration.checksum)
		}
	}
}

func assertColumnPresence(t *testing.T, ctx context.Context, pool *pgxpool.Pool, schema, table, column string, want bool) {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=$1 AND table_name=$2 AND column_name=$3)`, schema, table, column).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists != want {
		t.Fatalf("column %s.%s.%s exists = %v, want %v", schema, table, column, exists, want)
	}
}

func assertRelationInSchema(t *testing.T, ctx context.Context, pool *pgxpool.Pool, schema, relation string) {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_class relation JOIN pg_namespace namespace ON namespace.oid=relation.relnamespace WHERE namespace.nspname=$1 AND relation.relname=$2)`, schema, relation).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatalf("relation %s.%s is missing", schema, relation)
	}
}

func schemaFingerprint(t *testing.T, ctx context.Context, pool *pgxpool.Pool, schema string) []string {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT kind,value FROM (
			SELECT 'table' AS kind,table_name||'|'||table_type AS value
			FROM information_schema.tables WHERE table_schema=$1
			UNION ALL
			SELECT 'column',table_name||'|'||lpad(ordinal_position::text,4,'0')||'|'||column_name||'|'||data_type||'|'||udt_name||'|'||is_nullable||'|'||COALESCE(column_default,'')
			FROM information_schema.columns WHERE table_schema=$1
			UNION ALL
			SELECT 'constraint',relation.relname||'|'||constraint_value.conname||'|'||constraint_value.contype::text||'|'||pg_get_constraintdef(constraint_value.oid,true)
			FROM pg_constraint constraint_value
			JOIN pg_class relation ON relation.oid=constraint_value.conrelid
			JOIN pg_namespace namespace ON namespace.oid=relation.relnamespace
			WHERE namespace.nspname=$1
			UNION ALL
			SELECT 'index',tablename||'|'||indexname||'|'||indexdef
			FROM pg_indexes WHERE schemaname=$1
			UNION ALL
			SELECT 'trigger',event_object_table||'|'||trigger_name||'|'||event_manipulation||'|'||action_timing||'|'||action_statement
			FROM information_schema.triggers WHERE trigger_schema=$1
			UNION ALL
			SELECT 'function',procedure_value.proname||'|'||pg_get_function_identity_arguments(procedure_value.oid)||'|'||pg_get_functiondef(procedure_value.oid)
			FROM pg_proc procedure_value JOIN pg_namespace namespace ON namespace.oid=procedure_value.pronamespace
			WHERE namespace.nspname=$1
			UNION ALL
			SELECT 'sequence',sequence_name||'|'||data_type||'|'||start_value||'|'||minimum_value||'|'||maximum_value||'|'||increment
			FROM information_schema.sequences WHERE sequence_schema=$1
		) fingerprint ORDER BY kind,value`, schema)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	fingerprint := make([]string, 0)
	for rows.Next() {
		var kind, value string
		if err := rows.Scan(&kind, &value); err != nil {
			t.Fatal(err)
		}
		value = strings.ReplaceAll(value, `"`+schema+`".`, "<schema>.")
		value = strings.ReplaceAll(value, schema+".", "<schema>.")
		fingerprint = append(fingerprint, kind+"|"+value)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return fingerprint
}

func difference(left, right []string) []string {
	result := make([]string, 0)
	for _, value := range left {
		if !slices.Contains(right, value) {
			result = append(result, value)
		}
	}
	return result
}
