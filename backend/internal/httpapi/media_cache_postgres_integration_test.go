package httpapi

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hkjang/moina/backend/internal/model"
	"github.com/hkjang/moina/backend/internal/secure"
	"github.com/hkjang/moina/backend/internal/store"
)

// 미디어 조회는 요청마다 차단 관계와 공개 범위를 검사하므로 응답을 그대로 캐시하면
// 검사 결과가 낡습니다. 재검증은 ETag로 저렴하게 끝나되 접근 권한이 사라지면
// 같은 재검증 요청이 곧바로 막혀야 합니다.
func TestPostgreSQLMediaRevalidatesInsteadOfCaching(t *testing.T) {
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
	secrets, err := secure.New(bytes.Repeat([]byte{47}, 32))
	if err != nil {
		t.Fatal(err)
	}

	suffix := time.Now().UnixNano()
	authorID := fmt.Sprintf("usr_media_cache_author_%d", suffix)
	viewerID := fmt.Sprintf("usr_media_cache_viewer_%d", suffix)
	mediaID := fmt.Sprintf("media_cache_%d", suffix)
	postID := fmt.Sprintf("post_media_cache_%d", suffix)
	digest := strings.Repeat("7", 64)
	if _, err := repository.Pool().Exec(ctx, `INSERT INTO users(id,username,display_name,roles)
		VALUES($1,$1,'Cache author',ARRAY['member']::text[]),($2,$2,'Cache viewer',ARRAY['member']::text[])`, authorID, viewerID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM blocks WHERE blocker_id=ANY($1::text[]) OR blocked_id=ANY($1::text[])`, []string{authorID, viewerID})
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM posts WHERE author_id=ANY($1::text[])`, []string{authorID, viewerID})
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM media_assets WHERE owner_id=ANY($1::text[])`, []string{authorID, viewerID})
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM users WHERE id=ANY($1::text[])`, []string{authorID, viewerID})
	})
	if _, err := repository.Pool().Exec(ctx, `INSERT INTO media_assets
		(id,owner_id,filename,alt_text,mime_type,size_bytes,sha256,width,height,data)
		VALUES($1,$2,'shared.png','','image/png',1,$3,1,1,decode('05','hex'))`, mediaID, authorID, digest); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(ctx, `INSERT INTO posts(id,author_id,content,visibility,status,published_at)
		VALUES($1,$2,'첨부가 있는 Moin','public','published',now())`, postID, authorID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(ctx, `INSERT INTO post_media(post_id,media_id,position) VALUES($1,$2,0)`, postID, mediaID); err != nil {
		t.Fatal(err)
	}

	token := fmt.Sprintf("media-cache-token-%d", suffix)
	if err := repository.CreateSession(ctx, model.Session{
		ID: "session_media_cache_" + fmt.Sprint(suffix), UserID: viewerID,
		TokenHash: secrets.HashToken(token), CSRFHash: secrets.HashToken(fmt.Sprintf("media-cache-csrf-%d", suffix)),
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	handler := New(repository, secrets, "v0.1.12-test").Handler()
	fetch := func(ifNoneMatch string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/api/v1/media/"+mediaID, nil)
		request.AddCookie(&http.Cookie{Name: SessionCookie, Value: token})
		if ifNoneMatch != "" {
			request.Header.Set("If-None-Match", ifNoneMatch)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}

	first := fetch("")
	if first.Code != http.StatusOK || first.Body.Len() != 1 {
		t.Fatalf("GET media = %d body=%q", first.Code, first.Body.String())
	}
	if got := first.Header().Get("Cache-Control"); got != "private, no-cache" {
		t.Fatalf("Cache-Control=%q, 캐시 없이 재검증해야 합니다", got)
	}
	etag := first.Header().Get("ETag")
	if etag != `"`+digest+`"` {
		t.Fatalf("ETag=%q, want=%q", etag, `"`+digest+`"`)
	}

	revalidated := fetch(etag)
	if revalidated.Code != http.StatusNotModified || revalidated.Body.Len() != 0 {
		t.Fatalf("재검증 = %d body=%q", revalidated.Code, revalidated.Body.String())
	}

	if _, err := repository.Pool().Exec(ctx, `INSERT INTO blocks(blocker_id,blocked_id) VALUES($1,$2)`, authorID, viewerID); err != nil {
		t.Fatal(err)
	}
	blocked := fetch(etag)
	if blocked.Code != http.StatusNotFound {
		t.Fatalf("차단 후 재검증 = %d body=%q", blocked.Code, blocked.Body.String())
	}
}
