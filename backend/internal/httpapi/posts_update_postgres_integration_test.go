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

// A rejected edit (someone else's Moin, a Remoin) and a failed UPDATE are different
// outcomes: the first is the caller's mistake and stays 409 not_editable, the second is
// a storage problem the operator must be able to tell apart, so it must be 500
// storage_error like the delete path already reports.
func TestPostgreSQLUpdatePostSeparatesStorageErrorFromNotEditable(t *testing.T) {
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
	secrets, err := secure.New(bytes.Repeat([]byte{53}, 32))
	if err != nil {
		t.Fatal(err)
	}

	suffix := time.Now().UnixNano()
	authorID := fmt.Sprintf("usr_upd_author_%d", suffix)
	otherID := fmt.Sprintf("usr_upd_other_%d", suffix)
	authorUsername := fmt.Sprintf("upd_author_%d", suffix)
	otherUsername := fmt.Sprintf("upd_other_%d", suffix)
	ownPostID := fmt.Sprintf("post_upd_own_%d", suffix)
	otherPostID := fmt.Sprintf("post_upd_other_%d", suffix)
	remoinID := fmt.Sprintf("post_upd_remoin_%d", suffix)
	authorToken := fmt.Sprintf("upd-author-token-%d", suffix)
	authorCSRF := fmt.Sprintf("upd-author-csrf-%d", suffix)
	// The trigger below makes PostgreSQL refuse exactly this content, so the request
	// travels the real handler → pgx → server path and fails inside the UPDATE itself.
	poisonContent := fmt.Sprintf("storage-failure-%d", suffix)
	triggerFn := fmt.Sprintf("moina_test_refuse_update_%d", suffix)
	triggerName := fmt.Sprintf("moina_test_refuse_update_trg_%d", suffix)

	t.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = repository.Pool().Exec(cleanupContext, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON posts`, triggerName))
		_, _ = repository.Pool().Exec(cleanupContext, fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, triggerFn))
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM outbox_events WHERE aggregate_id=ANY($1::text[])`, []string{authorID, otherID})
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM posts WHERE id=ANY($1::text[])`, []string{remoinID, ownPostID, otherPostID})
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM sessions WHERE user_id=ANY($1::text[])`, []string{authorID, otherID})
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM audit_events WHERE actor_id=ANY($1::text[])`, []string{authorID, otherID})
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM users WHERE id=ANY($1::text[])`, []string{authorID, otherID})
	})

	if _, err := repository.Pool().Exec(ctx, `INSERT INTO users(id,username,display_name,roles) VALUES
		($1,$2,$2,ARRAY['member']::text[]),
		($3,$4,$4,ARRAY['member']::text[])`,
		authorID, authorUsername, otherID, otherUsername); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateSession(ctx, model.Session{
		ID: fmt.Sprintf("session_upd_author_%d", suffix), UserID: authorID,
		TokenHash: secrets.HashToken(authorToken), CSRFHash: secrets.HashToken(authorCSRF),
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(ctx, `INSERT INTO posts(id,author_id,content,status,published_at) VALUES
		($1,$2,'내 Moin','published',now()),
		($3,$4,'남의 Moin','published',now())`,
		ownPostID, authorID, otherPostID, otherID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(ctx, `INSERT INTO posts(id,author_id,content,kind,remoin_post_id,status,published_at) VALUES($1,$2,'','remoin',$3,'published',now())`,
		remoinID, authorID, otherPostID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.content = TG_ARGV[0] THEN
				RAISE EXCEPTION 'moina test: storage refused this update';
			END IF;
			RETURN NEW;
		END $$`, triggerFn)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(ctx, fmt.Sprintf(`CREATE TRIGGER %s BEFORE UPDATE ON posts FOR EACH ROW EXECUTE FUNCTION %s(%s)`, triggerName, triggerFn, pgQuoteLiteral(poisonContent))); err != nil {
		t.Fatal(err)
	}

	server := New(repository, secrets, "v0.1.32-test")
	handler := server.Handler()
	type apiError struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	patch := func(postID, content string) (int, apiError) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"content": content})
		request := httptest.NewRequest(http.MethodPatch, "/api/v1/posts/"+postID, bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-CSRF-Token", authorCSRF)
		request.AddCookie(&http.Cookie{Name: SessionCookie, Value: authorToken})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		var apiErr apiError
		if response.Code != http.StatusOK {
			if err := json.Unmarshal(response.Body.Bytes(), &apiErr); err != nil {
				t.Fatalf("응답 %d 본문을 읽을 수 없습니다: %s", response.Code, response.Body.String())
			}
		}
		return response.Code, apiErr
	}
	contentOf := func(postID string) string {
		t.Helper()
		var content string
		if err := repository.Pool().QueryRow(ctx, `SELECT content FROM posts WHERE id=$1`, postID).Scan(&content); err != nil {
			t.Fatal(err)
		}
		return content
	}

	t.Run("someone else's Moin stays 409 not_editable", func(t *testing.T) {
		code, apiErr := patch(otherPostID, "남의 Moin을 고치려는 시도")
		if code != http.StatusConflict || apiErr.Code != "not_editable" || apiErr.Message != "본인의 공개 Moin만 수정할 수 있습니다" {
			t.Fatalf("응답 = %d %+v", code, apiErr)
		}
		if got := contentOf(otherPostID); got != "남의 Moin" {
			t.Fatalf("남의 Moin이 바뀌었습니다: %q", got)
		}
	})

	t.Run("own Remoin stays 409 not_editable", func(t *testing.T) {
		code, apiErr := patch(remoinID, "Remoin에 본문을 붙이려는 시도")
		if code != http.StatusConflict || apiErr.Code != "not_editable" {
			t.Fatalf("응답 = %d %+v", code, apiErr)
		}
	})

	t.Run("failed UPDATE is 500 storage_error", func(t *testing.T) {
		code, apiErr := patch(ownPostID, poisonContent)
		if code != http.StatusInternalServerError || apiErr.Code != "storage_error" {
			t.Fatalf("저장 오류가 감춰졌습니다: %d %+v", code, apiErr)
		}
		if got := contentOf(ownPostID); got != "내 Moin" {
			t.Fatalf("실패한 수정이 남았습니다: %q", got)
		}
	})

	t.Run("own Moin still updates", func(t *testing.T) {
		code, apiErr := patch(ownPostID, "고친 내 Moin")
		if code != http.StatusOK {
			t.Fatalf("응답 = %d %+v", code, apiErr)
		}
		if got := contentOf(ownPostID); got != "고친 내 Moin" {
			t.Fatalf("본문이 바뀌지 않았습니다: %q", got)
		}
	})
}

// pgQuoteLiteral quotes a string for use as a trigger argument, which PostgreSQL
// only accepts as a literal in CREATE TRIGGER (not as a bound parameter).
func pgQuoteLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
