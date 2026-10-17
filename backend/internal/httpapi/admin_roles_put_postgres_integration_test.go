package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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

// adminPutRoles writes through five statements — the transaction, the roles
// upsert, the role_permissions delete and insert, and the commit — and every
// one of them answered 500 "역할 정책을 저장할 수 없습니다" with the pg error
// discarded. An operator reading that line could not tell which of the five
// gave way, which is the whole question when a role policy will not save.
//
// The request below goes through the production wiring — New(repository…) and
// server.Handler(), a real sessions row, the CSRF header and pgx — and the
// storage failure is produced by a test-only trigger that refuses the
// role_permissions rows of one sentinel role, not by a stand-in repository.
func TestPostgreSQLAdminPutRolesLeavesTheRefusedWriteInTheOperatorLog(t *testing.T) {
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
	secrets, err := secure.New(bytes.Repeat([]byte{62}, 32))
	if err != nil {
		t.Fatal(err)
	}

	suffix := time.Now().UnixNano()
	adminID := fmt.Sprintf("usr_roles_admin_%d", suffix)
	// Role names have to match roleNamePattern, so the suffix goes in as part
	// of a lower-case identifier.
	okRole := fmt.Sprintf("moina_test_role_ok_%d", suffix)
	poisonRole := fmt.Sprintf("moina_test_role_poison_%d", suffix)
	testRoles := []string{okRole, poisonRole}
	token := fmt.Sprintf("roles-token-%d", suffix)
	csrf := fmt.Sprintf("roles-csrf-%d", suffix)
	triggerFn := fmt.Sprintf("moina_test_roles_put_%d", suffix)
	triggerName := fmt.Sprintf("moina_test_roles_put_trg_%d", suffix)
	// super_admin is seeded as a system role and adminPutRoles refuses a
	// payload without it, so it is sent back with the description it already
	// carries and the only permission it is allowed to hold.
	const superAdminDescription = "전체 서비스 관리"

	t.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = repository.Pool().Exec(cleanupContext, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON role_permissions`, triggerName))
		_, _ = repository.Pool().Exec(cleanupContext, fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, triggerFn))
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM role_permissions WHERE role_name=ANY($1::text[])`, testRoles)
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM roles WHERE name=ANY($1::text[])`, testRoles)
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM sessions WHERE user_id=$1`, adminID)
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM audit_events WHERE actor_id=$1`, adminID)
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM users WHERE id=$1`, adminID)
	})

	// The seeded admin role carries roles:manage, which is what the
	// PUT /admin/roles route requires, and audit:read, which is the permission
	// the payloads below grant — adminPutRoles refuses to grant a permission
	// the acting administrator does not already hold.
	if _, err := repository.Pool().Exec(ctx, `INSERT INTO users(id,username,display_name,roles) VALUES($1,$2,$2,ARRAY['admin']::text[])`, adminID, fmt.Sprintf("u%s", adminID[len("usr_"):])); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateSession(ctx, model.Session{
		ID: fmt.Sprintf("session_roles_%d", suffix), UserID: adminID,
		TokenHash: secrets.HashToken(token), CSRFHash: secrets.HashToken(csrf),
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	// The CI source database is shared by four packages, so the trigger
	// refuses exactly one sentinel role name and nothing else, and it is
	// dropped again above. Keying it on role_name rather than on a permission
	// keeps every real permission value insertable for everyone else.
	if _, err := repository.Pool().Exec(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.role_name = TG_ARGV[0] THEN
				RAISE EXCEPTION 'moina test: storage refused this role permission';
			END IF;
			RETURN NEW;
		END $$`, triggerFn)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(ctx, fmt.Sprintf(`CREATE TRIGGER %s BEFORE INSERT ON role_permissions FOR EACH ROW EXECUTE FUNCTION %s(%s)`, triggerName, triggerFn, pgQuoteLiteral(poisonRole))); err != nil {
		t.Fatal(err)
	}

	// Handler() captures slog.Default() for the request-scoped logger, so this
	// is what lets the storage failure below be read back as the operator
	// would see it. Nothing in this package runs in parallel, and the writer is
	// guarded because the access log line is written from the same chain.
	logs := &lockedBuffer{}
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	server := New(repository, secrets, "v0.1.38-test")
	handler := server.Handler()
	type apiError struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	put := func(t *testing.T, body string) (int, []byte) {
		t.Helper()
		logs.reset()
		request := httptest.NewRequest(http.MethodPut, "/api/v1/admin/roles", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-CSRF-Token", csrf)
		request.AddCookie(&http.Cookie{Name: SessionCookie, Value: token})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response.Code, response.Body.Bytes()
	}
	permissionsOf := func(t *testing.T, role string) []string {
		t.Helper()
		rows, err := repository.Pool().Query(ctx, `SELECT permission FROM role_permissions WHERE role_name=$1 ORDER BY permission`, role)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		result := make([]string, 0)
		for rows.Next() {
			var permission string
			if err := rows.Scan(&permission); err != nil {
				t.Fatal(err)
			}
			result = append(result, permission)
		}
		return result
	}
	roleExists := func(t *testing.T, role string) bool {
		t.Helper()
		var count int
		if err := repository.Pool().QueryRow(ctx, `SELECT count(*) FROM roles WHERE name=$1`, role).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count > 0
	}
	entryFor := func(role string) string {
		return fmt.Sprintf(`{"name":%q,"description":"저장 실패 관측용","permissions":["audit:read"]}`, role)
	}
	payload := func(roles ...string) string {
		entries := make([]string, 0, len(roles)+1)
		entries = append(entries, fmt.Sprintf(`{"name":"super_admin","description":%q,"permissions":["*"]}`, superAdminDescription))
		for _, role := range roles {
			entries = append(entries, entryFor(role))
		}
		return `{"roles":[` + strings.Join(entries, ",") + `]}`
	}

	t.Run("a role policy that storage accepts is saved", func(t *testing.T) {
		status, body := put(t, payload(okRole))
		if status != http.StatusOK {
			t.Fatalf("status=%d 기대=200 본문=%s", status, string(body))
		}
		if got := permissionsOf(t, okRole); len(got) != 1 || got[0] != "audit:read" {
			t.Fatalf("role_permissions=%v 기대=[audit:read]", got)
		}
	})

	t.Run("a payload without super_admin stays 409 super_admin_required", func(t *testing.T) {
		status, body := put(t, `{"roles":[`+entryFor(okRole)+`]}`)
		if status != http.StatusConflict {
			t.Fatalf("status=%d 기대=409 본문=%s", status, string(body))
		}
		var failure apiError
		if err := json.Unmarshal(body, &failure); err != nil {
			t.Fatalf("오류 본문을 읽을 수 없습니다: %s", string(body))
		}
		if failure.Code != "super_admin_required" {
			t.Fatalf("code=%q 기대=%q 본문=%s", failure.Code, "super_admin_required", string(body))
		}
	})

	// A 500 with no cause in the log leaves the operator with the
	// administrator's complaint as the only clue, so the refused write has to
	// name itself and its SQLSTATE next to the request id.
	t.Run("a refused permission insert is a storage error the operator log explains", func(t *testing.T) {
		status, body := put(t, payload(poisonRole))
		if status != http.StatusInternalServerError {
			t.Fatalf("status=%d 기대=500 본문=%s", status, string(body))
		}
		var failure apiError
		if err := json.Unmarshal(body, &failure); err != nil {
			t.Fatalf("오류 본문을 읽을 수 없습니다: %s", string(body))
		}
		if failure.Code != "storage_error" {
			t.Fatalf("code=%q 기대=%q 본문=%s", failure.Code, "storage_error", string(body))
		}
		if failure.Message != "역할 정책을 저장할 수 없습니다" {
			t.Fatalf("message=%q 기대=%q", failure.Message, "역할 정책을 저장할 수 없습니다")
		}
		// The refusal rolls the whole policy write back, so the role the
		// request tried to create must not be left behind.
		if roleExists(t, poisonRole) {
			t.Errorf("거절된 역할 %s이 롤백되지 않았습니다", poisonRole)
		}
		written := logs.String()
		for _, field := range []string{
			`"error_code":"storage_error"`,
			`"handler":"adminPutRoles"`,
			`"cause_type":"*pgconn.PgError"`,
			`"pg_code":"P0001"`,
		} {
			if !strings.Contains(written, field) {
				t.Errorf("운영자 로그에 %s가 없습니다: %s", field, written)
			}
		}
		if !strings.Contains(written, `"request_id":"`) || strings.Contains(written, `"request_id":""`) {
			t.Errorf("운영자 로그에 request_id가 없습니다: %s", written)
		}
		// The pg message quotes the offending row, which is where a protected
		// value would leak, so only the type and the SQLSTATE are logged.
		if strings.Contains(written, "moina test: storage refused") {
			t.Errorf("pg 메시지 전문이 로그에 노출되었습니다: %s", written)
		}
	})
}
