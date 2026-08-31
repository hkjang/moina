package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	mediastore "github.com/hkjang/moina/backend/internal/media"
	"github.com/hkjang/moina/backend/internal/model"
	"github.com/hkjang/moina/backend/internal/secure"
	"github.com/hkjang/moina/backend/internal/store"
)

func TestPostgreSQLDeleteMediaOnlyRemovesOwnedOrphans(t *testing.T) {
	dsn := os.Getenv("MOINA_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("MOINA_TEST_POSTGRES_DSN is not set")
	}
	ctx := t.Context()
	repository, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(repository.Close)
	secrets, err := secure.New(bytes.Repeat([]byte{37}, 32))
	if err != nil {
		t.Fatal(err)
	}

	suffix := time.Now().UnixNano()
	ownerID := fmt.Sprintf("usr_media_delete_owner_%d", suffix)
	otherID := fmt.Sprintf("usr_media_delete_other_%d", suffix)
	postID := fmt.Sprintf("post_media_delete_%d", suffix)
	byteaOrphanID := fmt.Sprintf("media_delete_bytea_%d", suffix)
	largeObjectOrphanID := fmt.Sprintf("media_delete_lo_%d", suffix)
	attachedID := fmt.Sprintf("media_delete_attached_%d", suffix)
	avatarID := fmt.Sprintf("media_delete_avatar_%d", suffix)
	foreignID := fmt.Sprintf("media_delete_foreign_%d", suffix)
	allMediaIDs := []string{byteaOrphanID, largeObjectOrphanID, attachedID, avatarID, foreignID}

	if _, err := repository.Pool().Exec(ctx, `INSERT INTO users(id,username,display_name,roles)
		VALUES($1,$1,$1,ARRAY['member']::text[]),($2,$2,$2,ARRAY['member']::text[])`, ownerID, otherID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM audit_events WHERE actor_id=$1`, ownerID)
		_, _ = repository.Pool().Exec(cleanupContext, `UPDATE users SET avatar_id='' WHERE id=$1`, ownerID)
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM posts WHERE id=$1`, postID)
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM media_assets WHERE id=ANY($1::text[])`, allMediaIDs)
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM users WHERE id=ANY($1::text[])`, []string{ownerID, otherID})
	})

	if _, err := repository.Pool().Exec(ctx, `INSERT INTO media_assets
		(id,owner_id,filename,alt_text,mime_type,size_bytes,sha256,width,height,data)
		VALUES
		($1,$2,'orphan.png','','image/png',1,repeat('0',64),1,1,decode('00','hex')),
		($3,$2,'avatar.png','','image/png',1,repeat('1',64),1,1,decode('01','hex')),
		($4,$5,'foreign.png','','image/png',1,repeat('2',64),1,1,decode('02','hex'))`,
		byteaOrphanID, ownerID, avatarID, foreignID, otherID); err != nil {
		t.Fatal(err)
	}

	server := New(repository, secrets, "v0.1.6-test")
	putLargeObject := func(mediaID, filename string, body []byte) uint32 {
		t.Helper()
		if _, err := server.media.Put(ctx, mediastore.PutObject{
			Metadata: mediastore.Metadata{
				ID: mediaID, OwnerID: ownerID, Filename: filename, MIMEType: "image/png",
				Size: int64(len(body)), Width: 1, Height: 1, CreatedAt: time.Now().UTC(),
			},
			Body: bytes.NewReader(body),
		}); err != nil {
			t.Fatal(err)
		}
		var oid uint32
		if err := repository.Pool().QueryRow(ctx, `SELECT large_object_oid FROM media_assets WHERE id=$1 AND data IS NULL`, mediaID).Scan(&oid); err != nil {
			t.Fatal(err)
		}
		if oid == 0 {
			t.Fatalf("media %s has zero large object OID", mediaID)
		}
		return oid
	}
	largeObjectOrphanOID := putLargeObject(largeObjectOrphanID, "large-orphan.png", bytes.Repeat([]byte{3}, 4096))
	attachedOID := putLargeObject(attachedID, "attached.png", bytes.Repeat([]byte{4}, 4096))

	if _, err := repository.Pool().Exec(ctx, `INSERT INTO posts(id,author_id,content,visibility,status,published_at)
		VALUES($1,$2,'attached media','public','published',now())`, postID, ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(ctx, `INSERT INTO post_media(post_id,media_id,position) VALUES($1,$2,0)`, postID, attachedID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(ctx, `UPDATE users SET avatar_id=$1 WHERE id=$2`, avatarID, ownerID); err != nil {
		t.Fatal(err)
	}

	token := fmt.Sprintf("media-delete-token-%d", suffix)
	csrf := fmt.Sprintf("media-delete-csrf-%d", suffix)
	if err := repository.CreateSession(ctx, model.Session{
		ID: "session_media_delete_" + fmt.Sprint(suffix), UserID: ownerID,
		TokenHash: secrets.HashToken(token), CSRFHash: secrets.HashToken(csrf),
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	deleteMedia := func(mediaID string, wantStatus int) string {
		t.Helper()
		request := httptest.NewRequest(http.MethodDelete, "/api/v1/media/"+mediaID, nil)
		request.AddCookie(&http.Cookie{Name: SessionCookie, Value: token})
		request.Header.Set("X-CSRF-Token", csrf)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != wantStatus {
			t.Fatalf("DELETE media %s = %d: %s", mediaID, response.Code, response.Body.String())
		}
		if wantStatus == http.StatusNoContent {
			if response.Body.Len() != 0 {
				t.Fatalf("DELETE media %s returned a 204 body: %q", mediaID, response.Body.String())
			}
			return ""
		}
		var payload struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload.Code
	}

	deleteMedia(byteaOrphanID, http.StatusNoContent)
	deleteMedia(largeObjectOrphanID, http.StatusNoContent)
	for _, mediaID := range []string{attachedID, avatarID, foreignID, "media-delete-missing"} {
		if code := deleteMedia(mediaID, http.StatusConflict); code != "media_in_use_or_unavailable" {
			t.Fatalf("DELETE media %s error code = %q", mediaID, code)
		}
	}

	for _, mediaID := range []string{byteaOrphanID, largeObjectOrphanID} {
		var exists bool
		if err := repository.Pool().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM media_assets WHERE id=$1)`, mediaID).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Fatalf("deleted orphan media %s is still stored", mediaID)
		}
	}
	for _, mediaID := range []string{attachedID, avatarID, foreignID} {
		var exists bool
		if err := repository.Pool().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM media_assets WHERE id=$1)`, mediaID).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("protected media %s was deleted", mediaID)
		}
	}
	var attachedLinkExists bool
	if err := repository.Pool().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM post_media WHERE post_id=$1 AND media_id=$2)`, postID, attachedID).Scan(&attachedLinkExists); err != nil {
		t.Fatal(err)
	}
	if !attachedLinkExists {
		t.Fatal("attached media relationship was removed")
	}
	var currentAvatarID string
	if err := repository.Pool().QueryRow(ctx, `SELECT avatar_id FROM users WHERE id=$1`, ownerID).Scan(&currentAvatarID); err != nil {
		t.Fatal(err)
	}
	if currentAvatarID != avatarID {
		t.Fatalf("avatar_id = %q, want %q", currentAvatarID, avatarID)
	}

	assertLargeObjectExists := func(oid uint32, want bool) {
		t.Helper()
		var exists bool
		if err := repository.Pool().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_largeobject_metadata WHERE oid=$1)`, oid).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists != want {
			t.Fatalf("large object OID %d exists = %t, want %t", oid, exists, want)
		}
	}
	assertLargeObjectExists(largeObjectOrphanOID, false)
	assertLargeObjectExists(attachedOID, true)

	var auditCount int
	if err := repository.Pool().QueryRow(ctx, `SELECT count(*) FROM audit_events
		WHERE actor_id=$1 AND action='media.delete' AND target_type='media'
		AND target_id=ANY($2::text[]) AND success`, ownerID, []string{byteaOrphanID, largeObjectOrphanID}).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 2 {
		t.Fatalf("successful media.delete audit count = %d, want 2", auditCount)
	}
}
