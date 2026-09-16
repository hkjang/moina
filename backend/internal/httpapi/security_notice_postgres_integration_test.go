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

	"github.com/hkjang/moina/backend/internal/event"
	"github.com/hkjang/moina/backend/internal/model"
	"github.com/hkjang/moina/backend/internal/secure"
	"github.com/hkjang/moina/backend/internal/store"
	"golang.org/x/crypto/bcrypt"
)

// An account-security change must reach the owner even though they appear to
// be the actor: a password change, an admin reset and a new API key each leave
// a "security" notification that stays in the notification centre and, with
// the email channel on, an immediate email event.
func TestPostgreSQLSecurityNoticesReachTheOwner(t *testing.T) {
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
	ownerID := fmt.Sprintf("usr_secnotice_owner_%d", suffix)
	adminID := fmt.Sprintf("usr_secnotice_admin_%d", suffix)
	ownerUsername := fmt.Sprintf("secnotice_owner_%d", suffix)
	adminUsername := fmt.Sprintf("secnotice_admin_%d", suffix)
	ownerToken := fmt.Sprintf("secnotice-owner-token-%d", suffix)
	ownerCSRF := fmt.Sprintf("secnotice-owner-csrf-%d", suffix)
	adminToken := fmt.Sprintf("secnotice-admin-token-%d", suffix)
	adminCSRF := fmt.Sprintf("secnotice-admin-csrf-%d", suffix)
	const currentPassword = "secnotice-current-password"

	t.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM outbox_events WHERE aggregate_id=ANY($1::text[])`, []string{ownerID, adminID})
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM notifications WHERE user_id=ANY($1::text[])`, []string{ownerID, adminID})
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM api_keys WHERE user_id=$1`, ownerID)
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM sessions WHERE user_id=ANY($1::text[])`, []string{ownerID, adminID})
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM audit_events WHERE actor_id=ANY($1::text[])`, []string{ownerID, adminID})
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM users WHERE id=ANY($1::text[])`, []string{ownerID, adminID})
	})

	hash, err := bcrypt.GenerateFromPassword([]byte(currentPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(ctx, `INSERT INTO users(id,username,display_name,email,password_hash,provider,roles) VALUES
		($1,$2,$2,'owner@example.com',$3,'local',ARRAY['member']::text[]),
		($4,$5,$5,'',$3,'local',ARRAY['super_admin']::text[])`,
		ownerID, ownerUsername, string(hash), adminID, adminUsername); err != nil {
		t.Fatal(err)
	}
	preferences := defaultPreferencesDocument()
	preferences.Notifications.Email.Enabled = true
	preferencePayload, err := json.Marshal(preferences)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.PutPreference(ctx, ownerID, preferencePayload); err != nil {
		t.Fatal(err)
	}
	createSession := func(id, userID, token, csrf string) {
		t.Helper()
		if err := repository.CreateSession(ctx, model.Session{ID: id, UserID: userID, TokenHash: secrets.HashToken(token), CSRFHash: secrets.HashToken(csrf), ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	createSession(fmt.Sprintf("session_secnotice_owner_%d", suffix), ownerID, ownerToken, ownerCSRF)
	createSession(fmt.Sprintf("session_secnotice_admin_%d", suffix), adminID, adminToken, adminCSRF)

	server := New(repository, secrets, "v0.1.30-test")
	handler := server.Handler()
	requestJSON := func(method, path, token, csrf, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		request.RemoteAddr = "203.0.113.5:44812"
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-CSRF-Token", csrf)
		request.AddCookie(&http.Cookie{Name: SessionCookie, Value: token})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	// deliver drains the owner's pending notification.create events the way
	// the outbox worker would and returns the stored notifications, newest last.
	deliver := func() []model.Notification {
		t.Helper()
		rows, err := repository.Pool().Query(ctx, `SELECT id,event_type,aggregate_id,payload,created_at FROM outbox_events
			WHERE aggregate_id=$1 AND event_type=$2 AND delivered_at IS NULL ORDER BY created_at,id`, ownerID, notificationCreateEvent)
		if err != nil {
			t.Fatal(err)
		}
		var pending []event.Event
		for rows.Next() {
			var item event.Event
			if err := rows.Scan(&item.ID, &item.Type, &item.AggregateID, &item.Payload, &item.CreatedAt); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			pending = append(pending, item)
		}
		rows.Close()
		for _, item := range pending {
			if err := server.handleOutboxEvent(ctx, item); err != nil {
				t.Fatal(err)
			}
			if _, err := repository.Pool().Exec(ctx, `UPDATE outbox_events SET delivered_at=now() WHERE id=$1`, item.ID); err != nil {
				t.Fatal(err)
			}
		}
		result, err := repository.Pool().Query(ctx, `SELECT id,user_id,COALESCE(actor_id,''),type,target_id,payload,in_app,created_at FROM notifications WHERE user_id=$1 ORDER BY created_at,id`, ownerID)
		if err != nil {
			t.Fatal(err)
		}
		defer result.Close()
		var items []model.Notification
		for result.Next() {
			var item model.Notification
			if err := result.Scan(&item.ID, &item.UserID, &item.ActorID, &item.Type, &item.TargetID, &item.Payload, &item.InApp, &item.CreatedAt); err != nil {
				t.Fatal(err)
			}
			server.decorateNotification(ctx, &item)
			items = append(items, item)
		}
		return items
	}
	emailEvents := func(notificationID string) int {
		t.Helper()
		var count int
		if err := repository.Pool().QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE event_type=$1 AND idempotency_key=$2`, notificationEmailEvent, "notification:email:"+notificationID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}

	response := requestJSON(http.MethodPost, "/api/v1/profile/keys", ownerToken, ownerCSRF, `{"name":"ci runner"}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("키 생성 = %d: %s", response.Code, response.Body.String())
	}
	items := deliver()
	if len(items) != 1 {
		t.Fatalf("키 생성 뒤 알림 %d개", len(items))
	}
	key := items[0]
	if key.Type != "security" || !key.InApp || key.ActorID != "" || key.Title != "계정 보안" || key.TargetPath != "/settings/keys" {
		t.Fatalf("키 생성 알림 = %+v", key)
	}
	if !strings.Contains(key.Body, "'ci runner'") || !strings.Contains(key.Body, "(요청 IP 203.0.113.5)") {
		t.Fatalf("키 생성 알림 본문 = %q", key.Body)
	}
	if emailEvents(key.ID) != 1 {
		t.Fatal("이메일 채널이 켜진 계정 보안 알림이 즉시 메일 이벤트를 남기지 않았습니다")
	}

	response = requestJSON(http.MethodPost, "/api/v1/profile/password", ownerToken, ownerCSRF,
		fmt.Sprintf(`{"currentPassword":%q,"newPassword":"secnotice-replacement-pw"}`, currentPassword))
	if response.Code != http.StatusNoContent {
		t.Fatalf("비밀번호 변경 = %d: %s", response.Code, response.Body.String())
	}
	items = deliver()
	if len(items) != 2 {
		t.Fatalf("비밀번호 변경 뒤 알림 %d개", len(items))
	}
	changed := items[1]
	if changed.Type != "security" || changed.ActorID != "" || changed.TargetPath != "/settings/security" || !strings.Contains(changed.Body, "모든 로그인 세션을 종료") || !strings.Contains(changed.Body, "203.0.113.5") {
		t.Fatalf("비밀번호 변경 알림 = %+v", changed)
	}

	response = requestJSON(http.MethodPost, "/api/v1/admin/users/"+ownerID+"/password", adminToken, adminCSRF, `{"password":"secnotice-admin-reset-pw"}`)
	if response.Code != http.StatusNoContent {
		t.Fatalf("관리자 재설정 = %d: %s", response.Code, response.Body.String())
	}
	items = deliver()
	if len(items) != 3 {
		t.Fatalf("관리자 재설정 뒤 알림 %d개", len(items))
	}
	reset := items[2]
	if reset.UserID != ownerID || reset.ActorID != "" || reset.TargetPath != "/settings/security" || !strings.HasPrefix(reset.Body, "관리자가 비밀번호를 재설정") || strings.Contains(reset.Body, "요청 IP") {
		t.Fatalf("관리자 재설정 알림 = %+v", reset)
	}
	var adminNotices int
	if err := repository.Pool().QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE aggregate_id=$1`, adminID).Scan(&adminNotices); err != nil {
		t.Fatal(err)
	}
	if adminNotices != 0 {
		t.Fatalf("재설정한 관리자에게 알림 %d개가 갔습니다", adminNotices)
	}

	// The owner reads the notices on their next login; the password change
	// already ended the earlier session.
	createSession(fmt.Sprintf("session_secnotice_owner_next_%d", suffix), ownerID, ownerToken+"-next", ownerCSRF+"-next")
	response = requestJSON(http.MethodGet, "/api/v1/notifications?limit=10", ownerToken+"-next", ownerCSRF+"-next", "")
	if response.Code != http.StatusOK {
		t.Fatalf("알림 목록 = %d: %s", response.Code, response.Body.String())
	}
	var listed struct {
		Data struct {
			Items []model.Notification `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Data.Items) != 3 {
		t.Fatalf("알림 센터 항목 %d개: %s", len(listed.Data.Items), response.Body.String())
	}
	for _, item := range listed.Data.Items {
		if item.Type != "security" || item.Title != "계정 보안" || item.Body == "" || item.Actor != nil {
			t.Fatalf("알림 센터 항목 = %+v", item)
		}
	}
}
