package httpapi

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/hkjang/moina/backend/internal/observability"
	"github.com/jackc/pgx/v5/pgconn"
)

// writeStorageError answers a refused write exactly as writeError(w, 500,
// "storage_error", message) does — the body is what the frontend shows through
// readableError, so it stays byte for byte the same — and additionally leaves
// the cause in the operator log the way writeOIDCDiscoveryError does.
//
// The 500 exits used to discard err, so "신고를 처리할 수 없습니다" was all an
// operator ever saw and a constraint violation, an exhausted connection pool
// and a full disk were indistinguishable. Only the error type and the SQLSTATE
// are logged: the pg message, detail and hint quote the offending row, which is
// where an address or a token hash would leak.
func writeStorageError(w http.ResponseWriter, r *http.Request, handler string, err error, message string) {
	observability.Logger(r.Context()).ErrorContext(
		r.Context(),
		"저장 실패",
		"error_code", "storage_error",
		"handler", handler,
		"cause_type", deepestErrorType(err),
		"pg_code", pgSQLState(err),
	)
	writeError(w, http.StatusInternalServerError, "storage_error", message)
}

// pgSQLState is the SQLSTATE PostgreSQL refused the write with — 23505 for a
// unique violation, 23503 for a foreign key — and empty for an error that did
// not come from the server, such as a context deadline. store.IsConflict folds
// those codes into a bool, which is not enough to tell an operator which
// constraint gave way.
func pgSQLState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func deepestErrorType(err error) string {
	if err == nil {
		return "<nil>"
	}
	for {
		next := errors.Unwrap(err)
		if next == nil {
			return fmt.Sprintf("%T", err)
		}
		err = next
	}
}
