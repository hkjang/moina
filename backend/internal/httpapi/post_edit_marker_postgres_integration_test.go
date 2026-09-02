package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/hkjang/moina/backend/internal/model"
	"github.com/hkjang/moina/backend/internal/secure"
	"github.com/hkjang/moina/backend/internal/store"
	"github.com/jackc/pgx/v5"
)

// posts.updated_at is the only signal a client has for telling a rewritten Moin
// from the original, so a review decision must leave it alone and an author edit
// must move it.
func TestPostgreSQLPostEditTimestamp(t *testing.T) {
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

	suffix := time.Now().UnixNano()
	adminID := fmt.Sprintf("usr_edit_admin_%d", suffix)
	authorID := fmt.Sprintf("usr_edit_author_%d", suffix)
	reviewerID := fmt.Sprintf("usr_edit_reviewer_%d", suffix)
	adminUsername := fmt.Sprintf("edit_admin_%d", suffix)
	authorUsername := fmt.Sprintf("edit_author_%d", suffix)
	reviewerUsername := fmt.Sprintf("edit_reviewer_%d", suffix)
	reviewRole := fmt.Sprintf("edit_review_%d", suffix)
	postID := fmt.Sprintf("post_edit_%d", suffix)
	approvalID := fmt.Sprintf("approval_edit_%d", suffix)
	adminToken := fmt.Sprintf("edit-admin-token-%d", suffix)
	adminCSRF := fmt.Sprintf("edit-admin-csrf-%d", suffix)
	authorToken := fmt.Sprintf("edit-author-token-%d", suffix)
	authorCSRF := fmt.Sprintf("edit-author-csrf-%d", suffix)
	reviewerToken := fmt.Sprintf("edit-reviewer-token-%d", suffix)
	reviewerCSRF := fmt.Sprintf("edit-reviewer-csrf-%d", suffix)

	type savedSetting struct {
		payload   []byte
		sensitive bool
		revision  int64
		updatedBy string
		updatedAt time.Time
	}
	var previous savedSetting
	settingErr := repository.Pool().QueryRow(ctx, `SELECT payload,sensitive,revision,updated_by,updated_at FROM settings WHERE key=$1`, settingWorkflow).
		Scan(&previous.payload, &previous.sensitive, &previous.revision, &previous.updatedBy, &previous.updatedAt)
	if settingErr != nil && !errors.Is(settingErr, pgx.ErrNoRows) {
		t.Fatal(settingErr)
	}
	hadSetting := settingErr == nil

	t.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM outbox_events WHERE aggregate_id=ANY($1::text[])`, []string{authorID, reviewerID, adminID})
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM approval_requests WHERE id=$1`, approvalID)
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM posts WHERE id=$1`, postID)
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM sessions WHERE user_id=ANY($1::text[])`, []string{adminID, authorID, reviewerID})
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM audit_events WHERE actor_id=ANY($1::text[])`, []string{adminID, authorID, reviewerID})
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM users WHERE id=ANY($1::text[])`, []string{adminID, authorID, reviewerID})
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM roles WHERE name=$1`, reviewRole)
		if hadSetting {
			_, _ = repository.Pool().Exec(cleanupContext, `INSERT INTO settings(key,payload,sensitive,revision,updated_by,updated_at)
				VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(key) DO UPDATE
				SET payload=EXCLUDED.payload,sensitive=EXCLUDED.sensitive,revision=EXCLUDED.revision,updated_by=EXCLUDED.updated_by,updated_at=EXCLUDED.updated_at`,
				settingWorkflow, previous.payload, previous.sensitive, previous.revision, previous.updatedBy, previous.updatedAt)
		} else {
			_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM settings WHERE key=$1`, settingWorkflow)
		}
	})

	if _, err := repository.Pool().Exec(ctx, `INSERT INTO roles(name,description) VALUES($1,'edit marker reviewer')`, reviewRole); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(ctx, `INSERT INTO role_permissions(role_name,permission) VALUES
		($1,'approvals:*'),($1,'posts:read'),($1,'posts:write')`, reviewRole); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(ctx, `INSERT INTO users(id,username,display_name,roles) VALUES
		($1,$2,$2,ARRAY['super_admin']::text[]),
		($3,$4,$4,ARRAY['member']::text[]),
		($5,$6,$6,ARRAY[$7]::text[])`,
		adminID, adminUsername, authorID, authorUsername, reviewerID, reviewerUsername, reviewRole); err != nil {
		t.Fatal(err)
	}
	for _, session := range []model.Session{
		{ID: fmt.Sprintf("session_edit_admin_%d", suffix), UserID: adminID, TokenHash: secrets.HashToken(adminToken), CSRFHash: secrets.HashToken(adminCSRF), ExpiresAt: time.Now().UTC().Add(time.Hour)},
		{ID: fmt.Sprintf("session_edit_author_%d", suffix), UserID: authorID, TokenHash: secrets.HashToken(authorToken), CSRFHash: secrets.HashToken(authorCSRF), ExpiresAt: time.Now().UTC().Add(time.Hour)},
		{ID: fmt.Sprintf("session_edit_reviewer_%d", suffix), UserID: reviewerID, TokenHash: secrets.HashToken(reviewerToken), CSRFHash: secrets.HashToken(reviewerCSRF), ExpiresAt: time.Now().UTC().Add(time.Hour)},
	} {
		if err := repository.CreateSession(ctx, session); err != nil {
			t.Fatal(err)
		}
	}

	server := New(repository, secrets, "v0.1.17-test")
	handler := server.Handler()
	requestJSON := func(method, path, token, csrf, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-CSRF-Token", csrf)
		request.AddCookie(&http.Cookie{Name: SessionCookie, Value: token})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	timestamps := func() (time.Time, time.Time) {
		t.Helper()
		var createdAt, updatedAt time.Time
		if err := repository.Pool().QueryRow(ctx, `SELECT created_at,updated_at FROM posts WHERE id=$1`, postID).Scan(&createdAt, &updatedAt); err != nil {
			t.Fatal(err)
		}
		return createdAt, updatedAt
	}

	policy, err := json.Marshal(model.WorkflowConfig{Enabled: true, Actions: []string{"post.*"}, ApproverRoles: []string{reviewRole}})
	if err != nil {
		t.Fatal(err)
	}
	response := requestJSON(http.MethodPut, "/api/v1/admin/workflow", adminToken, adminCSRF, string(policy))
	if response.Code != http.StatusOK {
		t.Fatalf("승인 정책 저장 = %d: %s", response.Code, response.Body.String())
	}

	snapshot, err := secrets.Encrypt([]byte(`{"contentPreview":"검토 대기 본문"}`), "approval:"+approvalID+":snapshot")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(ctx, `INSERT INTO posts(id,author_id,content,status,approval_required) VALUES($1,$2,'검토 대기 본문','pending_approval',true)`, postID, authorID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(ctx, `INSERT INTO approval_requests(id,action,target_type,target_id,requester_id,status,snapshot) VALUES($1,'post.publish','post',$2,$3,'pending',$4)`, approvalID, postID, authorID, snapshot); err != nil {
		t.Fatal(err)
	}

	response = requestJSON(http.MethodPost, "/api/v1/approvals/"+approvalID+"/approve", reviewerToken, reviewerCSRF, `{}`)
	if response.Code != http.StatusOK {
		t.Fatalf("승인 = %d: %s", response.Code, response.Body.String())
	}
	createdAt, updatedAt := timestamps()
	if !updatedAt.Equal(createdAt) {
		t.Fatalf("승인 게시가 수정 시각을 옮겼습니다: created=%s updated=%s", createdAt, updatedAt)
	}

	response = requestJSON(http.MethodPatch, "/api/v1/posts/"+postID, authorToken, authorCSRF, `{"content":"검토 뒤 고친 본문"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("본문 수정 = %d: %s", response.Code, response.Body.String())
	}
	createdAt, updatedAt = timestamps()
	if !updatedAt.After(createdAt) {
		t.Fatalf("본문 수정이 수정 시각을 남기지 않았습니다: created=%s updated=%s", createdAt, updatedAt)
	}
	var body struct {
		Data model.Moin `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Data.UpdatedAt.After(body.Data.CreatedAt) {
		t.Fatalf("수정 응답이 편집 시각을 알리지 않습니다: %s", response.Body.String())
	}
}
