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

	"github.com/hkjang/moina/backend/internal/model"
	"github.com/hkjang/moina/backend/internal/secure"
	"github.com/hkjang/moina/backend/internal/store"
)

// A Topic that does not exist and a failed INSERT are different outcomes: the first is
// the caller's mistake and stays 404 not_found, the second is a storage problem the
// operator must be able to tell apart, so it must be 500 storage_error like the
// unfollow path in the same file already reports.
func TestPostgreSQLFollowTopicSeparatesStorageErrorFromNotFound(t *testing.T) {
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
	followerID := fmt.Sprintf("usr_follow_member_%d", suffix)
	// The trigger below makes PostgreSQL refuse exactly this user's follow row, so the
	// request travels the real handler → pgx → server path and fails inside the INSERT
	// itself instead of through a hand-injected double.
	poisonID := fmt.Sprintf("usr_follow_poison_%d", suffix)
	userIDs := []string{followerID, poisonID}
	topicID := fmt.Sprintf("top_follow_%d", suffix)
	topicSlug := fmt.Sprintf("follow-topic-%d", suffix)
	missingSlug := fmt.Sprintf("follow-missing-%d", suffix)
	triggerFn := fmt.Sprintf("moina_test_refuse_follow_%d", suffix)
	triggerName := fmt.Sprintf("moina_test_refuse_follow_trg_%d", suffix)

	t.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = repository.Pool().Exec(cleanupContext, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON user_topic_follows`, triggerName))
		_, _ = repository.Pool().Exec(cleanupContext, fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, triggerFn))
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM outbox_events WHERE aggregate_id=ANY($1::text[])`, userIDs)
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM user_topic_follows WHERE user_id=ANY($1::text[])`, userIDs)
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM topics WHERE id=$1`, topicID)
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
		token := fmt.Sprintf("follow-token-%d-%d", index, suffix)
		csrf := fmt.Sprintf("follow-csrf-%d-%d", index, suffix)
		if err := repository.CreateSession(ctx, model.Session{
			ID: fmt.Sprintf("session_follow_%d_%d", index, suffix), UserID: id,
			TokenHash: secrets.HashToken(token), CSRFHash: secrets.HashToken(csrf),
			ExpiresAt: time.Now().UTC().Add(time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
		tokens[id] = token
		csrfs[id] = csrf
	}
	if _, err := repository.Pool().Exec(ctx, `INSERT INTO topics(id,slug,name) VALUES($1,$2,'Link 대상 Topic')`, topicID, topicSlug); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.user_id = TG_ARGV[0] THEN
				RAISE EXCEPTION 'moina test: storage refused this Topic Link';
			END IF;
			RETURN NEW;
		END $$`, triggerFn)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(ctx, fmt.Sprintf(`CREATE TRIGGER %s BEFORE INSERT ON user_topic_follows FOR EACH ROW EXECUTE FUNCTION %s(%s)`, triggerName, triggerFn, pgQuoteLiteral(poisonID))); err != nil {
		t.Fatal(err)
	}

	server := New(repository, secrets, "v0.1.37-test")
	handler := server.Handler()
	type apiError struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	type followResult struct {
		Following bool `json:"following"`
		Weight    int  `json:"weight"`
	}
	follow := func(slug, userID, body string) (int, apiError, followResult) {
		t.Helper()
		var reader *strings.Reader
		if body == "" {
			reader = strings.NewReader("")
		} else {
			reader = strings.NewReader(body)
		}
		request := httptest.NewRequest(http.MethodPost, "/api/v1/topics/"+slug+"/follow", reader)
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		request.Header.Set("X-CSRF-Token", csrfs[userID])
		request.AddCookie(&http.Cookie{Name: SessionCookie, Value: tokens[userID]})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code == http.StatusOK {
			var envelope struct {
				Data followResult `json:"data"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("200 본문을 읽을 수 없습니다: %s", response.Body.String())
			}
			return response.Code, apiError{}, envelope.Data
		}
		var apiErr apiError
		if err := json.Unmarshal(response.Body.Bytes(), &apiErr); err != nil {
			t.Fatalf("응답 %d 본문을 읽을 수 없습니다: %s", response.Code, response.Body.String())
		}
		return response.Code, apiErr, followResult{}
	}
	storedWeight := func(userID string) (int, bool) {
		t.Helper()
		rows, err := repository.Pool().Query(ctx, `SELECT weight FROM user_topic_follows WHERE user_id=$1 AND topic_id=$2`, userID, topicID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		if !rows.Next() {
			return 0, false
		}
		var weight int
		if err := rows.Scan(&weight); err != nil {
			t.Fatal(err)
		}
		return weight, true
	}

	t.Run("new Topic Link succeeds with the default weight", func(t *testing.T) {
		code, apiErr, result := follow(topicSlug, followerID, "")
		if code != http.StatusOK || !result.Following || result.Weight != 50 {
			t.Fatalf("응답 = %d %+v %+v", code, apiErr, result)
		}
		if weight, ok := storedWeight(followerID); !ok || weight != 50 {
			t.Fatalf("저장된 가중치 = %d (있음=%v)", weight, ok)
		}
	})

	t.Run("existing Topic Link updates the weight", func(t *testing.T) {
		code, apiErr, result := follow(topicSlug, followerID, `{"weight":80}`)
		if code != http.StatusOK || !result.Following || result.Weight != 80 {
			t.Fatalf("응답 = %d %+v %+v", code, apiErr, result)
		}
		if weight, ok := storedWeight(followerID); !ok || weight != 80 {
			t.Fatalf("저장된 가중치 = %d (있음=%v)", weight, ok)
		}
	})

	t.Run("weight outside 1~100 stays 400 invalid_weight", func(t *testing.T) {
		code, apiErr, _ := follow(topicSlug, followerID, `{"weight":101}`)
		if code != http.StatusBadRequest || apiErr.Code != "invalid_weight" {
			t.Fatalf("응답 = %d %+v", code, apiErr)
		}
		if weight, ok := storedWeight(followerID); !ok || weight != 80 {
			t.Fatalf("거절된 요청이 가중치를 바꿨습니다: %d (있음=%v)", weight, ok)
		}
	})

	t.Run("missing slug stays 404 not_found", func(t *testing.T) {
		code, apiErr, _ := follow(missingSlug, followerID, "")
		if code != http.StatusNotFound || apiErr.Code != "not_found" || apiErr.Message != "Topic을 찾을 수 없습니다" {
			t.Fatalf("응답 = %d %+v", code, apiErr)
		}
	})

	t.Run("failed INSERT is 500 storage_error", func(t *testing.T) {
		code, apiErr, _ := follow(topicSlug, poisonID, "")
		if code != http.StatusInternalServerError || apiErr.Code != "storage_error" {
			t.Fatalf("저장 오류가 감춰졌습니다: %d %+v", code, apiErr)
		}
		if weight, ok := storedWeight(poisonID); ok {
			t.Fatalf("실패한 Link가 남았습니다: 가중치 %d", weight)
		}
	})
}
