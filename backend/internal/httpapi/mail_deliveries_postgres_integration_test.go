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
)

func TestPostgreSQLMailDeliveryLogRecordsEveryAttemptWithoutBody(t *testing.T) {
	dsn := os.Getenv("MOINA_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("MOINA_TEST_POSTGRES_DSN is not set")
	}
	repository, err := store.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(repository.Close)
	secrets, err := secure.New(bytes.Repeat([]byte{43}, 32))
	if err != nil {
		t.Fatal(err)
	}
	suffix := time.Now().UnixNano()
	userID := fmt.Sprintf("mail-log-user-%d", suffix)
	deliveryID := fmt.Sprintf("mail-log-%d", suffix)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = repository.Pool().Exec(ctx, `DELETE FROM mail_deliveries WHERE user_id=$1`, userID)
	})
	server := New(repository, secrets, "v0.1.30-test")
	delivery := mailDelivery{ID: deliveryID, Event: mailEventTest, UserID: userID, ActorID: userID, Recipient: "person@example.com", Subject: "MOINA SMTP 연결 테스트"}

	// First attempt fails: the row says so, with the relay's reason.
	server.recordMailAttempt(t.Context(), delivery)
	server.completeMailAttempt(t.Context(), delivery, errors.New("SMTP 서버 연결 실패: connection refused"))
	var status, message string
	var attempts int
	if err := repository.Pool().QueryRow(t.Context(), `SELECT status,attempts,error_message FROM mail_deliveries WHERE id=$1`, deliveryID).Scan(&status, &attempts, &message); err != nil {
		t.Fatal(err)
	}
	if status != mailDeliveryFailed || attempts != 1 || message == "" {
		t.Fatalf("첫 시도 기록 = %s/%d/%q", status, attempts, message)
	}

	// The retry updates the same row instead of adding another.
	server.recordMailAttempt(t.Context(), delivery)
	server.completeMailAttempt(t.Context(), delivery, nil)
	var count int
	if err := repository.Pool().QueryRow(t.Context(), `SELECT count(*) FROM mail_deliveries WHERE user_id=$1`, userID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err := repository.Pool().QueryRow(t.Context(), `SELECT status,attempts,error_message FROM mail_deliveries WHERE id=$1`, deliveryID).Scan(&status, &attempts, &message); err != nil {
		t.Fatal(err)
	}
	if count != 1 || status != mailDeliverySent || attempts != 2 || message != "" {
		t.Fatalf("재시도 기록 = rows %d, %s/%d/%q", count, status, attempts, message)
	}

	admin := principal{User: model.User{ID: userID, Roles: []string{model.RoleSuperAdmin}}, Permissions: []string{"*"}}
	list := func(query string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/smtp/deliveries"+query, nil)
		request = request.WithContext(withPrincipal(request, admin))
		response := httptest.NewRecorder()
		server.adminListMailDeliveries(response, request)
		return response
	}
	response := list("?status=sent&limit=100")
	if response.Code != http.StatusOK {
		t.Fatalf("발송 기록 조회 = %d: %s", response.Code, response.Body.String())
	}
	var page struct {
		Data struct {
			Items   []mailDelivery `json:"items"`
			Summary struct {
				Total  int64            `json:"total"`
				Status map[string]int64 `json:"status"`
			} `json:"summary"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	var found *mailDelivery
	for index := range page.Data.Items {
		if page.Data.Items[index].ID == deliveryID {
			found = &page.Data.Items[index]
		}
	}
	if found == nil || found.Recipient != "person@example.com" || found.Subject != delivery.Subject || found.Attempts != 2 || found.Event != mailEventTest {
		t.Fatalf("발송 기록 항목 = %+v", found)
	}
	if page.Data.Summary.Total < 1 || page.Data.Summary.Status[mailDeliverySent] < 1 {
		t.Fatalf("발송 기록 요약 = %+v", page.Data.Summary)
	}
	if bytes.Contains(response.Body.Bytes(), []byte("body")) || bytes.Contains(response.Body.Bytes(), []byte("정상적으로 연결")) {
		t.Fatal("발송 기록 응답에 본문이 들어 있습니다")
	}
	if response := list("?status=bounced"); response.Code != http.StatusBadRequest {
		t.Fatalf("알 수 없는 상태 필터 = %d", response.Code)
	}
}

func TestPostgreSQLMentionFromReplyIsBundledIntoOneMail(t *testing.T) {
	dsn := os.Getenv("MOINA_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("MOINA_TEST_POSTGRES_DSN is not set")
	}
	repository, err := store.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(repository.Close)
	secrets, err := secure.New(bytes.Repeat([]byte{44}, 32))
	if err != nil {
		t.Fatal(err)
	}
	suffix := time.Now().UnixNano()
	userID := fmt.Sprintf("mail-bundle-user-%d", suffix)
	if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO users(id,username,display_name,roles) VALUES($1,$2,'묶음',ARRAY['member'])`, userID, fmt.Sprintf("mailbundle%d", suffix)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = repository.Pool().Exec(ctx, `DELETE FROM notifications WHERE user_id=$1`, userID)
		_, _ = repository.Pool().Exec(ctx, `DELETE FROM users WHERE id=$1`, userID)
	})
	postID := fmt.Sprintf("post-bundle-%d", suffix)
	otherPostID := fmt.Sprintf("post-alone-%d", suffix)
	for id, kind := range map[string]string{"ntf-reply-" + postID: "reply", "ntf-mention-" + postID: "mention"} {
		if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO notifications(id,user_id,type,target_id,payload) VALUES($1,$2,$3,$4,'{}')`, id, userID, kind, postID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO notifications(id,user_id,type,target_id,payload) VALUES($1,$2,'mention',$3,'{}')`, "ntf-mention-"+otherPostID, userID, otherPostID); err != nil {
		t.Fatal(err)
	}
	server := New(repository, secrets, "v0.1.30-test")
	bundled, err := server.mentionBundledWithReply(t.Context(), model.Notification{ID: "ntf-mention-" + postID, UserID: userID, TargetID: postID})
	if err != nil || !bundled {
		t.Fatalf("같은 Echo에서 나온 멘션이 묶이지 않았습니다: bundled=%v err=%v", bundled, err)
	}
	alone, err := server.mentionBundledWithReply(t.Context(), model.Notification{ID: "ntf-mention-" + otherPostID, UserID: userID, TargetID: otherPostID})
	if err != nil || alone {
		t.Fatalf("답글 없는 멘션이 묶였습니다: bundled=%v err=%v", alone, err)
	}
}
