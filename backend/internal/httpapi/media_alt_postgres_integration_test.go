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

func TestPostgreSQLPostMediaAltTextIsScopedToPost(t *testing.T) {
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
	secrets, err := secure.New(bytes.Repeat([]byte{29}, 32))
	if err != nil {
		t.Fatal(err)
	}

	suffix := time.Now().UnixNano()
	userID := fmt.Sprintf("usr_media_alt_%d", suffix)
	mediaID := fmt.Sprintf("media_alt_%d", suffix)
	if _, err := repository.Pool().Exec(ctx, `INSERT INTO users(id,username,display_name,roles)
		VALUES($1,$1,$1,ARRAY['member']::text[])`, userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM users WHERE id=$1`, userID)
	})
	const uploadDefaultAlt = "업로드 기본 대체 텍스트"
	if _, err := repository.Pool().Exec(ctx, `INSERT INTO media_assets
		(id,owner_id,filename,alt_text,mime_type,size_bytes,sha256,width,height,data)
		VALUES($1,$2,'shared.png',$3,'image/png',1,repeat('0',64),1,1,decode('00','hex'))`, mediaID, userID, uploadDefaultAlt); err != nil {
		t.Fatal(err)
	}

	token := fmt.Sprintf("media-alt-token-%d", suffix)
	csrf := fmt.Sprintf("media-alt-csrf-%d", suffix)
	if err := repository.CreateSession(ctx, model.Session{
		ID: "session_media_alt_" + fmt.Sprint(suffix), UserID: userID,
		TokenHash: secrets.HashToken(token), CSRFHash: secrets.HashToken(csrf),
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	handler := New(repository, secrets, "v0.1.4").Handler()

	request := func(method, path string, body any, wantStatus int) model.Moin {
		t.Helper()
		var encoded []byte
		if body != nil {
			encoded, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		httpRequest := httptest.NewRequest(method, path, bytes.NewReader(encoded))
		httpRequest.AddCookie(&http.Cookie{Name: SessionCookie, Value: token})
		if body != nil {
			httpRequest.Header.Set("Content-Type", "application/json")
			httpRequest.Header.Set("X-CSRF-Token", csrf)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httpRequest)
		if response.Code != wantStatus {
			t.Fatalf("%s %s = %d: %s", method, path, response.Code, response.Body.String())
		}
		var envelope struct {
			Data model.Moin `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope.Data
	}

	create := func(content, altText string) model.Moin {
		t.Helper()
		return request(http.MethodPost, "/api/v1/posts", map[string]any{
			"content": content, "visibility": "public", "mediaIds": []string{mediaID},
			"mediaAltTexts": map[string]string{mediaID: altText},
		}, http.StatusCreated)
	}
	first := create("첫 번째 문맥", "첫 번째 게시물의 설명")
	second := create("두 번째 문맥", "두 번째 게시물의 설명")
	assertPostMediaAlt(t, first, mediaID, "첫 번째 게시물의 설명")
	assertPostMediaAlt(t, second, mediaID, "두 번째 게시물의 설명")

	first = request(http.MethodGet, "/api/v1/posts/"+first.ID, nil, http.StatusOK)
	second = request(http.MethodGet, "/api/v1/posts/"+second.ID, nil, http.StatusOK)
	assertPostMediaAlt(t, first, mediaID, "첫 번째 게시물의 설명")
	assertPostMediaAlt(t, second, mediaID, "두 번째 게시물의 설명")

	first = request(http.MethodPatch, "/api/v1/posts/"+first.ID, map[string]any{
		"content": "첫 번째 문맥 수정", "mediaAltTexts": map[string]string{mediaID: "첫 번째 게시물의 수정 설명"},
	}, http.StatusOK)
	assertPostMediaAlt(t, first, mediaID, "첫 번째 게시물의 수정 설명")
	second = request(http.MethodGet, "/api/v1/posts/"+second.ID, nil, http.StatusOK)
	assertPostMediaAlt(t, second, mediaID, "두 번째 게시물의 설명")

	feedRequest := httptest.NewRequest(http.MethodGet, "/api/v1/feed?mode=following&limit=20", nil)
	feedRequest.AddCookie(&http.Cookie{Name: SessionCookie, Value: token})
	feedResponse := httptest.NewRecorder()
	handler.ServeHTTP(feedResponse, feedRequest)
	if feedResponse.Code != http.StatusOK {
		t.Fatalf("GET following Flow = %d: %s", feedResponse.Code, feedResponse.Body.String())
	}
	var feedEnvelope struct {
		Data struct {
			Items []model.Moin `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(feedResponse.Body.Bytes(), &feedEnvelope); err != nil {
		t.Fatal(err)
	}
	wantByPost := map[string]string{first.ID: "첫 번째 게시물의 수정 설명", second.ID: "두 번째 게시물의 설명"}
	for _, post := range feedEnvelope.Data.Items {
		want, relevant := wantByPost[post.ID]
		if !relevant {
			continue
		}
		assertPostMediaAlt(t, post, mediaID, want)
		delete(wantByPost, post.ID)
	}
	if len(wantByPost) != 0 {
		t.Fatalf("Flow에서 문맥별 미디어를 찾지 못했습니다: %v", wantByPost)
	}

	var assetAlt string
	if err := repository.Pool().QueryRow(ctx, `SELECT alt_text FROM media_assets WHERE id=$1`, mediaID).Scan(&assetAlt); err != nil {
		t.Fatal(err)
	}
	if assetAlt != uploadDefaultAlt {
		t.Fatalf("media_assets.alt_text = %q, want unchanged default %q", assetAlt, uploadDefaultAlt)
	}
	if _, err := repository.Pool().Exec(ctx, `UPDATE post_media SET alt_text=repeat('가',501) WHERE post_id=$1 AND media_id=$2`, first.ID, mediaID); err == nil {
		t.Fatal("post_media accepted alt_text longer than 500 characters")
	}
}

func assertPostMediaAlt(t *testing.T, post model.Moin, mediaID, want string) {
	t.Helper()
	if len(post.Media) != 1 || post.Media[0].ID != mediaID || post.Media[0].AltText != want {
		t.Fatalf("post %s media = %+v, want %s alt %q", post.ID, post.Media, mediaID, want)
	}
}
