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
	otherUserID := fmt.Sprintf("usr_media_alt_other_%d", suffix)
	mediaID := fmt.Sprintf("media_alt_%d", suffix)
	newMediaID := fmt.Sprintf("media_alt_new_%d", suffix)
	otherMediaID := fmt.Sprintf("media_alt_other_%d", suffix)
	grandfatherMediaIDs := []string{
		fmt.Sprintf("media_alt_grandfather_1_%d", suffix),
		fmt.Sprintf("media_alt_grandfather_2_%d", suffix),
		fmt.Sprintf("media_alt_grandfather_3_%d", suffix),
	}
	aboveLimitMediaID := fmt.Sprintf("media_alt_above_limit_%d", suffix)
	if _, err := repository.Pool().Exec(ctx, `INSERT INTO users(id,username,display_name,roles)
		VALUES($1,$1,$1,ARRAY['member']::text[]),($2,$2,$2,ARRAY['member']::text[])`, userID, otherUserID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM users WHERE id=ANY($1)`, []string{userID, otherUserID})
	})
	const uploadDefaultAlt = "업로드 기본 대체 텍스트"
	const newUploadDefaultAlt = "신규 업로드 기본 대체 텍스트"
	if _, err := repository.Pool().Exec(ctx, `INSERT INTO media_assets
		(id,owner_id,filename,alt_text,mime_type,size_bytes,sha256,width,height,data)
		VALUES
		($1,$2,'shared.png',$3,'image/png',1,repeat('0',64),1,1,decode('00','hex')),
		($4,$2,'new.png',$5,'image/png',1,repeat('1',64),1,1,decode('01','hex')),
		($6,$7,'other.png','다른 사용자 기본 설명','image/png',1,repeat('2',64),1,1,decode('02','hex'))`,
		mediaID, userID, uploadDefaultAlt, newMediaID, newUploadDefaultAlt, otherMediaID, otherUserID); err != nil {
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
	handler := New(repository, secrets, "v0.1.7").Handler()

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

	first = request(http.MethodPatch, "/api/v1/posts/"+first.ID, map[string]any{
		"content": "첨부 순서와 목록 수정", "mediaIds": []string{newMediaID, mediaID},
	}, http.StatusOK)
	assertPostMediaList(t, first, []string{newMediaID, mediaID}, map[string]string{
		newMediaID: newUploadDefaultAlt,
		mediaID:    "첫 번째 게시물의 수정 설명",
	})

	request(http.MethodPatch, "/api/v1/posts/"+first.ID, map[string]any{
		"content": "잘못된 설명으로 롤백되어야 함", "mediaIds": []string{newMediaID, mediaID},
		"mediaAltTexts": map[string]string{"media-not-attached": "허용되지 않는 설명"},
	}, http.StatusBadRequest)
	first = request(http.MethodGet, "/api/v1/posts/"+first.ID, nil, http.StatusOK)
	if first.Content != "첨부 순서와 목록 수정" {
		t.Fatalf("invalid alt key 뒤 content = %q", first.Content)
	}
	assertPostMediaList(t, first, []string{newMediaID, mediaID}, map[string]string{
		newMediaID: newUploadDefaultAlt,
		mediaID:    "첫 번째 게시물의 수정 설명",
	})

	request(http.MethodPatch, "/api/v1/posts/"+first.ID, map[string]any{
		"content": "다른 사용자 media로 롤백되어야 함", "mediaIds": []string{otherMediaID},
	}, http.StatusBadRequest)
	first = request(http.MethodGet, "/api/v1/posts/"+first.ID, nil, http.StatusOK)
	if first.Content != "첨부 순서와 목록 수정" {
		t.Fatalf("foreign media 뒤 content = %q", first.Content)
	}
	assertPostMediaList(t, first, []string{newMediaID, mediaID}, map[string]string{
		newMediaID: newUploadDefaultAlt,
		mediaID:    "첫 번째 게시물의 수정 설명",
	})
	request(http.MethodPatch, "/api/v1/posts/"+first.ID, map[string]any{
		"content": "중복 media로 변경되지 않아야 함", "mediaIds": []string{mediaID, mediaID},
	}, http.StatusBadRequest)
	request(http.MethodPatch, "/api/v1/posts/"+first.ID, map[string]any{
		"content": "없는 media로 롤백되어야 함", "mediaIds": []string{"media-does-not-exist"},
	}, http.StatusBadRequest)
	first = request(http.MethodGet, "/api/v1/posts/"+first.ID, nil, http.StatusOK)
	if first.Content != "첨부 순서와 목록 수정" {
		t.Fatalf("duplicate/missing media 뒤 content = %q", first.Content)
	}
	assertPostMediaList(t, first, []string{newMediaID, mediaID}, map[string]string{
		newMediaID: newUploadDefaultAlt,
		mediaID:    "첫 번째 게시물의 수정 설명",
	})

	grandfatherAssets := append(append([]string{}, grandfatherMediaIDs...), aboveLimitMediaID)
	if _, err := repository.Pool().Exec(ctx, `INSERT INTO media_assets
		(id,owner_id,filename,alt_text,mime_type,size_bytes,sha256,width,height,data)
		SELECT value,$2,value||'.png','기존 첨부 기본 설명 '||ordinality,'image/png',1,repeat('3',64),1,1,decode('03','hex')
		FROM unnest($1::text[]) WITH ORDINALITY AS assets(value,ordinality)`, grandfatherAssets, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(ctx, `INSERT INTO post_media(post_id,media_id,position,alt_text)
		SELECT $1,value,(ordinality+1)::smallint,'기존 문맥 설명 '||ordinality
		FROM unnest($2::text[]) WITH ORDINALITY AS attachments(value,ordinality)`, first.ID, grandfatherMediaIDs); err != nil {
		t.Fatal(err)
	}
	grandfatherOrder := []string{grandfatherMediaIDs[2], grandfatherMediaIDs[1], grandfatherMediaIDs[0], newMediaID, mediaID}
	grandfatherAlt := map[string]string{
		grandfatherMediaIDs[0]: "기존 문맥 설명 1",
		grandfatherMediaIDs[1]: "기존 문맥 설명 2",
		grandfatherMediaIDs[2]: "기존 문맥 설명 3",
		newMediaID:             newUploadDefaultAlt,
		mediaID:                "첫 번째 게시물의 수정 설명",
	}
	first = request(http.MethodPatch, "/api/v1/posts/"+first.ID, map[string]any{
		"content": "낮아진 한도 위 기존 첨부 재정렬", "mediaIds": grandfatherOrder,
	}, http.StatusOK)
	assertPostMediaList(t, first, grandfatherOrder, grandfatherAlt)

	request(http.MethodPatch, "/api/v1/posts/"+first.ID, map[string]any{
		"content": "낮아진 한도 위 신규 첨부는 롤백", "mediaIds": []string{
			grandfatherMediaIDs[2], grandfatherMediaIDs[1], grandfatherMediaIDs[0], newMediaID, aboveLimitMediaID,
		},
	}, http.StatusBadRequest)
	first = request(http.MethodGet, "/api/v1/posts/"+first.ID, nil, http.StatusOK)
	if first.Content != "낮아진 한도 위 기존 첨부 재정렬" {
		t.Fatalf("above-limit addition 뒤 content = %q", first.Content)
	}
	assertPostMediaList(t, first, grandfatherOrder, grandfatherAlt)

	first = request(http.MethodPatch, "/api/v1/posts/"+first.ID, map[string]any{
		"content": "첨부 전체 제거", "mediaIds": []string{},
	}, http.StatusOK)
	if len(first.Media) != 0 {
		t.Fatalf("empty mediaIds 뒤 media = %+v", first.Media)
	}
	second = request(http.MethodGet, "/api/v1/posts/"+second.ID, nil, http.StatusOK)
	assertPostMediaAlt(t, second, mediaID, "두 번째 게시물의 설명")

	if _, err := repository.Pool().Exec(ctx, `UPDATE post_media SET alt_text=repeat('가',501) WHERE post_id=$1 AND media_id=$2`, second.ID, mediaID); err == nil {
		t.Fatal("post_media accepted alt_text longer than 500 characters")
	}
}

func assertPostMediaAlt(t *testing.T, post model.Moin, mediaID, want string) {
	t.Helper()
	if len(post.Media) != 1 || post.Media[0].ID != mediaID || post.Media[0].AltText != want {
		t.Fatalf("post %s media = %+v, want %s alt %q", post.ID, post.Media, mediaID, want)
	}
}

func assertPostMediaList(t *testing.T, post model.Moin, wantIDs []string, wantAlt map[string]string) {
	t.Helper()
	if len(post.Media) != len(wantIDs) {
		t.Fatalf("post %s media = %+v, want IDs %v", post.ID, post.Media, wantIDs)
	}
	for index, wantID := range wantIDs {
		if post.Media[index].ID != wantID || post.Media[index].AltText != wantAlt[wantID] {
			t.Fatalf("post %s media[%d] = %+v, want %s alt %q", post.ID, index, post.Media[index], wantID, wantAlt[wantID])
		}
	}
}
