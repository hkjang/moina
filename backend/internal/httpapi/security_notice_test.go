package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hkjang/moina/backend/internal/model"
)

func TestSecurityNoticeBodyNamesRequestIP(t *testing.T) {
	request := httptest.NewRequest("POST", "/api/v1/profile/password", nil)
	request.RemoteAddr = "203.0.113.5:44812"
	if got := securityNoticeBody(request, "비밀번호를 변경했습니다."); got != "비밀번호를 변경했습니다. (요청 IP 203.0.113.5)" {
		t.Fatalf("body with IP = %q", got)
	}
	request.RemoteAddr = ""
	if got := securityNoticeBody(request, "비밀번호를 변경했습니다."); got != "비밀번호를 변경했습니다." {
		t.Fatalf("body without IP = %q", got)
	}
}

// A security notice links to the screen where the owner would undo an action
// they did not take, and the mandatory type is never switched off or batched.
func TestSecurityNoticeDecoration(t *testing.T) {
	server := &Server{}
	for event, wantPath := range map[string]string{
		securityEventPasswordChanged: "/settings/security",
		securityEventPasswordReset:   "/settings/security",
		securityEventAPIKeyCreated:   "/settings/keys",
		securityEventAPIKeyRotated:   "/settings/keys",
		"":                           "/settings/security",
	} {
		payload, err := json.Marshal(map[string]string{"event": event, "body": "본문"})
		if err != nil {
			t.Fatal(err)
		}
		item := model.Notification{ID: "ntf_" + event, Type: "security", Payload: payload}
		server.decorateNotification(t.Context(), &item)
		if item.Title != "계정 보안" || item.Body != "본문" || item.TargetPath != wantPath || item.Type != "security" {
			t.Fatalf("%q decorated = title %q body %q path %q type %q", event, item.Title, item.Body, item.TargetPath, item.Type)
		}
	}
	preferences := defaultPreferencesDocument().Notifications
	preferences.Email.Enabled = true
	preferences.Digest.Mode = "daily"
	if !notificationInAppEnabled(preferences, "security") || notificationBatched(preferences, "security") || !notificationEmailEnabled(preferences, "security") {
		t.Fatal("security notice must stay in-app, unbatched and emailed at once")
	}
}

func TestSecurityNoticeEmailUsesNoticeBody(t *testing.T) {
	payload, err := json.Marshal(map[string]string{"event": securityEventAPIKeyCreated, "body": "새 API·MCP 키 'ci'을(를) 만들었습니다. (요청 IP 203.0.113.5)"})
	if err != nil {
		t.Fatal(err)
	}
	item := model.Notification{ID: "ntf_key", Type: "security", Payload: payload}
	(&Server{}).decorateNotification(t.Context(), &item)
	message := notificationEmailMessage("MOINA", "https://moina.example.com/", "owner@example.com", item)
	if message.Subject != "[MOINA] 계정 보안" {
		t.Fatalf("subject = %q", message.Subject)
	}
	if !strings.HasPrefix(message.Body, "새 API·MCP 키 'ci'을(를) 만들었습니다. (요청 IP 203.0.113.5)") || !strings.Contains(message.Body, "확인하기: https://moina.example.com/settings/keys") {
		t.Fatalf("body = %q", message.Body)
	}
}
