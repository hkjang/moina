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

// A Moim the caller may not join (missing slug, private) and a failed INSERT are
// different outcomes: the first is the caller's mistake and stays 404 not_found, the
// second is a storage problem the operator must be able to tell apart, so it must be
// 500 storage_error like the leave path already reports.
func TestPostgreSQLJoinMoimSeparatesStorageErrorFromNotFound(t *testing.T) {
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
	ownerID := fmt.Sprintf("usr_join_owner_%d", suffix)
	joinerID := fmt.Sprintf("usr_join_member_%d", suffix)
	// The trigger below makes PostgreSQL refuse exactly this user's membership row, so
	// the request travels the real handler → pgx → server path and fails inside the
	// INSERT itself instead of through a hand-injected double.
	poisonID := fmt.Sprintf("usr_join_poison_%d", suffix)
	userIDs := []string{ownerID, joinerID, poisonID}
	publicMoimID := fmt.Sprintf("moim_join_public_%d", suffix)
	privateMoimID := fmt.Sprintf("moim_join_private_%d", suffix)
	publicSlug := fmt.Sprintf("join-public-%d", suffix)
	privateSlug := fmt.Sprintf("join-private-%d", suffix)
	missingSlug := fmt.Sprintf("join-missing-%d", suffix)
	triggerFn := fmt.Sprintf("moina_test_refuse_join_%d", suffix)
	triggerName := fmt.Sprintf("moina_test_refuse_join_trg_%d", suffix)

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
		token := fmt.Sprintf("join-token-%d-%d", index, suffix)
		csrf := fmt.Sprintf("join-csrf-%d-%d", index, suffix)
		if err := repository.CreateSession(ctx, model.Session{
			ID: fmt.Sprintf("session_join_%d_%d", index, suffix), UserID: id,
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
	if _, err := repository.Pool().Exec(ctx, `INSERT INTO moim_members(moim_id,user_id,role) VALUES($1,$2,'owner')`, publicMoimID, ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.user_id = TG_ARGV[0] THEN
				RAISE EXCEPTION 'moina test: storage refused this membership';
			END IF;
			RETURN NEW;
		END $$`, triggerFn)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(ctx, fmt.Sprintf(`CREATE TRIGGER %s BEFORE INSERT ON moim_members FOR EACH ROW EXECUTE FUNCTION %s(%s)`, triggerName, triggerFn, pgQuoteLiteral(poisonID))); err != nil {
		t.Fatal(err)
	}

	server := New(repository, secrets, "v0.1.35-test")
	handler := server.Handler()
	type apiError struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	join := func(path, userID string) (int, apiError, bool) {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, path, nil)
		request.Header.Set("X-CSRF-Token", csrfs[userID])
		request.AddCookie(&http.Cookie{Name: SessionCookie, Value: tokens[userID]})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code == http.StatusOK {
			var envelope struct {
				Data struct {
					Joined bool `json:"joined"`
				} `json:"data"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("200 본문을 읽을 수 없습니다: %s", response.Body.String())
			}
			return response.Code, apiError{}, envelope.Data.Joined
		}
		var apiErr apiError
		if err := json.Unmarshal(response.Body.Bytes(), &apiErr); err != nil {
			t.Fatalf("응답 %d 본문을 읽을 수 없습니다: %s", response.Code, response.Body.String())
		}
		return response.Code, apiErr, false
	}
	memberCount := func(moimID, userID string) int {
		t.Helper()
		var count int
		if err := repository.Pool().QueryRow(ctx, `SELECT count(*) FROM moim_members WHERE moim_id=$1 AND user_id=$2`, moimID, userID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}

	t.Run("public Moim join succeeds", func(t *testing.T) {
		code, apiErr, joined := join("/api/v1/moims/"+publicSlug+"/join", joinerID)
		if code != http.StatusOK || !joined {
			t.Fatalf("응답 = %d %+v joined=%v", code, apiErr, joined)
		}
		if got := memberCount(publicMoimID, joinerID); got != 1 {
			t.Fatalf("멤버 행 수 = %d", got)
		}
	})

	t.Run("re-join of an existing member stays 200", func(t *testing.T) {
		code, apiErr, joined := join("/api/v1/moims/"+publicSlug+"/join", joinerID)
		if code != http.StatusOK || !joined {
			t.Fatalf("응답 = %d %+v joined=%v", code, apiErr, joined)
		}
		if got := memberCount(publicMoimID, joinerID); got != 1 {
			t.Fatalf("멤버 행 수 = %d", got)
		}
	})

	t.Run("owner re-join via members alias stays 200", func(t *testing.T) {
		code, apiErr, joined := join("/api/v1/moims/"+publicSlug+"/members", ownerID)
		if code != http.StatusOK || !joined {
			t.Fatalf("응답 = %d %+v joined=%v", code, apiErr, joined)
		}
	})

	t.Run("missing slug stays 404 not_found", func(t *testing.T) {
		code, apiErr, _ := join("/api/v1/moims/"+missingSlug+"/join", joinerID)
		if code != http.StatusNotFound || apiErr.Code != "not_found" || apiErr.Message != "가입할 수 있는 공개 Moim을 찾을 수 없습니다" {
			t.Fatalf("응답 = %d %+v", code, apiErr)
		}
	})

	t.Run("private Moim stays 404 not_found", func(t *testing.T) {
		code, apiErr, _ := join("/api/v1/moims/"+privateSlug+"/join", joinerID)
		if code != http.StatusNotFound || apiErr.Code != "not_found" || apiErr.Message != "가입할 수 있는 공개 Moim을 찾을 수 없습니다" {
			t.Fatalf("응답 = %d %+v", code, apiErr)
		}
		if got := memberCount(privateMoimID, joinerID); got != 0 {
			t.Fatalf("비공개 Moim에 멤버 행이 생겼습니다: %d", got)
		}
	})

	t.Run("failed INSERT is 500 storage_error", func(t *testing.T) {
		code, apiErr, _ := join("/api/v1/moims/"+publicSlug+"/join", poisonID)
		if code != http.StatusInternalServerError || apiErr.Code != "storage_error" {
			t.Fatalf("저장 오류가 감춰졌습니다: %d %+v", code, apiErr)
		}
		if got := memberCount(publicMoimID, poisonID); got != 0 {
			t.Fatalf("실패한 가입이 남았습니다: %d", got)
		}
	})

	t.Run("failed INSERT via members alias is 500 storage_error", func(t *testing.T) {
		code, apiErr, _ := join("/api/v1/moims/"+publicSlug+"/members", poisonID)
		if code != http.StatusInternalServerError || apiErr.Code != "storage_error" {
			t.Fatalf("저장 오류가 감춰졌습니다: %d %+v", code, apiErr)
		}
	})
}
