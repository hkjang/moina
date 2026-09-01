package httpapi

import (
	"bufio"
	"context"
	"net"
	"strings"
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
