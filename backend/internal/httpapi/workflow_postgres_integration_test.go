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

	"github.com/hkjang/moina/backend/internal/event"
	"github.com/hkjang/moina/backend/internal/model"
	"github.com/hkjang/moina/backend/internal/secure"
	"github.com/hkjang/moina/backend/internal/store"
	"github.com/jackc/pgx/v5"
)

func TestPostgreSQLWorkflowPolicyGuards(t *testing.T) {
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
	secrets, err := secure.New(bytes.Repeat([]byte{31}, 32))
	if err != nil {
		t.Fatal(err)
	}

	suffix := time.Now().UnixNano()
	adminID := fmt.Sprintf("usr_workflow_admin_%d", suffix)
	requesterID := fmt.Sprintf("usr_workflow_requester_%d", suffix)
	eligibleID := fmt.Sprintf("usr_workflow_eligible_%d", suffix)
	staleID := fmt.Sprintf("usr_workflow_stale_%d", suffix)
	adminUsername := fmt.Sprintf("workflow_admin_%d", suffix)
	requesterUsername := fmt.Sprintf("workflow_requester_%d", suffix)
	reviewRole := fmt.Sprintf("workflow_review_%d", suffix)
	badRole := fmt.Sprintf("workflow_bad_%d", suffix)
	auxRole := fmt.Sprintf("workflow_aux_%d", suffix)
	postID := fmt.Sprintf("post_workflow_%d", suffix)
	approvalID := fmt.Sprintf("approval_workflow_%d", suffix)
	notificationApprovalID := fmt.Sprintf("approval_notify_%d", suffix)
	adminToken := fmt.Sprintf("workflow-admin-token-%d", suffix)
	adminCSRF := fmt.Sprintf("workflow-admin-csrf-%d", suffix)
	requesterToken := fmt.Sprintf("workflow-requester-token-%d", suffix)
	requesterCSRF := fmt.Sprintf("workflow-requester-csrf-%d", suffix)

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
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM outbox_events WHERE idempotency_key LIKE $1`, "notification:approval:"+notificationApprovalID+":%")
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM approval_requests WHERE id=$1`, approvalID)
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM posts WHERE id=$1`, postID)
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM sessions WHERE user_id=ANY($1::text[])`, []string{adminID, requesterID, eligibleID, staleID})
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM audit_events WHERE actor_id=ANY($1::text[])`, []string{adminID, requesterID, eligibleID, staleID})
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM users WHERE id=ANY($1::text[])`, []string{adminID, requesterID, eligibleID, staleID})
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM roles WHERE name=ANY($1::text[])`, []string{reviewRole, badRole, auxRole})
		if hadSetting {
			_, _ = repository.Pool().Exec(cleanupContext, `INSERT INTO settings(key,payload,sensitive,revision,updated_by,updated_at)
				VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(key) DO UPDATE
				SET payload=EXCLUDED.payload,sensitive=EXCLUDED.sensitive,revision=EXCLUDED.revision,updated_by=EXCLUDED.updated_by,updated_at=EXCLUDED.updated_at`,
				settingWorkflow, previous.payload, previous.sensitive, previous.revision, previous.updatedBy, previous.updatedAt)
		} else {
			_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM settings WHERE key=$1`, settingWorkflow)
		}
	})

	if _, err := repository.Pool().Exec(ctx, `INSERT INTO roles(name,description) VALUES
		($1,'workflow reviewer'),($2,'workflow invalid reviewer'),($3,'workflow auxiliary reviewer')`, reviewRole, badRole, auxRole); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(ctx, `INSERT INTO role_permissions(role_name,permission) VALUES
		($1,'approvals:*'),($1,'posts:read'),($1,'posts:write'),($2,'posts:read'),($3,'approvals:review')`, reviewRole, badRole, auxRole); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(ctx, `INSERT INTO users(id,username,display_name,roles) VALUES
		($1,$2,$2,ARRAY['super_admin']::text[]),
		($3,$4,$4,ARRAY[$5]::text[]),
		($6,$6,$6,ARRAY[$5,$7]::text[]),
		($8,$8,$8,ARRAY[$5]::text[])`,
		adminID, adminUsername, requesterID, requesterUsername, reviewRole, eligibleID, auxRole, staleID); err != nil {
		t.Fatal(err)
	}
	for _, session := range []model.Session{
		{ID: fmt.Sprintf("session_workflow_admin_%d", suffix), UserID: adminID, TokenHash: secrets.HashToken(adminToken), CSRFHash: secrets.HashToken(adminCSRF), ExpiresAt: time.Now().UTC().Add(time.Hour)},
		{ID: fmt.Sprintf("session_workflow_requester_%d", suffix), UserID: requesterID, TokenHash: secrets.HashToken(requesterToken), CSRFHash: secrets.HashToken(requesterCSRF), ExpiresAt: time.Now().UTC().Add(time.Hour)},
	} {
		if err := repository.CreateSession(ctx, session); err != nil {
			t.Fatal(err)
		}
	}

	server := New(repository, secrets, "v0.1.5-test")
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
	workflowBody := func(roles ...string) string {
		raw, marshalErr := json.Marshal(model.WorkflowConfig{Enabled: true, Actions: []string{"post.*"}, ApproverRoles: roles})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		return string(raw)
	}

	disabledInvalid, err := json.Marshal(model.WorkflowConfig{Enabled: false, Actions: []string{"post.publish"}, ApproverRoles: []string{badRole}})
	if err != nil {
		t.Fatal(err)
	}
	response := requestJSON(http.MethodPut, "/api/v1/admin/workflow", adminToken, adminCSRF, string(disabledInvalid))
	if response.Code != http.StatusBadRequest || workflowResponseCode(response.Body.Bytes()) != "no_eligible_approver" {
		t.Fatalf("비활성 정책의 권한 없는 reviewer role 저장 = %d: %s", response.Code, response.Body.String())
	}

	response = requestJSON(http.MethodPut, "/api/v1/admin/workflow", adminToken, adminCSRF, workflowBody(reviewRole, badRole))
	if response.Code != http.StatusBadRequest || workflowResponseCode(response.Body.Bytes()) != "no_eligible_approver" {
		t.Fatalf("권한 없는 reviewer role 포함 저장 = %d: %s", response.Code, response.Body.String())
	}
	response = requestJSON(http.MethodPut, "/api/v1/admin/workflow", adminToken, adminCSRF, workflowBody(reviewRole))
	if response.Code != http.StatusOK {
		t.Fatalf("모든 reviewer role 유효 저장 = %d: %s", response.Code, response.Body.String())
	}

	snapshot, err := secrets.Encrypt([]byte(`{"contentPreview":"self approval"}`), "approval:"+approvalID+":snapshot")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(ctx, `INSERT INTO posts(id,author_id,content,status,approval_required) VALUES($1,$2,'self approval','pending_approval',true)`, postID, requesterID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(ctx, `INSERT INTO approval_requests(id,action,target_type,target_id,requester_id,status,snapshot) VALUES($1,'post.publish','post',$2,$3,'pending',$4)`, approvalID, postID, requesterID, snapshot); err != nil {
		t.Fatal(err)
	}
	for _, decision := range []struct {
		path string
		body string
	}{
		{"approve", `{}`},
		{"reject", `{"comment":"본인 요청 반려 시도"}`},
	} {
		response = requestJSON(http.MethodPost, "/api/v1/approvals/"+approvalID+"/"+decision.path, requesterToken, requesterCSRF, decision.body)
		if response.Code != http.StatusForbidden || workflowResponseCode(response.Body.Bytes()) != "self_approval" {
			t.Fatalf("자기 승인 REST %s = %d: %s", decision.path, response.Code, response.Body.String())
		}
	}
	var approvalStatus, postStatus string
	if err := repository.Pool().QueryRow(ctx, `SELECT status FROM approval_requests WHERE id=$1`, approvalID).Scan(&approvalStatus); err != nil {
		t.Fatal(err)
	}
	if err := repository.Pool().QueryRow(ctx, `SELECT status FROM posts WHERE id=$1`, postID).Scan(&postStatus); err != nil {
		t.Fatal(err)
	}
	if approvalStatus != "pending" || postStatus != "pending_approval" {
		t.Fatalf("자기 승인 후 상태 변경: approval=%s post=%s", approvalStatus, postStatus)
	}

	// An approval notification already queued before a permission revoke must
	// be discarded when the outbox handler checks the current final policy.
	staleEventPayload, err := json.Marshal(notificationEventPayload{
		UserID: staleID, ActorID: adminID, Type: "approval_requested", TargetID: approvalID,
		Payload: json.RawMessage(fmt.Sprintf(`{"postId":%q,"approvalId":%q}`, postID, approvalID)),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a role policy change after workflow configuration. Membership in
	// the configured role alone must not produce an approval notification; the
	// user's current union of role permissions must still authorize review.
	if _, err := repository.Pool().Exec(ctx, `DELETE FROM role_permissions WHERE role_name=$1 AND permission='approvals:*'`, reviewRole); err != nil {
		t.Fatal(err)
	}
	staleNotificationID := fmt.Sprintf("ntf_workflow_stale_%d", suffix)
	if err := server.handleOutboxEvent(ctx, event.Event{ID: staleNotificationID, Type: notificationCreateEvent, AggregateID: staleID, Payload: staleEventPayload, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	var staleNotifications int
	if err := repository.Pool().QueryRow(ctx, `SELECT count(*) FROM notifications WHERE id=$1`, staleNotificationID).Scan(&staleNotifications); err != nil {
		t.Fatal(err)
	}
	if staleNotifications != 0 {
		t.Fatal("권한이 회수된 사용자에게 지연 승인 알림이 저장됐습니다")
	}
	tx, err := repository.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.enqueueApproverNotifications(ctx, tx, adminID, notificationApprovalID, postID, []string{reviewRole}); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := repository.Pool().Query(ctx, `SELECT payload->>'userId' FROM outbox_events WHERE idempotency_key LIKE $1 ORDER BY payload->>'userId'`, "notification:approval:"+notificationApprovalID+":%")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	notified := make([]string, 0)
	for rows.Next() {
		var userID string
		if err := rows.Scan(&userID); err != nil {
			t.Fatal(err)
		}
		notified = append(notified, userID)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(notified) != 1 || notified[0] != eligibleID {
		t.Fatalf("최종 승인 권한과 다른 알림 대상: %v, want [%s]", notified, eligibleID)
	}

	if _, err := repository.Pool().Exec(ctx, `UPDATE users SET active=false WHERE id=$1`, eligibleID); err != nil {
		t.Fatal(err)
	}
	response = requestJSON(http.MethodPost, "/api/v1/posts", requesterToken, requesterCSRF, `{"content":"독립 승인자가 없는 제출","visibility":"public"}`)
	if response.Code != http.StatusConflict || workflowResponseCode(response.Body.Bytes()) != "no_independent_approver" {
		t.Fatalf("독립 승인자 없는 게시 = %d: %s", response.Code, response.Body.String())
	}
}

func workflowResponseCode(raw []byte) string {
	var payload struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(raw, &payload)
	return payload.Code
}
