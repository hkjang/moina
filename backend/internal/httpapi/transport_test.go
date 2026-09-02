package httpapi

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func jsonHandler(body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = io.WriteString(w, body)
	})
}

func TestCompressResponsesEncodesJSON(t *testing.T) {
	body := strings.Repeat(`{"moin":"압축 대상"},`, 200)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/feed", nil)
	request.Header.Set("Accept-Encoding", "gzip")
	response := httptest.NewRecorder()
	compressResponses(jsonHandler(body)).ServeHTTP(response, request)

	if encoding := response.Header().Get("Content-Encoding"); encoding != "gzip" {
		t.Fatalf("Content-Encoding=%q, gzip을 기대했습니다", encoding)
	}
	if vary := response.Header().Get("Vary"); !strings.Contains(vary, "Accept-Encoding") {
		t.Fatalf("Vary=%q, Accept-Encoding을 포함해야 합니다", vary)
	}
	if response.Header().Get("Content-Length") != "" {
		t.Fatal("압축 응답은 원본 Content-Length를 남기면 안 됩니다")
	}
	if response.Body.Len() >= len(body) {
		t.Fatalf("압축 후 %d바이트로 원본 %d바이트보다 작지 않습니다", response.Body.Len(), len(body))
	}
	reader, err := gzip.NewReader(response.Body)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("gzip 본문 읽기: %v", err)
	}
	if string(decoded) != body {
		t.Fatal("압축 해제 결과가 원본과 다릅니다")
	}
}

func TestCompressResponsesSkipsRangeRequests(t *testing.T) {
	// A range response numbers its bytes against the identity encoding, so the
	// middleware must step aside rather than describe the wrong bytes.
	request := httptest.NewRequest(http.MethodGet, "/api/v1/media/media_1", nil)
	request.Header.Set("Accept-Encoding", "gzip")
	request.Header.Set("Range", "bytes=0-99")
	response := httptest.NewRecorder()
	compressResponses(jsonHandler(strings.Repeat("a", 5000))).ServeHTTP(response, request)

	if encoding := response.Header().Get("Content-Encoding"); encoding != "" {
		t.Fatalf("Range 요청이 %q로 인코딩되었습니다", encoding)
	}
}

func TestCompressResponsesLeavesStreamsAndMediaAlone(t *testing.T) {
	for _, contentType := range []string{"text/event-stream", "video/mp4", "image/png"} {
		t.Run(contentType, func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", contentType)
				_, _ = io.WriteString(w, strings.Repeat("data: 압축하면 안 됩니다\n\n", 200))
			})
			request := httptest.NewRequest(http.MethodGet, "/api/v1/ai/chat", nil)
			request.Header.Set("Accept-Encoding", "gzip")
			response := httptest.NewRecorder()
			compressResponses(handler).ServeHTTP(response, request)
			if encoding := response.Header().Get("Content-Encoding"); encoding != "" {
				t.Fatalf("%s가 %q로 인코딩되었습니다", contentType, encoding)
			}
		})
	}
}

func TestBodyReadDeadlineAtMatchesTheRequestShape(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		method  string
		path    string
		upgrade string
		want    time.Time
	}{
		{name: "WebSocket 업그레이드는 기한 없음", method: http.MethodGet, path: "/api/v1/ws/notifications", upgrade: "websocket", want: time.Time{}},
		{name: "대소문자 무시", method: http.MethodGet, path: "/api/v1/ws", upgrade: "WebSocket", want: time.Time{}},
		{name: "미디어 업로드는 긴 기한", method: http.MethodPost, path: "/api/v1/media", want: now.Add(uploadBodyReadTimeout)},
		{name: "미디어 GET은 기본 기한", method: http.MethodGet, path: "/api/v1/media/media_1", want: now.Add(requestBodyReadTimeout)},
		{name: "일반 POST는 기본 기한", method: http.MethodPost, path: "/api/v1/posts", want: now.Add(requestBodyReadTimeout)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, nil)
			if test.upgrade != "" {
				request.Header.Set("Upgrade", test.upgrade)
			}
			if got := bodyReadDeadlineAt(request, now); !got.Equal(test.want) {
				t.Fatalf("deadline=%v, %v를 기대했습니다", got, test.want)
			}
		})
	}
}

func TestUploadDeadlineCoversTheAdministratorCeiling(t *testing.T) {
	// The largest upload validateMedia accepts must be reachable over a link
	// slow enough to be realistic, otherwise the deadline is the real limit.
	const maxUploadBytes = 50 << 20
	slowestSupportedBytesPerSecond := float64(maxUploadBytes) / uploadBodyReadTimeout.Seconds()
	if kbps := slowestSupportedBytesPerSecond * 8 / 1000; kbps > 500 {
		t.Fatalf("50 MiB 업로드에 %.0f kbps가 필요합니다. 업로드 기한이 너무 짧습니다", kbps)
	}
}
