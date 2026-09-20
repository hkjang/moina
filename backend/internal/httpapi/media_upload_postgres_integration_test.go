package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/hkjang/moina/backend/internal/model"
	"github.com/hkjang/moina/backend/internal/secure"
	"github.com/hkjang/moina/backend/internal/store"
)

// 업로드 응답의 filename과 GET /media/{id}의 Content-Disposition은 브라우저가 보낸
// 이름이 아니라 서버가 판정한 MIME의 확장자를 따라야 합니다. 대역 mediastore 없이
// 실제 New(repository…) 배선·세션 cookie·CSRF·pgx를 지나는 multipart 업로드로 확인합니다.
func TestPostgreSQLUploadMediaFilenameFollowsDetectedMIME(t *testing.T) {
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
	secrets, err := secure.New(bytes.Repeat([]byte{61}, 32))
	if err != nil {
		t.Fatal(err)
	}

	suffix := time.Now().UnixNano()
	ownerID := fmt.Sprintf("usr_media_upload_owner_%d", suffix)
	token := fmt.Sprintf("media-upload-token-%d", suffix)
	csrf := fmt.Sprintf("media-upload-csrf-%d", suffix)

	if _, err := repository.Pool().Exec(ctx, `INSERT INTO users(id,username,display_name,roles)
		VALUES($1,$1,$1,ARRAY['member']::text[])`, ownerID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM audit_events WHERE actor_id=$1`, ownerID)
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM media_assets WHERE owner_id=$1`, ownerID)
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM sessions WHERE user_id=$1`, ownerID)
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM users WHERE id=$1`, ownerID)
	})
	if err := repository.CreateSession(ctx, model.Session{
		ID: fmt.Sprintf("session_media_upload_%d", suffix), UserID: ownerID,
		TokenHash: secrets.HashToken(token), CSRFHash: secrets.HashToken(csrf),
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	var pngBytes bytes.Buffer
	if err := png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	// ftyp box의 major brand가 isom인 최소 MP4 머리 — detectMediaType이 video/mp4로 판정합니다.
	mp4Bytes := append([]byte{0, 0, 0, 24}, []byte("ftypisom")...)
	mp4Bytes = append(mp4Bytes, []byte{0, 0, 2, 0}...)
	mp4Bytes = append(mp4Bytes, []byte("isomiso2mp41")...)

	server := New(repository, secrets, "v0.1.33-test")
	handler := server.Handler()
	upload := func(sentFilename string, body []byte) model.Media {
		t.Helper()
		var form bytes.Buffer
		writer := multipart.NewWriter(&form)
		part, err := writer.CreateFormFile("file", sentFilename)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(body); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "/api/v1/media", &form)
		request.Header.Set("Content-Type", writer.FormDataContentType())
		request.Header.Set("X-CSRF-Token", csrf)
		request.AddCookie(&http.Cookie{Name: SessionCookie, Value: token})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusCreated {
			t.Fatalf("POST /media (filename=%q) = %d: %s", sentFilename, response.Code, response.Body.String())
		}
		var envelope struct {
			Data model.Media `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("업로드 응답을 읽을 수 없습니다: %s", response.Body.String())
		}
		return envelope.Data
	}
	download := func(mediaID string) http.Header {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/api/v1/media/"+mediaID, nil)
		request.AddCookie(&http.Cookie{Name: SessionCookie, Value: token})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("GET /media/%s = %d: %s", mediaID, response.Code, response.Body.String())
		}
		return response.Header()
	}

	for _, testCase := range []struct {
		name            string
		sentFilename    string
		body            []byte
		wantMIME        string
		wantType        string
		wantFilename    string
		wantDisposition string
	}{
		{
			name: "photo.jpg라고 보낸 PNG", sentFilename: "photo.jpg", body: pngBytes.Bytes(),
			wantMIME: "image/png", wantType: "image", wantFilename: "photo.png", wantDisposition: `inline; filename="photo.png"`,
		},
		{
			// net/http는 filename=""인 part를 파일이 아니라 값으로 읽어 file_required가 되므로,
			// 이름이 비는 경로는 filepath.Base가 그대로 돌려주는 ".."로 지납니다.
			name: "이름이 ..인 PNG", sentFilename: "..", body: pngBytes.Bytes(),
			wantMIME: "image/png", wantType: "image", wantFilename: "image.png", wantDisposition: `inline; filename="image.png"`,
		},
		{
			name: "clip.mov라고 보낸 MP4", sentFilename: "clip.mov", body: mp4Bytes,
			wantMIME: "video/mp4", wantType: "video", wantFilename: "clip.mp4", wantDisposition: `inline; filename="clip.mp4"`,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			media := upload(testCase.sentFilename, testCase.body)
			if media.MIMEType != testCase.wantMIME || media.Type != testCase.wantType {
				t.Fatalf("mimeType=%q type=%q, want %q/%q", media.MIMEType, media.Type, testCase.wantMIME, testCase.wantType)
			}
			if media.Filename != testCase.wantFilename {
				t.Fatalf("응답 filename=%q, want=%q", media.Filename, testCase.wantFilename)
			}
			var storedFilename string
			if err := repository.Pool().QueryRow(ctx, `SELECT filename FROM media_assets WHERE id=$1`, media.ID).Scan(&storedFilename); err != nil {
				t.Fatal(err)
			}
			if storedFilename != testCase.wantFilename {
				t.Fatalf("저장된 filename=%q, want=%q", storedFilename, testCase.wantFilename)
			}
			headers := download(media.ID)
			if got := headers.Get("Content-Disposition"); got != testCase.wantDisposition {
				t.Fatalf("Content-Disposition=%q, want=%q", got, testCase.wantDisposition)
			}
			if got := headers.Get("Content-Type"); got != testCase.wantMIME {
				t.Fatalf("Content-Type=%q, want=%q", got, testCase.wantMIME)
			}
		})
	}
}
