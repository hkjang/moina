package store

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

var (
	ErrMigrationChecksumMismatch = errors.New("migration checksum mismatch")
	ErrMigrationNewerSchema      = errors.New("database schema is newer than this binary")
)

type migration struct {
	version  string
	body     string
	checksum string
}

func loadMigrations() ([]migration, error) {
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("migration 목록 읽기: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	loaded := make([]migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		body, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return nil, fmt.Errorf("migration %s 읽기: %w", entry.Name(), err)
		}
		digest := sha256.Sum256(body)
		loaded = append(loaded, migration{
			version:  entry.Name(),
			body:     string(body),
			checksum: hex.EncodeToString(digest[:]),
		})
	}
	return loaded, nil
}

// validateAppliedChecksums accepts empty checksums only for databases created by
// releases predating checksum persistence. The caller backfills those values in the
// same advisory-locked transaction before applying newer migrations.
func validateAppliedChecksums(applied map[string]string, migrations []migration) (map[string]string, error) {
	backfill := make(map[string]string)
	known := make(map[string]struct{}, len(migrations))
	for _, item := range migrations {
		known[item.version] = struct{}{}
		stored, exists := applied[item.version]
		if !exists {
			continue
		}
		if stored == "" {
			backfill[item.version] = item.checksum
			continue
		}
		if !strings.EqualFold(stored, item.checksum) {
			return nil, fmt.Errorf("%w: %s (database=%s binary=%s)", ErrMigrationChecksumMismatch, item.version, stored, item.checksum)
		}
	}
	for version := range applied {
		if _, exists := known[version]; !exists {
			return nil, fmt.Errorf("%w: %s", ErrMigrationNewerSchema, version)
		}
	}
	return backfill, nil
}

func (s *Store) migrate(ctx context.Context) error {
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(1297042026)`); err != nil {
		return fmt.Errorf("migration 잠금: %w", err)
	}
	if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations(version text PRIMARY KEY,applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return fmt.Errorf("migration 이력 테이블 생성: %w", err)
	}
	if _, err := tx.Exec(ctx, `ALTER TABLE schema_migrations ADD COLUMN IF NOT EXISTS checksum text`); err != nil {
		return fmt.Errorf("migration checksum 컬럼 생성: %w", err)
	}

	rows, err := tx.Query(ctx, `SELECT version,COALESCE(checksum,'') FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("migration 이력 읽기: %w", err)
	}
	applied := make(map[string]string)
	for rows.Next() {
		var version, checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			rows.Close()
			return fmt.Errorf("migration 이력 해석: %w", err)
		}
		applied[version] = checksum
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("migration 이력 순회: %w", err)
	}
	rows.Close()

	backfill, err := validateAppliedChecksums(applied, migrations)
	if err != nil {
		return err
	}
	for version, checksum := range backfill {
		if _, err := tx.Exec(ctx, `UPDATE schema_migrations SET checksum=$2 WHERE version=$1 AND NULLIF(checksum,'') IS NULL`, version, checksum); err != nil {
			return fmt.Errorf("migration %s checksum 백필: %w", version, err)
		}
		applied[version] = checksum
	}

	for _, item := range migrations {
		if _, exists := applied[item.version]; exists {
			continue
		}
		if _, err := tx.Exec(ctx, item.body, pgx.QueryExecModeSimpleProtocol); err != nil {
			return fmt.Errorf("migration %s: %w", item.version, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations(version,checksum) VALUES($1,$2)`, item.version, item.checksum); err != nil {
			return fmt.Errorf("migration %s 이력 저장: %w", item.version, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("migration commit: %w", err)
	}
	return nil
}
