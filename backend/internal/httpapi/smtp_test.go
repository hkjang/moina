package httpapi

import (
	"bufio"
	"context"
	"net"
	"strings"
	"sync"
	"testing"
)

func validSMTPConfig() smtpConfig {
	return smtpConfig{
		Enabled: true, Host: "smtp.example.com", Port: 587, Security: "starttls",
		FromAddress: "no-reply@example.com", FromName: "MOINA", TimeoutSeconds: 15,
	}
}

func TestSMTPValidationKeepsPrivateOptInNarrow(t *testing.T) {
	valid := validSMTPConfig()
	valid.Host = "smtp.internal"
	valid.AllowPrivateNetwork = true
	if err := validateSMTP(valid); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*smtpConfig){
		"host with port": func(cfg *smtpConfig) { cfg.Host = "smtp.internal:587" },
		"private IP":     func(cfg *smtpConfig) { cfg.Host = "10.0.0.8" },
		"plaintext auth": func(cfg *smtpConfig) { cfg.Security, cfg.Username = "none", "mailer" },
		"header address": func(cfg *smtpConfig) { cfg.FromAddress = "MOINA <no-reply@example.com>" },
		"header name":    func(cfg *smtpConfig) { cfg.FromName = "MOINA\r\nBcc: attacker@example.com" },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := valid
			mutate(&cfg)
			if err := validateSMTP(cfg); err == nil {
				t.Fatal("invalid SMTP configuration was accepted")
			}
		})
	}
}

func TestSMTPViewNeverReturnsPassword(t *testing.T) {
	cfg := validSMTPConfig()
	cfg.Password = "secret"
	view := smtpView(cfg)
	if _, exists := view["password"]; exists || view["passwordConfigured"] != true {
		t.Fatalf("SMTP secret view = %#v", view)
	}
}

func TestBareEmailAddressRejectsDisplayNamesAndHeaderControls(t *testing.T) {
	if value, ok := bareEmailAddress(" user@example.com "); !ok || value != "user@example.com" {
		t.Fatalf("valid mailbox = %q, %v", value, ok)
	}
	for _, value := range []string{"", "MOINA <user@example.com>", "user@example.com\r\nBcc: attacker@example.com"} {
		if _, ok := bareEmailAddress(value); ok {
			t.Fatalf("unsafe mailbox was accepted: %q", value)
		}
	}
}

func TestBuildSMTPMessageEncodesUnicodeAndStripsSubjectNewlines(t *testing.T) {
	cfg := validSMTPConfig()
	raw := string(buildSMTPMessage(cfg, smtpMessage{To: "user@example.com", Subject: "새 알림\r\nBcc: attacker@example.com", Body: "안녕하세요"}))
	if strings.Contains(raw, "\r\nBcc:") || !strings.Contains(raw, "Subject: =?UTF-8?q?") || !strings.Contains(raw, "Content-Transfer-Encoding: quoted-printable") {
		t.Fatalf("unsafe or malformed message:\n%s", raw)
	}
}

func TestDeliverSMTPPlaintextUnauthenticatedSession(t *testing.T) {
	cfg := validSMTPConfig()
	cfg.Security = "none"
	var delivered strings.Builder
	dial := func(context.Context, string, string) (net.Conn, error) {
		client, server := net.Pipe()
		go serveFakeSMTP(server, &delivered)
		return client, nil
	}
	message := smtpMessage{To: "user@example.com", Subject: "테스트", Body: "본문"}
	if err := deliverSMTPWithDial(t.Context(), cfg, message, dial); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(delivered.String(), "Subject: =?UTF-8?q?") || !strings.Contains(delivered.String(), "=EB=B3=B8=EB=AC=B8") {
		t.Fatalf("message was not delivered: %s", delivered.String())
	}
}

func serveFakeSMTP(connection net.Conn, delivered *strings.Builder) {
	defer connection.Close()
	_, _ = connection.Write([]byte("220 smtp.example.com ready\r\n"))
	scanner := bufio.NewScanner(connection)
	data := false
	for scanner.Scan() {
		line := scanner.Text()
		if data {
			if line == "." {
				data = false
				_, _ = connection.Write([]byte("250 queued\r\n"))
				continue
			}
			delivered.WriteString(line + "\r\n")
			continue
		}
		switch {
		case strings.HasPrefix(line, "EHLO"):
			_, _ = connection.Write([]byte("250 smtp.example.com\r\n"))
		case strings.HasPrefix(line, "MAIL FROM"), strings.HasPrefix(line, "RCPT TO"):
			_, _ = connection.Write([]byte("250 ok\r\n"))
		case line == "DATA":
			data = true
			_, _ = connection.Write([]byte("354 end with dot\r\n"))
		case line == "QUIT":
			_, _ = connection.Write([]byte("221 bye\r\n"))
			return
		default:
			_, _ = connection.Write([]byte("250 ok\r\n"))
		}
	}
}

func TestNotificationEmailPolicyHonorsCategoriesAndDigest(t *testing.T) {
	preferences := defaultPreferencesDocument().Notifications
	preferences.Email.Enabled = true
	if !notificationEmailEnabled(preferences, "mention") || !notificationEmailEnabled(preferences, "security") {
		t.Fatal("immediate email category was disabled")
	}
	preferences.InApp.Mentions = false
	if notificationEmailEnabled(preferences, "mention") {
		t.Fatal("disabled mention category still emitted email")
	}
	preferences.Digest.Mode = "hourly"
	if notificationEmailEnabled(preferences, "follow") || !notificationEmailEnabled(preferences, "digest") {
		t.Fatal("digest mode did not batch ordinary email notifications")
	}
}

func TestSMTPDefaultsMatchCompanyRelay(t *testing.T) {
	cfg := defaultSMTP()
	if cfg.Enabled || cfg.Port != 25 || cfg.Security != "auto" || cfg.TimeoutSeconds != 10 {
		t.Fatalf("SMTP 기본값 = %+v", cfg)
	}
	for _, event := range mailEvents {
		if !cfg.allows(event) {
			t.Fatalf("기본값에서 %s 이벤트가 꺼져 있습니다", event)
		}
	}
}

func TestSMTPNormalizeKeepsEveryKnownEventOnUnlessSwitchedOff(t *testing.T) {
	legacy := smtpConfig{Enabled: true, Host: "relay.internal", FromAddress: "no-reply@example.com"}
	normalizeSMTP(&legacy)
	if legacy.Port != 25 || legacy.Security != "auto" || legacy.TimeoutSeconds != 10 {
		t.Fatalf("빈 설정의 기본값 = %+v", legacy)
	}
	for _, event := range mailEvents {
		if !legacy.allows(event) {
			t.Fatalf("이벤트 스위치가 없던 설정에서 %s가 꺼졌습니다", event)
		}
	}
	partial := smtpConfig{Notify: map[string]bool{mailEventEcho: false, "reaction": true}}
	normalizeSMTP(&partial)
	if partial.allows(mailEventEcho) || !partial.allows(mailEventMention) || partial.allows("reaction") {
		t.Fatalf("이벤트 스위치 정규화 = %+v", partial.Notify)
	}
	if _, kept := partial.Notify["reaction"]; kept {
		t.Fatal("알 수 없는 이벤트 키가 저장 문서에 남았습니다")
	}
	if !partial.allows(mailEventTest) {
		t.Fatal("시험 발송은 스위치와 무관하게 허용되어야 합니다")
	}
}

func TestSMTPValidationAcceptsAutoSecurity(t *testing.T) {
	cfg := validSMTPConfig()
	cfg.Port, cfg.Security, cfg.Username = 25, "auto", ""
	if err := validateSMTP(cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Security = "opportunistic"
	if err := validateSMTP(cfg); err == nil {
		t.Fatal("알 수 없는 보안 방식이 허용되었습니다")
	}
}

func TestMailEventForOnlyCoversWhatPeopleWaitFor(t *testing.T) {
	expected := map[string]string{
		"approval_requested": mailEventApprovalRequested, "approval_approved": mailEventApprovalDecided,
		"approval_rejected": mailEventApprovalDecided, "mention": mailEventMention, "reply": mailEventEcho, "digest": mailEventDigest,
		"security": mailEventSecurity, "reaction": "", "follow": "", "quote": "", "remoin": "", "": "",
	}
	for kind, event := range expected {
		if got := mailEventFor(kind); got != event {
			t.Errorf("mailEventFor(%q) = %q, %q을 기대했습니다", kind, got, event)
		}
	}
}

func TestSMTPViewExposesSwitchesButNeverPassword(t *testing.T) {
	cfg := validSMTPConfig()
	cfg.Password, cfg.SkipTLSVerify = "secret", true
	normalizeSMTP(&cfg)
	cfg.Notify[mailEventDigest] = false
	view := smtpView(cfg)
	if _, exists := view["password"]; exists || view["passwordConfigured"] != true || view["skipTlsVerify"] != true {
		t.Fatalf("SMTP view = %#v", view)
	}
	notify, ok := view["notify"].(map[string]bool)
	if !ok || notify[mailEventDigest] || !notify[mailEventMention] {
		t.Fatalf("SMTP view notify = %#v", view["notify"])
	}
}

func TestDeliverSMTPAutoFallsBackToPlainWhenRelayOffersNoSTARTTLS(t *testing.T) {
	cfg := validSMTPConfig()
	cfg.Port, cfg.Security, cfg.Username = 25, "auto", ""
	var delivered strings.Builder
	dial := func(context.Context, string, string) (net.Conn, error) {
		client, server := net.Pipe()
		go serveFakeSMTP(server, &delivered)
		return client, nil
	}
	if err := deliverSMTPWithDial(t.Context(), cfg, smtpMessage{To: "user@example.com", Subject: "자동", Body: "본문"}, dial); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(delivered.String(), "Subject: =?UTF-8?q?") {
		t.Fatalf("message was not delivered: %s", delivered.String())
	}
}

func TestDeliverSMTPAutoNeverSendsCredentialsInTheClear(t *testing.T) {
	cfg := validSMTPConfig()
	cfg.Port, cfg.Security, cfg.Username, cfg.Password = 25, "auto", "mailer", "secret"
	var delivered strings.Builder
	recording := &recordingConn{}
	served := make(chan struct{})
	dial := func(context.Context, string, string) (net.Conn, error) {
		client, server := net.Pipe()
		recording.Conn = server
		go func() {
			defer close(served)
			serveFakeSMTP(recording, &delivered)
		}()
		return client, nil
	}
	err := deliverSMTPWithDial(t.Context(), cfg, smtpMessage{To: "user@example.com", Subject: "자동", Body: "본문"}, dial)
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("plain relay with credentials: err=%v", err)
	}
	<-served
	if transcript := recording.String(); strings.Contains(transcript, "AUTH") || strings.Contains(transcript, "secret") || delivered.Len() > 0 {
		t.Fatalf("credentials or body reached the plain relay:\n%s", transcript)
	}
}

// recordingConn keeps what the client wrote so a test can assert on what the
// relay saw. The fake relay reads on its own goroutine, hence the lock.
type recordingConn struct {
	net.Conn
	mu         sync.Mutex
	transcript strings.Builder
}

func (c *recordingConn) Read(buffer []byte) (int, error) {
	n, err := c.Conn.Read(buffer)
	c.mu.Lock()
	c.transcript.Write(buffer[:n])
	c.mu.Unlock()
	return n, err
}

func (c *recordingConn) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.transcript.String()
}
