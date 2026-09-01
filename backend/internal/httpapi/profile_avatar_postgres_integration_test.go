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

	"github.com/hkjang/moina/backend/internal/model"
	"github.com/hkjang/moina/backend/internal/secure"
	"github.com/hkjang/moina/backend/internal/store"
)

func TestPostgreSQLProfileAvatarRequiresOwnedImageAndSupportsRemoval(t *testing.T) {
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
	secrets, err := secure.New(bytes.Repeat([]byte{41}, 32))
	if err != nil {
		t.Fatal(err)
	}

	suffix := time.Now().UnixNano()
	ownerID := fmt.Sprintf("usr_profile_avatar_owner_%d", suffix)
	otherID := fmt.Sprintf("usr_profile_avatar_other_%d", suffix)
	imageID := fmt.Sprintf("media_profile_avatar_image_%d", suffix)
	videoID := fmt.Sprintf("media_profile_avatar_video_%d", suffix)
	foreignID := fmt.Sprintf("media_profile_avatar_foreign_%d", suffix)
	if _, err := repository.Pool().Exec(ctx, `INSERT INTO users(id,username,display_name,roles)
		VALUES($1,$1,'Avatar owner',ARRAY[]::text[]),($2,$2,'Other owner',ARRAY['member']::text[])`, ownerID, otherID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM audit_events WHERE actor_id=$1`, ownerID)
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM users WHERE id=ANY($1::text[])`, []string{ownerID, otherID})
	})
	if _, err := repository.Pool().Exec(ctx, `INSERT INTO media_assets
		(id,owner_id,filename,mime_type,size_bytes,sha256,width,height,data)
		VALUES
		($1,$2,'avatar.png','image/png',1,repeat('1',64),1,1,decode('01','hex')),
		($3,$2,'avatar.webm','video/webm',1,repeat('2',64),0,0,decode('02','hex')),
		($4,$5,'foreign.png','image/png',1,repeat('3',64),1,1,decode('03','hex'))`,
		imageID, ownerID, videoID, foreignID, otherID); err != nil {
		t.Fatal(err)
	}

	token := fmt.Sprintf("profile-avatar-token-%d", suffix)
	csrf := fmt.Sprintf("profile-avatar-csrf-%d", suffix)
	if err := repository.CreateSession(ctx, model.Session{
		ID: "session_profile_avatar_" + fmt.Sprint(suffix), UserID: ownerID,
		TokenHash: secrets.HashToken(token), CSRFHash: secrets.HashToken(csrf),
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	handler := New(repository, secrets, "v0.1.10-test").Handler()
	patchAvatar := func(avatarID string, wantStatus int) map[string]any {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"avatarId": avatarID})
		request := httptest.NewRequest(http.MethodPatch, "/api/v1/profile", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-CSRF-Token", csrf)
		request.AddCookie(&http.Cookie{Name: SessionCookie, Value: token})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != wantStatus {
			t.Fatalf("PATCH avatar %q = %d: %s", avatarID, response.Code, response.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}

	for _, rejected := range []string{videoID, foreignID, "missing-avatar"} {
		payload := patchAvatar(rejected, http.StatusBadRequest)
		if payload["code"] != "invalid_avatar" {
			t.Fatalf("PATCH avatar %q code=%v", rejected, payload["code"])
		}
	}
	payload := patchAvatar(imageID, http.StatusOK)
	data, _ := payload["data"].(map[string]any)
	if data["avatarId"] != imageID || data["avatarUrl"] != "/api/v1/media/"+imageID {
		t.Fatalf("profile avatar response=%v", data)
	}
	var storedAvatarID string
	if err := repository.Pool().QueryRow(ctx, `SELECT avatar_id FROM users WHERE id=$1`, ownerID).Scan(&storedAvatarID); err != nil {
		t.Fatal(err)
	}
	if storedAvatarID != imageID {
		t.Fatalf("avatar_id=%q want=%q", storedAvatarID, imageID)
	}
	mediaRequest := httptest.NewRequest(http.MethodGet, "/api/v1/media/"+imageID, nil)
	mediaRequest.AddCookie(&http.Cookie{Name: SessionCookie, Value: token})
	mediaResponse := httptest.NewRecorder()
	handler.ServeHTTP(mediaResponse, mediaRequest)
	if mediaResponse.Code != http.StatusOK || mediaResponse.Body.Len() != 1 {
		t.Fatalf("GET own profile media = %d body=%q", mediaResponse.Code, mediaResponse.Body.String())
	}

	payload = patchAvatar("", http.StatusOK)
	data, _ = payload["data"].(map[string]any)
	if data["avatarId"] != "" || data["avatarUrl"] != "" {
		t.Fatalf("removed profile avatar response=%v", data)
	}
}
