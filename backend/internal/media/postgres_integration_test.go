package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	storepkg "github.com/hkjang/moina/backend/internal/store"
)

func TestPostgreSQLLargeObjectRoundTripAndCleanup(t *testing.T) {
	dsn := os.Getenv("MOINA_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("MOINA_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	database, err := storepkg.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	defer database.Close()
	suffix := time.Now().UnixNano()
	userID := fmt.Sprintf("usr_media_%d", suffix)
	mediaID := fmt.Sprintf("media_integration_%d", suffix)
	if _, err := database.Pool().Exec(ctx, `INSERT INTO users(id,username,display_name) VALUES($1,$2,$2)`, userID, userID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = database.Pool().Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID)
	}()
	body := bytes.Repeat([]byte("MOINA-stream-"), 16384)
	backend := NewPostgreSQLStore(database.Pool(), int64(len(body))+1)
	metadata, err := backend.Put(ctx, PutObject{
		Metadata: Metadata{ID: mediaID, OwnerID: userID, Filename: "stream.bin", MIMEType: "application/octet-stream", Size: int64(len(body)), CreatedAt: time.Unix(1, 0).UTC()},
		Body:     bytes.NewReader(body),
	})
	if err != nil {
		t.Fatal(err)
	}
	var oid uint32
	if err := database.Pool().QueryRow(ctx, `SELECT large_object_oid FROM media_assets WHERE id=$1 AND data IS NULL`, mediaID).Scan(&oid); err != nil || oid == 0 {
		t.Fatalf("large object OID=%d err=%v", oid, err)
	}
	object, err := backend.Open(ctx, mediaID, userID)
	if err != nil {
		t.Fatal(err)
	}
	var streamed bytes.Buffer
	written, copyErr := io.Copy(&streamed, object.Body)
	closeErr := object.Body.Close()
	if copyErr != nil || closeErr != nil || written != metadata.Size || !bytes.Equal(streamed.Bytes(), body) {
		t.Fatalf("stream copy bytes=%d copyErr=%v closeErr=%v", written, copyErr, closeErr)
	}
	result, err := backend.DeleteOrphans(ctx, time.Unix(2, 0).UTC(), 1)
	if err != nil || result.Deleted != 1 {
		t.Fatalf("cleanup=%#v err=%v", result, err)
	}
	var exists bool
	if err := database.Pool().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_largeobject_metadata WHERE oid=$1)`, oid).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("orphan cleanup left PostgreSQL large object behind")
	}
}

func TestPostgreSQLUploadLockQuotaAndReadReservation(t *testing.T) {
	dsn := os.Getenv("MOINA_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("MOINA_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	database, err := storepkg.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	suffix := time.Now().UnixNano()
	userID := fmt.Sprintf("usr_media_quota_%d", suffix)
	mediaPrefix := fmt.Sprintf("media_quota_%d_", suffix)
	if _, err := database.Pool().Exec(ctx, `INSERT INTO users(id,username,display_name) VALUES($1,$2,$2)`, userID, userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = database.Pool().Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID)
	})

	backend := NewPostgreSQLStore(database.Pool(), 1024)
	if got := cap(backend.readSlots); got != defaultConcurrentMediaReads {
		t.Fatalf("media read slots = %d, want %d", got, defaultConcurrentMediaReads)
	}

	locker, err := database.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := locker.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('media-upload:'||$1,0))`, userID); err != nil {
		_ = locker.Rollback(ctx)
		t.Fatal(err)
	}
	_, err = backend.Put(ctx, PutObject{
		Metadata: Metadata{ID: mediaPrefix + "busy", OwnerID: userID, Filename: "busy.png", MIMEType: "image/png", Size: 1},
		Body:     bytes.NewReader([]byte{0}),
	})
	if rollbackErr := locker.Rollback(ctx); rollbackErr != nil {
		t.Fatal(rollbackErr)
	}
	if !errors.Is(err, ErrUploadBusy) {
		t.Fatalf("locked upload error = %v, want ErrUploadBusy", err)
	}

	if _, err := database.Pool().Exec(ctx, `INSERT INTO media_assets
		(id,owner_id,filename,alt_text,mime_type,size_bytes,sha256,width,height,data,created_at)
		SELECT $1||value,$2,'quota.png','','image/png',1,repeat('0',64),1,1,decode('00','hex'),now()
		FROM generate_series(1,$3) value`, mediaPrefix, userID, MaxUnattachedObjects); err != nil {
		t.Fatal(err)
	}
	_, err = backend.Put(ctx, PutObject{
		Metadata: Metadata{ID: mediaPrefix + "overflow", OwnerID: userID, Filename: "overflow.png", MIMEType: "image/png", Size: 1},
		Body:     bytes.NewReader([]byte{0}),
	})
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("quota upload error = %v, want ErrQuotaExceeded", err)
	}
}
