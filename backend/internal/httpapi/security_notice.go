package httpapi

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/hkjang/moina/backend/internal/secure"
)

// Security notice events. The value travels in the notification payload as
// "event" so the notification centre can point at the screen where the user
// would undo an action they did not take.
const (
	securityEventPasswordChanged = "password_changed"
	securityEventPasswordReset   = "password_reset"
	securityEventAPIKeyCreated   = "api_key_created"
	securityEventAPIKeyRotated   = "api_key_rotated"
)

// securityNoticeTargetPath is where a security notice links: key events go to
// the personal key screen, everything else to the login security screen.
func securityNoticeTargetPath(event string) string {
	switch event {
	case securityEventAPIKeyCreated, securityEventAPIKeyRotated:
		return "/settings/keys"
	default:
		return "/settings/security"
	}
}

// securityNoticeBody names the request IP so the owner can tell their own
// action from one made with a stolen session. The IP is the resolved client
// address (trusted proxy aware), never a raw header.
func securityNoticeBody(r *http.Request, body string) string {
	if ip := strings.TrimSpace(clientIP(r)); ip != "" {
		return body + " (요청 IP " + ip + ")"
	}
	return body
}

// notifySecurity records an account-security notice for userID. "security" is
// a mandatory type: notificationInAppEnabled keeps it in the notification
// centre regardless of preferences, notificationBatched keeps it out of
// digests, so an enabled email channel sends it at once. No actor is recorded
// on purpose — enqueueNotification drops self-notifications, but the whole
// point of a security notice is reaching the owner even when the request
// looked like theirs. The change itself already committed through the
// repository, so like an audit row a failure here is logged rather than
// undoing what was done.
func (s *Server) notifySecurity(r *http.Request, userID, event, body string) {
	payload := map[string]string{"event": event, "body": body}
	if err := s.enqueueNotification(r.Context(), s.repo.Pool(), userID, "", "security", "", payload,
		"notification:security:"+secure.NewID(event)); err != nil {
		slog.WarnContext(r.Context(), "계정 보안 알림 저장 실패", "event", event, "user_id", userID, "error", err)
	}
}
