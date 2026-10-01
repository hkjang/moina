package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/hkjang/moina/backend/internal/model"
	"github.com/hkjang/moina/backend/internal/secure"
	"github.com/hkjang/moina/backend/internal/store"
)

// Both password writers discarded the result of the session revocation that
// follows them — `_ = s.repo.DeleteUserSessions(...)` — and then answered 204
// and sent the owner a security notice saying "모든 로그인 세션을 종료했습니다".
// Sessions are not tied to the password hash, so when that DELETE was refused
// every other device kept a working session while the owner was told the
// opposite, which is the one assurance someone changing a password after a
// compromise relies on. The hash and the sessions now move together.
//
// The requests below go through the production wiring — New(repository…) and
// server.Handler(), real sessions rows, the CSRF header and pgx — and the
// storage failure is produced by a test-only BEFORE DELETE trigger that refuses
// the sessions of two sentinel users, not by a stand-in repository.
func TestPostgreSQLPasswordChangeRevokesSessionsAtomically(t *testing.T) {
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

	const oldPassword = "old-password-0001"
	const newPassword = "new-password-0002"
	oldHash, err := bcrypt.GenerateFromPassword([]byte(oldPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}

	suffix := time.Now().UnixNano()
	selfOKID := fmt.Sprintf("usr_pwrev_self_ok_%d", suffix)
	selfPoisonID := fmt.Sprintf("usr_pwrev_self_poison_%d", suffix)
	adminID := fmt.Sprintf("usr_pwrev_admin_%d", suffix)
	targetOKID := fmt.Sprintf("usr_pwrev_target_ok_%d", suffix)
	targetPoisonID := fmt.Sprintf("usr_pwrev_target_poison_%d", suffix)
	targetOIDCID := fmt.Sprintf("usr_pwrev_target_oidc_%d", suffix)
	userIDs := []string{selfOKID, selfPoisonID, adminID, targetOKID, targetPoisonID, targetOIDCID}
	triggerFn := fmt.Sprintf("moina_test_pwrev_%d", suffix)
	triggerName := fmt.Sprintf("moina_test_pwrev_trg_%d", suffix)

	t.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = repository.Pool().Exec(cleanupContext, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON sessions`, triggerName))
		_, _ = repository.Pool().Exec(cleanupContext, fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, triggerFn))
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM sessions WHERE user_id=ANY($1::text[])`, userIDs)
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM outbox_events WHERE aggregate_id=ANY($1::text[])`, userIDs)
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM audit_events WHERE actor_id=ANY($1::text[])`, userIDs)
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM users WHERE id=ANY($1::text[])`, userIDs)
	})

	type account struct {
		id       string
		roles    string
		provider string
		hash     string
	}
	accounts := []account{
		{id: selfOKID, roles: "member", provider: "local", hash: string(oldHash)},
		{id: selfPoisonID, roles: "member", provider: "local", hash: string(oldHash)},
		// The seeded admin role carries users:manage, which the
		// POST /admin/users/{userID}/password route requires.
		{id: adminID, roles: "admin", provider: "local", hash: string(oldHash)},
		{id: targetOKID, roles: "member", provider: "local", hash: string(oldHash)},
		{id: targetPoisonID, roles: "member", provider: "local", hash: string(oldHash)},
		{id: targetOIDCID, roles: "member", provider: "oidc", hash: ""},
	}
	for _, seeded := range accounts {
		username := fmt.Sprintf("u%s", seeded.id[len("usr_"):])
		if _, err := repository.Pool().Exec(ctx, `INSERT INTO users(id,username,display_name,password_hash,provider,roles) VALUES($1,$2,$2,$3,$4,ARRAY[$5]::text[])`,
			seeded.id, username, seeded.hash, seeded.provider, seeded.roles); err != nil {
			t.Fatal(err)
		}
	}

	// Two sessions per account: the one the request authenticates with and one
	// standing in for another device, which is what the notice promises to end.
	tokens := map[string]string{}
	csrfs := map[string]string{}
	for index, seeded := range accounts {
		token := fmt.Sprintf("pwrev-token-%d-%d", suffix, index)
		csrf := fmt.Sprintf("pwrev-csrf-%d-%d", suffix, index)
		tokens[seeded.id] = token
		csrfs[seeded.id] = csrf
		if err := repository.CreateSession(ctx, model.Session{
			ID: fmt.Sprintf("session_pwrev_%d_%d", suffix, index), UserID: seeded.id,
			TokenHash: secrets.HashToken(token), CSRFHash: secrets.HashToken(csrf),
			ExpiresAt: time.Now().UTC().Add(time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
		if err := repository.CreateSession(ctx, model.Session{
			ID: fmt.Sprintf("session_pwrev_other_%d_%d", suffix, index), UserID: seeded.id,
			TokenHash: secrets.HashToken(token + "-other-device"), CSRFHash: secrets.HashToken(csrf),
			ExpiresAt: time.Now().UTC().Add(time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := repository.Pool().Exec(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF OLD.user_id = TG_ARGV[0] OR OLD.user_id = TG_ARGV[1] THEN
				RAISE EXCEPTION 'moina test: storage refused this session revocation';
			END IF;
			RETURN OLD;
		END $$`, triggerFn)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(ctx, fmt.Sprintf(`CREATE TRIGGER %s BEFORE DELETE ON sessions FOR EACH ROW EXECUTE FUNCTION %s(%s,%s)`,
		triggerName, triggerFn, pgQuoteLiteral(selfPoisonID), pgQuoteLiteral(targetPoisonID))); err != nil {
		t.Fatal(err)
	}

	server := New(repository, secrets, "v0.1.39-test")
	handler := server.Handler()
	type apiError struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	post := func(t *testing.T, actorID, path, body string) (int, []byte) {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-CSRF-Token", csrfs[actorID])
		request.AddCookie(&http.Cookie{Name: SessionCookie, Value: tokens[actorID]})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response.Code, response.Body.Bytes()
	}
	sessionCount := func(t *testing.T, userID string) int {
		t.Helper()
		var count int
		if err := repository.Pool().QueryRow(ctx, `SELECT count(*) FROM sessions WHERE user_id=$1`, userID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	// A security notice leaves the handler as an outbox event; the delivery
	// worker turns it into the notification row later, so the outbox is where
	// the promise "모든 로그인 세션을 종료했습니다" becomes observable.
	noticeCount := func(t *testing.T, userID string) int {
		t.Helper()
		var count int
		if err := repository.Pool().QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE event_type='notification.create' AND aggregate_id=$1 AND payload->>'type'='security'`, userID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	storedPassword := func(t *testing.T, userID string) string {
		t.Helper()
		var hash string
		if err := repository.Pool().QueryRow(ctx, `SELECT password_hash FROM users WHERE id=$1`, userID).Scan(&hash); err != nil {
			t.Fatal(err)
		}
		switch {
		case bcrypt.CompareHashAndPassword([]byte(hash), []byte(newPassword)) == nil:
			return "new"
		case bcrypt.CompareHashAndPassword([]byte(hash), []byte(oldPassword)) == nil:
			return "old"
		default:
			return "neither"
		}
	}

	cases := []struct {
		name         string
		actorID      string
		targetID     string
		path         string
		body         string
		wantStatus   int
		wantCode     string
		wantSessions int
		wantPassword string
		wantNotices  int
	}{
		{
			name: "changing my own password ends every session", actorID: selfOKID, targetID: selfOKID,
			path: "/api/v1/profile/password", body: fmt.Sprintf(`{"currentPassword":%q,"newPassword":%q}`, oldPassword, newPassword),
			wantStatus: http.StatusNoContent, wantSessions: 0, wantPassword: "new", wantNotices: 1,
		},
		{
			// The notice says every session ended, so a refused revocation
			// must leave the password alone rather than report success.
			name: "a refused revocation leaves my password unchanged", actorID: selfPoisonID, targetID: selfPoisonID,
			path: "/api/v1/profile/password", body: fmt.Sprintf(`{"currentPassword":%q,"newPassword":%q}`, oldPassword, newPassword),
			wantStatus: http.StatusInternalServerError, wantCode: "storage_error",
			wantSessions: 2, wantPassword: "old", wantNotices: 0,
		},
		{
			name: "an administrator reset ends every session", actorID: adminID, targetID: targetOKID,
			path: "/api/v1/admin/users/" + targetOKID + "/password", body: fmt.Sprintf(`{"password":%q}`, newPassword),
			wantStatus: http.StatusNoContent, wantSessions: 0, wantPassword: "new", wantNotices: 1,
		},
		{
			name: "a refused revocation leaves the reset password unchanged", actorID: adminID, targetID: targetPoisonID,
			path: "/api/v1/admin/users/" + targetPoisonID + "/password", body: fmt.Sprintf(`{"password":%q}`, newPassword),
			wantStatus: http.StatusInternalServerError, wantCode: "storage_error",
			wantSessions: 2, wantPassword: "old", wantNotices: 0,
		},
		{
			// An SSO account has no local password: the conflict answer and the
			// untouched sessions are the behaviour before this change.
			name: "resetting an SSO password is still a conflict", actorID: adminID, targetID: targetOIDCID,
			path: "/api/v1/admin/users/" + targetOIDCID + "/password", body: fmt.Sprintf(`{"password":%q}`, newPassword),
			wantStatus: http.StatusConflict, wantCode: "not_local_user",
			wantSessions: 2, wantPassword: "neither", wantNotices: 0,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			status, payload := post(t, testCase.actorID, testCase.path, testCase.body)
			if status != testCase.wantStatus {
				t.Fatalf("status=%d 기대=%d 본문=%s", status, testCase.wantStatus, string(payload))
			}
			if testCase.wantCode != "" {
				var failure apiError
				if err := json.Unmarshal(payload, &failure); err != nil {
					t.Fatalf("오류 본문을 읽을 수 없습니다: %s", string(payload))
				}
				if failure.Code != testCase.wantCode {
					t.Fatalf("code=%q 기대=%q 본문=%s", failure.Code, testCase.wantCode, string(payload))
				}
			}
			if got := sessionCount(t, testCase.targetID); got != testCase.wantSessions {
				t.Fatalf("sessions 행=%d 기대=%d", got, testCase.wantSessions)
			}
			if got := storedPassword(t, testCase.targetID); got != testCase.wantPassword {
				t.Fatalf("저장된 비밀번호=%s 기대=%s", got, testCase.wantPassword)
			}
			if got := noticeCount(t, testCase.targetID); got != testCase.wantNotices {
				t.Fatalf("보안 알림=%d 기대=%d", got, testCase.wantNotices)
			}
		})
	}
}
