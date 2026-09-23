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

// Leaving deletes no row for four different reasons: the caller owns the Moim, the
// slug does not exist, the caller already left, and the DELETE itself failed. Reporting
// all of them as 409 owner_cannot_leave shows "Moim 소유자는 나갈 수 없습니다" to a member
// who merely pressed 나가기 twice, because the front end renders the server message as
// is, so each reason must carry its own status.
func TestPostgreSQLLeaveMoimSeparatesNotFoundFromOwner(t *testing.T) {
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
	ownerID := fmt.Sprintf("usr_leave_owner_%d", suffix)
	memberID := fmt.Sprintf("usr_leave_member_%d", suffix)
	strangerID := fmt.Sprintf("usr_leave_stranger_%d", suffix)
	// The trigger below makes PostgreSQL refuse exactly this user's deletion, so the
	// request travels the real handler → pgx → server path and fails inside the DELETE
	// itself instead of through a hand-injected double.
	poisonID := fmt.Sprintf("usr_leave_poison_%d", suffix)
	userIDs := []string{ownerID, memberID, strangerID, poisonID}
	publicMoimID := fmt.Sprintf("moim_leave_public_%d", suffix)
	privateMoimID := fmt.Sprintf("moim_leave_private_%d", suffix)
	publicSlug := fmt.Sprintf("leave-public-%d", suffix)
	privateSlug := fmt.Sprintf("leave-private-%d", suffix)
	missingSlug := fmt.Sprintf("leave-missing-%d", suffix)
	triggerFn := fmt.Sprintf("moina_test_refuse_leave_%d", suffix)
	triggerName := fmt.Sprintf("moina_test_refuse_leave_trg_%d", suffix)

	t.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = repository.Pool().Exec(cleanupContext, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON moim_members`, triggerName))
		_, _ = repository.Pool().Exec(cleanupContext, fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, triggerFn))
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM outbox_events WHERE aggregate_id=ANY($1::text[])`, userIDs)
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM moims WHERE id=ANY($1::text[])`, []string{publicMoimID, privateMoimID})
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM sessions WHERE user_id=ANY($1::text[])`, userIDs)
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM audit_events WHERE actor_id=ANY($1::text[])`, userIDs)
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM users WHERE id=ANY($1::text[])`, userIDs)
	})

	for _, id := range userIDs {
		username := fmt.Sprintf("u%s", id[len("usr_"):])
		if _, err := repository.Pool().Exec(ctx, `INSERT INTO users(id,username,display_name,roles) VALUES($1,$2,$2,ARRAY['member']::text[])`, id, username); err != nil {
			t.Fatal(err)
		}
	}
	tokens := map[string]string{}
	csrfs := map[string]string{}
	for index, id := range userIDs {
		token := fmt.Sprintf("leave-token-%d-%d", index, suffix)
		csrf := fmt.Sprintf("leave-csrf-%d-%d", index, suffix)
		if err := repository.CreateSession(ctx, model.Session{
			ID: fmt.Sprintf("session_leave_%d_%d", index, suffix), UserID: id,
			TokenHash: secrets.HashToken(token), CSRFHash: secrets.HashToken(csrf),
			ExpiresAt: time.Now().UTC().Add(time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
		tokens[id] = token
		csrfs[id] = csrf
	}
	if _, err := repository.Pool().Exec(ctx, `INSERT INTO moims(id,slug,name,owner_id,visibility) VALUES
		($1,$2,'공개 Moim',$5,'public'),
		($3,$4,'비공개 Moim',$5,'private')`,
		publicMoimID, publicSlug, privateMoimID, privateSlug, ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(ctx, `INSERT INTO moim_members(moim_id,user_id,role) VALUES
		($1,$3,'owner'),($2,$3,'owner'),
		($1,$4,'member'),($2,$4,'member'),
		($1,$5,'member')`,
		publicMoimID, privateMoimID, ownerID, memberID, poisonID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF OLD.user_id = TG_ARGV[0] THEN
				RAISE EXCEPTION 'moina test: storage refused this departure';
			END IF;
			RETURN OLD;
		END $$`, triggerFn)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(ctx, fmt.Sprintf(`CREATE TRIGGER %s BEFORE DELETE ON moim_members FOR EACH ROW EXECUTE FUNCTION %s(%s)`, triggerName, triggerFn, pgQuoteLiteral(poisonID))); err != nil {
		t.Fatal(err)
	}

	server := New(repository, secrets, "v0.1.36-test")
	handler := server.Handler()
	type apiError struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	leave := func(path, userID string) (int, apiError) {
		t.Helper()
		request := httptest.NewRequest(http.MethodDelete, path, nil)
		request.Header.Set("X-CSRF-Token", csrfs[userID])
		request.AddCookie(&http.Cookie{Name: SessionCookie, Value: tokens[userID]})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code == http.StatusNoContent {
			if body := response.Body.String(); body != "" {
				t.Fatalf("204에 본문이 있습니다: %s", body)
			}
			return response.Code, apiError{}
		}
		var apiErr apiError
		if err := json.Unmarshal(response.Body.Bytes(), &apiErr); err != nil {
			t.Fatalf("응답 %d 본문을 읽을 수 없습니다: %s", response.Code, response.Body.String())
		}
		return response.Code, apiErr
	}
	memberCount := func(moimID, userID string) int {
		t.Helper()
		var count int
		if err := repository.Pool().QueryRow(ctx, `SELECT count(*) FROM moim_members WHERE moim_id=$1 AND user_id=$2`, moimID, userID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}

	t.Run("member leaving a public Moim stays 204", func(t *testing.T) {
		code, apiErr := leave("/api/v1/moims/"+publicSlug+"/join", memberID)
		if code != http.StatusNoContent {
			t.Fatalf("응답 = %d %+v", code, apiErr)
		}
		if got := memberCount(publicMoimID, memberID); got != 0 {
			t.Fatalf("멤버 행이 남았습니다: %d", got)
		}
	})

	t.Run("member leaving a private Moim stays 204", func(t *testing.T) {
		code, apiErr := leave("/api/v1/moims/"+privateSlug+"/members", memberID)
		if code != http.StatusNoContent {
			t.Fatalf("응답 = %d %+v", code, apiErr)
		}
		if got := memberCount(privateMoimID, memberID); got != 0 {
			t.Fatalf("멤버 행이 남았습니다: %d", got)
		}
	})

	t.Run("owner stays 409 owner_cannot_leave", func(t *testing.T) {
		code, apiErr := leave("/api/v1/moims/"+publicSlug+"/join", ownerID)
		if code != http.StatusConflict || apiErr.Code != "owner_cannot_leave" || apiErr.Message != "Moim 소유자는 나갈 수 없습니다" {
			t.Fatalf("응답 = %d %+v", code, apiErr)
		}
		if got := memberCount(publicMoimID, ownerID); got != 1 {
			t.Fatalf("소유자 행이 사라졌습니다: %d", got)
		}
	})

	t.Run("owner of a private Moim stays 409 owner_cannot_leave", func(t *testing.T) {
		code, apiErr := leave("/api/v1/moims/"+privateSlug+"/members", ownerID)
		if code != http.StatusConflict || apiErr.Code != "owner_cannot_leave" || apiErr.Message != "Moim 소유자는 나갈 수 없습니다" {
			t.Fatalf("응답 = %d %+v", code, apiErr)
		}
	})

	t.Run("missing slug is 404 not_found", func(t *testing.T) {
		code, apiErr := leave("/api/v1/moims/"+missingSlug+"/join", memberID)
		if code != http.StatusNotFound || apiErr.Code != "not_found" || apiErr.Message != "Moim을 찾을 수 없습니다" {
			t.Fatalf("응답 = %d %+v", code, apiErr)
		}
	})

	t.Run("leaving a public Moim twice is 204", func(t *testing.T) {
		code, apiErr := leave("/api/v1/moims/"+publicSlug+"/join", memberID)
		if code != http.StatusNoContent {
			t.Fatalf("응답 = %d %+v", code, apiErr)
		}
		if got := memberCount(publicMoimID, memberID); got != 0 {
			t.Fatalf("멤버 행이 되살아났습니다: %d", got)
		}
	})

	t.Run("non-member of a public Moim is 204", func(t *testing.T) {
		code, apiErr := leave("/api/v1/moims/"+publicSlug+"/members", strangerID)
		if code != http.StatusNoContent {
			t.Fatalf("응답 = %d %+v", code, apiErr)
		}
		if got := memberCount(publicMoimID, strangerID); got != 0 {
			t.Fatalf("비회원에게 멤버 행이 생겼습니다: %d", got)
		}
	})

	t.Run("non-member of a private Moim is 404 not_found", func(t *testing.T) {
		code, apiErr := leave("/api/v1/moims/"+privateSlug+"/join", strangerID)
		if code != http.StatusNotFound || apiErr.Code != "not_found" || apiErr.Message != "Moim을 찾을 수 없습니다" {
			t.Fatalf("응답 = %d %+v", code, apiErr)
		}
	})

	t.Run("failed DELETE stays 500 storage_error", func(t *testing.T) {
		code, apiErr := leave("/api/v1/moims/"+publicSlug+"/join", poisonID)
		if code != http.StatusInternalServerError || apiErr.Code != "storage_error" || apiErr.Message != "Moim에서 나갈 수 없습니다" {
			t.Fatalf("저장 오류가 감춰졌습니다: %d %+v", code, apiErr)
		}
		if got := memberCount(publicMoimID, poisonID); got != 1 {
			t.Fatalf("실패한 탈퇴가 행을 지웠습니다: %d", got)
		}
	})
}
