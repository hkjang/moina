package httpapi

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestUploadMediaRejectsOversizedMultipart(t *testing.T) {
	const maxUploadBytes = 64 << 10
	const bodyLimit = maxUploadBytes + (1 << 20)
	for _, tc := range []struct {
		name        string
		size        int
		missing     bool
		malformed   bool
		wantStatus  int
		wantCode    string
		wantMessage string
	}{
		{"body_limit", bodyLimit + 1, false, false, http.StatusRequestEntityTooLarge, "media_too_large", "파일이 비어 있거나 업로드 한도를 넘었습니다"},
		{"file_limit", maxUploadBytes + 1, false, false, http.StatusRequestEntityTooLarge, "media_too_large", "파일이 비어 있거나 업로드 한도를 넘었습니다"},
		{"empty_file", 0, false, false, http.StatusRequestEntityTooLarge, "media_too_large", "파일이 비어 있거나 업로드 한도를 넘었습니다"},
		{"malformed", 16, false, true, http.StatusBadRequest, "invalid_media", "업로드 파일을 읽을 수 없습니다"},
		{"missing_file", 0, true, false, http.StatusBadRequest, "file_required", "file 필드가 필요합니다"},
	} {
		for _, unknownLength := range []bool{false, true} {
			lengthName := "known_length"
			if unknownLength {
				lengthName = "unknown_length"
			}
			t.Run(tc.name+"/"+lengthName, func(t *testing.T) {
				var body bytes.Buffer
				writer := multipart.NewWriter(&body)
				if tc.missing {
					if err := writer.WriteField("altText", "description"); err != nil {
						t.Fatal(err)
					}
				} else {
					part, err := writer.CreateFormFile("file", "upload.png")
					if err != nil {
						t.Fatal(err)
					}
					if _, err := part.Write(bytes.Repeat([]byte("x"), tc.size)); err != nil {
						t.Fatal(err)
					}
				}
				// Leaving out the closing boundary makes a small, truncated multipart.
				if !tc.malformed {
					if err := writer.Close(); err != nil {
						t.Fatal(err)
					}
				}
				r := httptest.NewRequest(http.MethodPost, "/api/v1/media", &body)
				r.Header.Set("Content-Type", writer.FormDataContentType())
				if unknownLength {
					r.ContentLength = -1
				}
				s := &Server{settings: newSettingCache()}
				payload, err := json.Marshal(mediaConfig{MaxUploadBytes: maxUploadBytes, MaxPerPost: 4, OrphanTTLHours: 24})
				if err != nil {
					t.Fatal(err)
				}
				s.settings.put(settingMedia, settingEntry{payload: payload, loadedAt: time.Now()})
				w := httptest.NewRecorder()
				s.uploadMedia(w, r)
				if w.Code != tc.wantStatus {
					t.Errorf("status = %d, want %d; body = %s", w.Code, tc.wantStatus, w.Body.String())
				}
				var response struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if response.Code != tc.wantCode || response.Message != tc.wantMessage {
					t.Errorf("error = %+v, want %s / %s", response, tc.wantCode, tc.wantMessage)
				}
			})
		}
	}
}
