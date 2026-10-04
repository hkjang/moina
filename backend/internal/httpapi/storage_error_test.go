package httpapi

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hkjang/moina/backend/internal/observability"
	"github.com/jackc/pgx/v5/pgconn"
)

// lockedBuffer collects log output for the integration tests that read back
// what an operator would see. slog serializes its own writes, but the access
// log line and the handler line come from the same chain, so the buffer is
// guarded to keep -race quiet when it is read between requests.
type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func (b *lockedBuffer) reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buffer.Reset()
}

// The storage_error 500 exits used to discard err, so an operator reading
// "신고를 처리할 수 없습니다" could not tell a constraint violation from an
// exhausted connection pool. writeStorageError keeps the response byte for byte
// and adds the one log line that makes the failure investigable — without the
// pg message, which carries the values of the offending row.
func serveStorageError(t *testing.T, err error, message string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	handler := observability.HTTPMiddleware(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeStorageError(w, r, "resolveReport", err, message)
	}))
	request := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/reports/rep_1", nil)
	request.Header.Set(observability.RequestIDHeader, "storage-error-test")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)
	return response, logs.String()
}

func TestStorageErrorLogsCauseWithRequestID(t *testing.T) {
	_, logs := serveStorageError(t, fmt.Errorf("저장 실패: %w", errors.New("pool exhausted")), "신고를 처리할 수 없습니다")

	for _, field := range []string{
		`"request_id":"storage-error-test"`,
		`"error_code":"storage_error"`,
		`"handler":"resolveReport"`,
		`"cause_type":"*errors.errorString"`,
		`"pg_code":""`,
	} {
		if !strings.Contains(logs, field) {
			t.Errorf("log is missing %s: %s", field, logs)
		}
	}
}

func TestStorageErrorResponseMatchesWriteError(t *testing.T) {
	const message = "신고를 처리할 수 없습니다"
	got, _ := serveStorageError(t, errors.New("refused"), message)

	want := httptest.NewRecorder()
	writeError(want, http.StatusInternalServerError, "storage_error", message)

	if got.Code != want.Code {
		t.Fatalf("status = %d, want %d", got.Code, want.Code)
	}
	if got.Body.String() != want.Body.String() {
		t.Fatalf("body = %q, want %q", got.Body.String(), want.Body.String())
	}
	if got.Header().Get("Content-Type") != want.Header().Get("Content-Type") {
		t.Fatalf("Content-Type = %q, want %q", got.Header().Get("Content-Type"), want.Header().Get("Content-Type"))
	}
}

func TestStorageErrorDoesNotExposePostgresDetail(t *testing.T) {
	const secretDetail = "PRIVATE-ROW-VALUE"
	pgErr := &pgconn.PgError{
		Code:    "23505",
		Message: `duplicate key value violates unique constraint "users_username_key"`,
		Detail:  "Key (username)=(" + secretDetail + ") already exists.",
		Hint:    secretDetail,
	}
	response, logs := serveStorageError(t, fmt.Errorf("insert: %w", pgErr), "비밀번호를 변경할 수 없습니다")

	if strings.Contains(logs, secretDetail) || strings.Contains(response.Body.String(), secretDetail) {
		t.Fatalf("offending row value was exposed: body=%s log=%s", response.Body.String(), logs)
	}
	if strings.Contains(logs, "users_username_key") {
		t.Fatalf("pg message was exposed: %s", logs)
	}
	for _, field := range []string{`"pg_code":"23505"`, `"cause_type":"*pgconn.PgError"`} {
		if !strings.Contains(logs, field) {
			t.Errorf("log is missing %s: %s", field, logs)
		}
	}
}
