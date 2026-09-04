package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/hkjang/moina/backend/internal/model"
	"github.com/hkjang/moina/backend/internal/secure"
	"github.com/hkjang/moina/backend/internal/store"
)

// A shared address is one token. The Moin must join only the Topic its author
// actually wrote and notify only the member they actually mentioned, never the
// fragment or the handle carried inside a link to another site.
func TestPostgreSQLLinkTokensDoNotBecomeTopicsOrMentions(t *testing.T) {
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
	secrets, err := secure.New(bytes.Repeat([]byte{51}, 32))
	if err != nil {
		t.Fatal(err)
	}

	suffix := time.Now().UnixNano()
	authorID := fmt.Sprintf("usr_link_author_%d", suffix)
	mentionedID := fmt.Sprintf("usr_link_mentioned_%d", suffix)
	linkedID := fmt.Sprintf("usr_link_handle_%d", suffix)
	authorUsername := fmt.Sprintf("link_author_%d", suffix)
	mentionedUsername := fmt.Sprintf("link_mentioned_%d", suffix)
	linkedUsername := fmt.Sprintf("link_handle_%d", suffix)
	authorToken := fmt.Sprintf("link-author-token-%d", suffix)
	authorCSRF := fmt.Sprintf("link-author-csrf-%d", suffix)
	writtenTag := fmt.Sprintf("link_tag_%d", suffix)
	fragmentTag := fmt.Sprintf("link_fragment_%d", suffix)

	t.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		userIDs := []string{authorID, mentionedID, linkedID}
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM outbox_events WHERE aggregate_id=ANY($1::text[])`, userIDs)
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM posts WHERE author_id=ANY($1::text[])`, userIDs)
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM topics WHERE slug=ANY($1::text[])`, []string{writtenTag, fragmentTag})
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM sessions WHERE user_id=ANY($1::text[])`, userIDs)
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM audit_events WHERE actor_id=ANY($1::text[])`, userIDs)
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM users WHERE id=ANY($1::text[])`, userIDs)
	})

	if _, err := repository.Pool().Exec(ctx, `INSERT INTO users(id,username,display_name,roles) VALUES
		($1,$2,$2,ARRAY['member']::text[]),
		($3,$4,$4,ARRAY['member']::text[]),
		($5,$6,$6,ARRAY['member']::text[])`,
		authorID, authorUsername, mentionedID, mentionedUsername, linkedID, linkedUsername); err != nil {
		t.Fatal(err)
	}
	session := model.Session{
		ID: fmt.Sprintf("session_link_author_%d", suffix), UserID: authorID,
		TokenHash: secrets.HashToken(authorToken), CSRFHash: secrets.HashToken(authorCSRF),
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := repository.CreateSession(ctx, session); err != nil {
		t.Fatal(err)
	}

	server := New(repository, secrets, "v0.1.20-test")
	handler := server.Handler()
	content := fmt.Sprintf("설치 안내 https://example.com/guide#%s 와 https://example.com/@%s 를 보세요 #%s @%s",
		fragmentTag, linkedUsername, writtenTag, mentionedUsername)
	body, err := json.Marshal(map[string]string{"content": content, "visibility": "public"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/posts", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", authorCSRF)
	request.AddCookie(&http.Cookie{Name: SessionCookie, Value: authorToken})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("Moin 작성 = %d: %s", response.Code, response.Body.String())
	}
	var created struct {
		Data model.Moin `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	rows, err := repository.Pool().Query(ctx, `SELECT t.slug FROM post_topics pt JOIN topics t ON t.id=pt.topic_id WHERE pt.post_id=$1 ORDER BY t.slug`, created.Data.ID)
	if err != nil {
		t.Fatal(err)
	}
	slugs := make([]string, 0, 2)
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		slugs = append(slugs, slug)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	if !slices.Equal(slugs, []string{writtenTag}) {
		t.Fatalf("링크 안의 fragment가 Topic이 되었습니다: %v", slugs)
	}

	notified := func(userID string) int {
		t.Helper()
		var count int
		if err := repository.Pool().QueryRow(ctx, `SELECT count(*)::integer FROM outbox_events WHERE aggregate_id=$1`, userID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	if got := notified(mentionedID); got != 1 {
		t.Fatalf("멘션한 회원에게 알림이 가지 않았습니다: %d", got)
	}
	if got := notified(linkedID); got != 0 {
		t.Fatalf("주소 안의 handle이 멘션 알림을 만들었습니다: %d", got)
	}
}
