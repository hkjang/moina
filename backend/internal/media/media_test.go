package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type rowStub struct {
	values []any
	err    error
}

func (row rowStub) Scan(dest ...any) error {
	if row.err != nil {
		return row.err
	}
	if len(dest) != len(row.values) {
		return errors.New("scan arity mismatch")
	}
	for index, value := range row.values {
		switch pointer := dest[index].(type) {
		case *string:
			*pointer = value.(string)
		case *int:
			*pointer = value.(int)
		case *int64:
			*pointer = value.(int64)
		case *time.Time:
			*pointer = value.(time.Time)
		case *[]byte:
			*pointer = append((*pointer)[:0], value.([]byte)...)
		default:
			return errors.New("unsupported scan type")
		}
	}
	return nil
}

type dbStub struct {
	execSQL  string
	execArgs []any
	rowSQL   string
	rowArgs  []any
	row      pgx.Row
}

func (db *dbStub) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	db.execSQL, db.execArgs = sql, args
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}
func (db *dbStub) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	db.rowSQL, db.rowArgs = sql, args
	return db.row
}

func TestStreamAndHashUsesDeclaredBound(t *testing.T) {
	body := []byte("moina-media")
	var destination bytes.Buffer
	digestValue, err := streamAndHash(&destination, bytes.NewReader(body), int64(len(body)), 1024)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	if digestValue != hex.EncodeToString(digest[:]) {
		t.Fatalf("sha256 = %s", digestValue)
	}
	if !bytes.Equal(destination.Bytes(), body) {
		t.Fatalf("streamed body = %q", destination.Bytes())
	}
}

func TestStreamAndHashRejectsSizeMismatch(t *testing.T) {
	_, err := streamAndHash(&bytes.Buffer{}, strings.NewReader("too long"), 2, 1024)
	if !errors.Is(err, ErrSizeMismatch) {
		t.Fatalf("expected size mismatch, got %v", err)
	}
}

func TestMediaAccessSQLPreservesLegacyAuthorization(t *testing.T) {
	for _, fragment := range []string{"large_object_oid", "post_media", "avatar_user", "moim_members", "follows"} {
		if !strings.Contains(mediaAccessSQL, fragment) {
			t.Errorf("authorization query missing %q", fragment)
		}
	}
}

func TestDeleteOrphansUsesTTLReferencesAndSkipLocked(t *testing.T) {
	db := &dbStub{row: rowStub{values: []any{int64(2), int64(4096)}}}
	store := newPostgreSQLStoreForTest(db, 1024)
	result, err := store.DeleteOrphans(context.Background(), time.Now().Add(-24*time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	if result.Deleted != 2 || result.DeletedBytes != 4096 {
		t.Fatalf("result = %#v", result)
	}
	for _, fragment := range []string{"post_media", "avatar_user.avatar_id", "FOR UPDATE OF asset SKIP LOCKED", "LIMIT $2"} {
		if !strings.Contains(orphanCleanupSQL, fragment) {
			t.Errorf("cleanup SQL missing %q", fragment)
		}
	}
}

type orphanStoreStub struct {
	cutoff time.Time
	batch  int
}

func (store *orphanStoreStub) DeleteOrphans(_ context.Context, cutoff time.Time, batch int) (CleanupResult, error) {
	store.cutoff, store.batch = cutoff, batch
	return CleanupResult{Deleted: 1}, nil
}

func TestCleanerRunOnceUsesTTL(t *testing.T) {
	backend := &orphanStoreStub{}
	cleaner := &Cleaner{Store: backend, TTL: 24 * time.Hour, Interval: time.Hour, Batch: 50}
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	if _, err := cleaner.RunOnce(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if !backend.cutoff.Equal(now.Add(-24*time.Hour)) || backend.batch != 50 {
		t.Fatalf("cutoff=%s batch=%d", backend.cutoff, backend.batch)
	}
}
