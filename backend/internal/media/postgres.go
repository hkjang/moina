package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	DefaultMaxObjectBytes       int64 = 256 << 20
	MaxUnattachedObjects              = 100
	MaxUnattachedBytes          int64 = 512 << 20
	defaultConcurrentMediaReads       = 8
)

const mediaAccessSQL = `SELECT m.id,m.owner_id,m.filename,m.alt_text,m.mime_type,m.size_bytes,m.sha256,m.width,m.height,m.created_at,m.large_object_oid,m.data
FROM media_assets m
WHERE m.id=$1 AND (
    m.owner_id=$2
    OR EXISTS (
        SELECT 1 FROM users avatar_user
        WHERE avatar_user.avatar_id=m.id AND avatar_user.active
          AND NOT EXISTS (
            SELECT 1 FROM blocks b
            WHERE (b.blocker_id=$2 AND b.blocked_id=avatar_user.id)
               OR (b.blocker_id=avatar_user.id AND b.blocked_id=$2)
          )
    )
    OR EXISTS (
        SELECT 1 FROM post_media pm JOIN posts p ON p.id=pm.post_id
        WHERE pm.media_id=m.id AND p.status='published'
          AND NOT EXISTS (
            SELECT 1 FROM blocks b
            WHERE (b.blocker_id=$2 AND b.blocked_id=p.author_id)
               OR (b.blocker_id=p.author_id AND b.blocked_id=$2)
          ) AND (
            p.visibility='public' OR p.author_id=$2
            OR (p.visibility='followers' AND EXISTS (
                SELECT 1 FROM follows f WHERE f.follower_id=$2 AND f.followee_id=p.author_id
            ))
            OR (p.visibility='moim' AND EXISTS (
                SELECT 1 FROM moim_members mm WHERE mm.moim_id=p.moim_id AND mm.user_id=$2
            ))
        )
    )
)`

const orphanCleanupSQL = `WITH victims AS (
    SELECT asset.id
    FROM media_assets asset
    WHERE asset.created_at < $1
      AND NOT EXISTS (SELECT 1 FROM post_media linked WHERE linked.media_id=asset.id)
      AND NOT EXISTS (SELECT 1 FROM users avatar_user WHERE avatar_user.avatar_id=asset.id)
    ORDER BY asset.created_at,asset.id
    FOR UPDATE OF asset SKIP LOCKED
    LIMIT $2
), deleted AS (
    DELETE FROM media_assets asset USING victims
    WHERE asset.id=victims.id
    RETURNING asset.size_bytes
)
SELECT count(*)::bigint,COALESCE(sum(size_bytes),0)::bigint FROM deleted`

type postgresDB interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type PostgreSQLStore struct {
	pool      *pgxpool.Pool
	db        postgresDB
	maxBytes  int64
	readSlots chan struct{}
}

func NewPostgreSQLStore(pool *pgxpool.Pool, maxBytes int64) *PostgreSQLStore {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxObjectBytes
	}
	readLimit := defaultConcurrentMediaReads
	if maxConnections := int(pool.Config().MaxConns); maxConnections-readLimit < 5 {
		readLimit = max(1, maxConnections-5)
	}
	return &PostgreSQLStore{pool: pool, db: pool, maxBytes: maxBytes, readSlots: make(chan struct{}, readLimit)}
}

func newPostgreSQLStoreForTest(db postgresDB, maxBytes int64) *PostgreSQLStore {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxObjectBytes
	}
	return &PostgreSQLStore{db: db, maxBytes: maxBytes}
}

// Put streams a bounded body into PostgreSQL Large Objects. Only the fixed copy
// buffer is resident in the Go heap; media_assets keeps metadata and the OID.
func (store *PostgreSQLStore) Put(ctx context.Context, input PutObject) (Metadata, error) {
	metadata, err := store.validatePut(input)
	if err != nil {
		return Metadata{}, err
	}
	if store.pool == nil {
		return Metadata{}, errors.New("media PostgreSQL store has no transaction pool")
	}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return Metadata{}, fmt.Errorf("media transaction begin: %w", err)
	}
	defer tx.Rollback(ctx)
	var locked bool
	if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended('media-upload:'||$1,0))`, metadata.OwnerID).Scan(&locked); err != nil {
		return Metadata{}, fmt.Errorf("media quota lock: %w", err)
	}
	if !locked {
		return Metadata{}, ErrUploadBusy
	}
	var unattachedCount int64
	var unattachedBytes int64
	if err := tx.QueryRow(ctx, `SELECT count(*)::bigint,COALESCE(sum(asset.size_bytes),0)::bigint
		FROM media_assets asset WHERE asset.owner_id=$1
		AND NOT EXISTS(SELECT 1 FROM post_media linked WHERE linked.media_id=asset.id)
		AND NOT EXISTS(SELECT 1 FROM users avatar_user WHERE avatar_user.avatar_id=asset.id)`, metadata.OwnerID).Scan(&unattachedCount, &unattachedBytes); err != nil {
		return Metadata{}, fmt.Errorf("media quota query: %w", err)
	}
	if unattachedCount >= MaxUnattachedObjects || unattachedBytes > MaxUnattachedBytes-metadata.Size {
		return Metadata{}, ErrQuotaExceeded
	}
	largeObjects := tx.LargeObjects()
	oid, err := largeObjects.Create(ctx, 0)
	if err != nil {
		return Metadata{}, fmt.Errorf("media large object create: %w", err)
	}
	object, err := largeObjects.Open(ctx, oid, pgx.LargeObjectModeWrite)
	if err != nil {
		return Metadata{}, fmt.Errorf("media large object open: %w", err)
	}
	metadata.SHA256, err = streamAndHash(object, input.Body, metadata.Size, store.maxBytes)
	closeErr := object.Close()
	if err != nil {
		return Metadata{}, err
	}
	if closeErr != nil {
		return Metadata{}, fmt.Errorf("media large object close: %w", closeErr)
	}
	_, err = tx.Exec(ctx, `INSERT INTO media_assets
		(id,owner_id,filename,alt_text,mime_type,size_bytes,sha256,width,height,data,large_object_oid,created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,NULL,$10,$11)`,
		metadata.ID, metadata.OwnerID, metadata.Filename, metadata.AltText, metadata.MIMEType,
		metadata.Size, metadata.SHA256, metadata.Width, metadata.Height, oid, metadata.CreatedAt,
	)
	if err != nil {
		return Metadata{}, fmt.Errorf("media insert: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Metadata{}, fmt.Errorf("media transaction commit: %w", err)
	}
	return metadata, nil
}

func (store *PostgreSQLStore) validatePut(input PutObject) (Metadata, error) {
	metadata := input.Metadata
	metadata.ID = strings.TrimSpace(metadata.ID)
	metadata.OwnerID = strings.TrimSpace(metadata.OwnerID)
	metadata.Filename = strings.TrimSpace(metadata.Filename)
	metadata.AltText = strings.TrimSpace(metadata.AltText)
	metadata.MIMEType = strings.TrimSpace(metadata.MIMEType)
	if store == nil || input.Body == nil || metadata.ID == "" || metadata.OwnerID == "" || metadata.Filename == "" || metadata.MIMEType == "" || metadata.Size <= 0 || metadata.Width < 0 || metadata.Height < 0 || !utf8.ValidString(metadata.AltText) || utf8.RuneCountInString(metadata.AltText) > 500 || strings.ContainsRune(metadata.AltText, '\x00') {
		return Metadata{}, ErrInvalidObject
	}
	if metadata.Size > store.maxBytes {
		return Metadata{}, ErrTooLarge
	}
	if metadata.CreatedAt.IsZero() {
		metadata.CreatedAt = time.Now().UTC()
	} else {
		metadata.CreatedAt = metadata.CreatedAt.UTC()
	}
	return metadata, nil
}

func streamAndHash(destination io.Writer, source io.Reader, declaredSize, maxBytes int64) (string, error) {
	if destination == nil || source == nil || declaredSize <= 0 {
		return "", ErrInvalidObject
	}
	if maxBytes > 0 && declaredSize > maxBytes {
		return "", ErrTooLarge
	}
	hash := sha256.New()
	limited := io.LimitReader(source, declaredSize+1)
	written, err := io.CopyBuffer(io.MultiWriter(destination, hash), limited, make([]byte, 64<<10))
	if err != nil {
		return "", fmt.Errorf("media stream copy: %w", err)
	}
	if written != declaredSize {
		return "", ErrSizeMismatch
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (store *PostgreSQLStore) Open(ctx context.Context, mediaID, viewerID string) (Object, error) {
	if store == nil || store.pool == nil {
		return Object{}, errors.New("media PostgreSQL store has no transaction pool")
	}
	select {
	case store.readSlots <- struct{}{}:
	case <-ctx.Done():
		return Object{}, ctx.Err()
	}
	release := func() { <-store.readSlots }
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		release()
		return Object{}, fmt.Errorf("media read transaction begin: %w", err)
	}
	var metadata Metadata
	var oid pgtype.Uint32
	var legacyData []byte
	err = tx.QueryRow(ctx, mediaAccessSQL, strings.TrimSpace(mediaID), strings.TrimSpace(viewerID)).Scan(
		&metadata.ID, &metadata.OwnerID, &metadata.Filename, &metadata.AltText, &metadata.MIMEType,
		&metadata.Size, &metadata.SHA256, &metadata.Width, &metadata.Height,
		&metadata.CreatedAt, &oid, &legacyData,
	)
	if err != nil {
		_ = tx.Rollback(ctx)
		release()
		return Object{}, err
	}
	if !oid.Valid {
		_ = tx.Rollback(ctx)
		release()
		if int64(len(legacyData)) != metadata.Size {
			return Object{}, errors.New("legacy media payload is missing or corrupt")
		}
		return Object{Metadata: metadata, Body: &memoryBody{Reader: bytes.NewReader(legacyData)}}, nil
	}
	largeObjects := tx.LargeObjects()
	largeObject, err := largeObjects.Open(ctx, oid.Uint32, pgx.LargeObjectModeRead)
	if err != nil {
		_ = tx.Rollback(ctx)
		release()
		return Object{}, fmt.Errorf("media large object read open: %w", err)
	}
	return Object{Metadata: metadata, Body: &largeObjectBody{object: largeObject, tx: tx, release: release}}, nil
}

type largeObjectBody struct {
	object  *pgx.LargeObject
	tx      pgx.Tx
	once    sync.Once
	err     error
	release func()
}

func (body *largeObjectBody) Read(buffer []byte) (int, error) { return body.object.Read(buffer) }

func (body *largeObjectBody) Seek(offset int64, whence int) (int64, error) {
	return body.object.Seek(offset, whence)
}

func (body *largeObjectBody) Close() error {
	body.once.Do(func() {
		defer body.release()
		objectErr := body.object.Close()
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		rollbackErr := body.tx.Rollback(cleanup)
		if errors.Is(rollbackErr, pgx.ErrTxClosed) {
			rollbackErr = nil
		}
		body.err = errors.Join(objectErr, rollbackErr)
	})
	return body.err
}

type memoryBody struct{ *bytes.Reader }

func (*memoryBody) Close() error { return nil }

func (store *PostgreSQLStore) DeleteOrphans(ctx context.Context, olderThan time.Time, limit int) (CleanupResult, error) {
	if store == nil || store.db == nil {
		return CleanupResult{}, errors.New("media PostgreSQL store has no database")
	}
	if olderThan.IsZero() || limit < 1 || limit > 10000 {
		return CleanupResult{}, ErrInvalidObject
	}
	var result CleanupResult
	err := store.db.QueryRow(ctx, orphanCleanupSQL, olderThan.UTC(), limit).Scan(&result.Deleted, &result.DeletedBytes)
	if err != nil {
		return CleanupResult{}, fmt.Errorf("media orphan cleanup: %w", err)
	}
	return result, nil
}

var _ MediaStore = (*PostgreSQLStore)(nil)
