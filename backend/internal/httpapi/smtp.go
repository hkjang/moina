package httpapi

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hkjang/moina/backend/internal/model"
	"github.com/hkjang/moina/backend/internal/outbound"
	"github.com/hkjang/moina/backend/internal/store"
)

const settingSMTP = "notifications.smtp"

type smtpConfig struct {
	Enabled             bool   `json:"enabled"`
	Host                string `json:"host"`
	Port                int    `json:"port"`
	Security            string `json:"security"`
	Username            string `json:"username"`
	Password            string `json:"password,omitempty"`
	ClearPassword       bool   `json:"clearPassword,omitempty"`
	FromAddress         string `json:"fromAddress"`
	FromName            string `json:"fromName"`
	TimeoutSeconds      int    `json:"timeoutSeconds"`
	AllowPrivateNetwork bool   `json:"allowPrivateNetwork"`
}

type smtpMessage struct {
	To      string
	Subject string
	Body    string
}

func defaultSMTP() smtpConfig {
	return smtpConfig{Port: 587, Security: "starttls", FromName: "MOINA", TimeoutSeconds: 15}
}

func (s *Server) smtpConfigContext(ctx context.Context) (smtpConfig, error) {
	cfg := defaultSMTP()
	if err := s.loadSettingContext(ctx, settingSMTP, &cfg); err != nil && !store.IsNotFound(err) {
		return smtpConfig{}, err
	}
	normalizeSMTP(&cfg)
	return cfg, validateSMTP(cfg)
}

func normalizeSMTP(cfg *smtpConfig) {
	cfg.Host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(cfg.Host), "."))
	cfg.Security = strings.ToLower(strings.TrimSpace(cfg.Security))
	cfg.Username = strings.TrimSpace(cfg.Username)
	cfg.FromAddress = strings.TrimSpace(cfg.FromAddress)
	cfg.FromName = strings.TrimSpace(cfg.FromName)
	if cfg.Port == 0 {
		cfg.Port = 587
	}
	if cfg.Security == "" {
		cfg.Security = "starttls"
	}
	if cfg.TimeoutSeconds == 0 {
		cfg.TimeoutSeconds = 15
	}
}

func validateSMTP(cfg smtpConfig) error {
	if cfg.Port < 1 || cfg.Port > 65535 || cfg.TimeoutSeconds < 3 || cfg.TimeoutSeconds > 60 {
		return errors.New("SMTP 포트와 제한 시간을 확인해 주세요")
	}
	if cfg.Security != "starttls" && cfg.Security != "tls" && cfg.Security != "none" {
		return errors.New("SMTP 보안 방식은 STARTTLS, TLS 또는 암호화 없음 중 하나여야 합니다")
	}
	if !cfg.Enabled {
		return nil
	}
	if cfg.Host == "" || cfg.FromAddress == "" {
		return errors.New("SMTP 서버와 보내는 주소가 필요합니다")
	}
	normalized, err := outbound.NormalizeHosts([]string{cfg.Host})
	if err != nil || len(normalized) != 1 || normalized[0] != cfg.Host {
		return errors.New("SMTP 서버에는 포트 없는 정확한 DNS 이름 또는 IP를 입력해 주세요")
	}
	parsed, err := mail.ParseAddress(cfg.FromAddress)
	if err != nil || parsed.Name != "" || !strings.EqualFold(parsed.Address, cfg.FromAddress) || len(cfg.FromAddress) > 320 {
		return errors.New("보내는 주소는 이름 없는 올바른 이메일 주소여야 합니다")
	}
	if !utf8.ValidString(cfg.FromName) || strings.ContainsFunc(cfg.FromName, func(r rune) bool { return r < 0x20 || r == 0x7f }) || len([]rune(cfg.FromName)) > 80 || len(cfg.Username) > 320 || len(cfg.Password) > 4096 {
		return errors.New("SMTP 발신자 이름 또는 인증 정보를 확인해 주세요")
	}
	if cfg.Security == "none" && (cfg.Username != "" || cfg.Password != "") {
		return errors.New("암호화되지 않은 SMTP 연결에는 인증 정보를 전송할 수 없습니다")
	}
	authority := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	if _, err := outbound.NormalizeHosts([]string{authority}); err != nil {
		return errors.New("SMTP 서버 주소가 올바르지 않습니다")
	}
	if cfg.AllowPrivateNetwork {
		if _, err := outbound.NormalizePrivateHosts([]string{authority}); err != nil {
			return errors.New("사설망 SMTP는 IP가 아닌 정확한 DNS 이름을 사용해야 합니다")
		}
	}
	return nil
}

func smtpView(cfg smtpConfig) map[string]any {
	return map[string]any{
		"enabled": cfg.Enabled, "host": cfg.Host, "port": cfg.Port, "security": cfg.Security,
		"username": cfg.Username, "fromAddress": cfg.FromAddress, "fromName": cfg.FromName,
		"timeoutSeconds": cfg.TimeoutSeconds, "allowPrivateNetwork": cfg.AllowPrivateNetwork,
		"passwordConfigured": cfg.Password != "",
	}
}

func (s *Server) adminGetSMTP(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.smtpConfigContext(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "SMTP 설정을 불러올 수 없습니다")
		return
	}
	writeData(w, http.StatusOK, smtpView(cfg))
}

func (s *Server) adminPutSMTP(w http.ResponseWriter, r *http.Request) {
	var cfg smtpConfig
	if !decodeJSON(w, r, &cfg) {
		return
	}
	old, _ := s.smtpConfigContext(r.Context())
	if cfg.ClearPassword && cfg.Password != "" {
		writeError(w, http.StatusBadRequest, "ambiguous_secret", "SMTP 비밀번호 입력과 삭제를 동시에 요청할 수 없습니다")
		return
	}
	if cfg.ClearPassword {
		cfg.Password = ""
	} else if cfg.Password == "" || cfg.Password == "********" {
		cfg.Password = old.Password
	}
	cfg.ClearPassword = false
	normalizeSMTP(&cfg)
	if err := validateSMTP(cfg); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_config", err.Error())
		return
	}
	if _, err := s.saveSetting(r, settingSMTP, cfg, true, nil); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "SMTP 설정을 저장할 수 없습니다")
		return
	}
	s.audit(r, "smtp.config.update", "setting", settingSMTP, true, map[string]any{
		"enabled": cfg.Enabled, "host": cfg.Host, "port": cfg.Port, "security": cfg.Security,
	})
	writeData(w, http.StatusOK, smtpView(cfg))
}

func (s *Server) adminTestSMTP(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.smtpConfigContext(r.Context())
	if err != nil || !cfg.Enabled {
		writeError(w, http.StatusConflict, "smtp_not_configured", "SMTP를 활성화하고 올바른 설정을 먼저 저장해 주세요")
		return
	}
	recipient, recipientConfigured := bareEmailAddress(getPrincipal(r).User.Email)
	if !recipientConfigured {
		writeError(w, http.StatusConflict, "recipient_email_required", "현재 관리자 프로필에 올바른 이메일 주소를 먼저 저장해 주세요")
		return
	}
	general := defaultGeneral()
	if err := s.loadSettingContext(r.Context(), settingGeneral, &general); err != nil && !store.IsNotFound(err) {
		writeError(w, http.StatusInternalServerError, "storage_error", "서비스 설정을 불러올 수 없습니다")
		return
	}
	message := smtpMessage{
		To: recipient, Subject: general.ServiceName + " SMTP 연결 테스트",
		Body: "SMTP 메일 설정이 정상적으로 연결되었습니다.\n\n이 메일은 관리자 연결 테스트에서 발송되었습니다.",
	}
	if err := deliverSMTP(r.Context(), cfg, message); err != nil {
		writeError(w, http.StatusBadGateway, "smtp_test_failed", "SMTP 테스트 메일을 보낼 수 없습니다: "+err.Error())
		return
	}
	s.audit(r, "smtp.config.test", "setting", settingSMTP, true, map[string]any{"recipient": recipient})
	writeData(w, http.StatusOK, map[string]any{"ok": true, "recipient": recipient})
}

func (s *Server) notificationEmailStatus(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.smtpConfigContext(r.Context())
	_, recipientConfigured := bareEmailAddress(getPrincipal(r).User.Email)
	smtpConfigured := err == nil && cfg.Enabled
	writeData(w, http.StatusOK, map[string]any{
		"available": smtpConfigured && recipientConfigured, "smtpConfigured": smtpConfigured, "recipientConfigured": recipientConfigured,
	})
}

func bareEmailAddress(value string) (string, bool) {
	value = strings.TrimSpace(value)
	parsed, err := mail.ParseAddress(value)
	return value, err == nil && parsed.Name == "" && strings.EqualFold(parsed.Address, value) && len(value) <= 320
}

func deliverSMTP(ctx context.Context, cfg smtpConfig, message smtpMessage) error {
	authority := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	privateHosts := []string{}
	if cfg.AllowPrivateNetwork {
		privateHosts = []string{authority}
	}
	policy := outbound.Policy{AllowedHosts: []string{authority}, PrivateAllowedHosts: privateHosts}
	return deliverSMTPWithDial(ctx, cfg, message, policy.DialContext)
}

type smtpDialFunc func(context.Context, string, string) (net.Conn, error)

func deliverSMTPWithDial(ctx context.Context, cfg smtpConfig, message smtpMessage, dial smtpDialFunc) error {
	if err := validateSMTP(cfg); err != nil {
		return err
	}
	if strings.TrimSpace(message.To) == "" {
		return errors.New("받는 이메일 주소가 없습니다")
	}
	recipient, recipientConfigured := bareEmailAddress(message.To)
	if !recipientConfigured {
		return errors.New("받는 이메일 주소가 올바르지 않습니다")
	}
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	requestContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	authority := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	connection, err := dial(requestContext, "tcp", authority)
	if err != nil {
		return fmt.Errorf("SMTP 서버 연결 실패: %w", err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(timeout))
	tlsConfig := &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}
	if cfg.Security == "tls" {
		secureConnection := tls.Client(connection, tlsConfig)
		if err := secureConnection.HandshakeContext(requestContext); err != nil {
			return fmt.Errorf("SMTP TLS 연결 실패: %w", err)
		}
		connection = secureConnection
	}
	client, err := smtp.NewClient(connection, cfg.Host)
	if err != nil {
		return fmt.Errorf("SMTP 응답 확인 실패: %w", err)
	}
	defer client.Close()
	if cfg.Security == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("SMTP 서버가 STARTTLS를 지원하지 않습니다")
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			return fmt.Errorf("SMTP STARTTLS 실패: %w", err)
		}
	}
	if cfg.Username != "" {
		if ok, _ := client.Extension("AUTH"); !ok {
			return errors.New("SMTP 서버가 인증을 지원하지 않습니다")
		}
		if err := client.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)); err != nil {
			return fmt.Errorf("SMTP 인증 실패: %w", err)
		}
	}
	if err := client.Mail(cfg.FromAddress); err != nil {
		return fmt.Errorf("SMTP 보내는 주소 거부: %w", err)
	}
	if err := client.Rcpt(recipient); err != nil {
		return fmt.Errorf("SMTP 받는 주소 거부: %w", err)
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("SMTP 본문 전송 시작 실패: %w", err)
	}
	if _, err := io.Copy(writer, bytes.NewReader(buildSMTPMessage(cfg, message))); err != nil {
		_ = writer.Close()
		return fmt.Errorf("SMTP 본문 전송 실패: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("SMTP 전송 완료 실패: %w", err)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("SMTP 세션 종료 실패: %w", err)
	}
	return nil
}

func buildSMTPMessage(cfg smtpConfig, message smtpMessage) []byte {
	var buffer bytes.Buffer
	from := (&mail.Address{Name: cfg.FromName, Address: cfg.FromAddress}).String()
	to := (&mail.Address{Address: message.To}).String()
	subject := mime.QEncoding.Encode("UTF-8", strings.ReplaceAll(strings.ReplaceAll(message.Subject, "\r", " "), "\n", " "))
	fmt.Fprintf(&buffer, "From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n", from, to, subject)
	encoded := quotedprintable.NewWriter(&buffer)
	_, _ = encoded.Write([]byte(strings.ReplaceAll(message.Body, "\r\n", "\n")))
	_ = encoded.Close()
	return buffer.Bytes()
}

func notificationEmailMessage(serviceName, publicBaseURL, recipient string, item model.Notification) smtpMessage {
	actorName := ""
	if actor, ok := item.Actor.(map[string]any); ok {
		actorName, _ = actor["displayName"].(string)
	}
	body := strings.TrimSpace(item.Body)
	if body == "" {
		actions := map[string]string{
			"mention": "회원님을 멘션했습니다.", "signal": "회원님의 모인에 Signal을 보냈습니다.",
			"echo": "회원님의 모인에 Echo를 남겼습니다.", "follow": "회원님과 Link했습니다.",
			"quote": "회원님의 모인을 인용했습니다.", "remoin": "회원님의 모인을 Remoin했습니다.",
		}
		body = actions[item.Type]
		if body == "" {
			body = "새로운 알림이 도착했습니다."
		}
		if actorName != "" {
			body = actorName + "님이 " + strings.TrimPrefix(body, "회원님을 ")
		}
	}
	if publicBaseURL != "" && item.TargetPath != "" {
		body += "\n\n확인하기: " + strings.TrimRight(publicBaseURL, "/") + item.TargetPath
	}
	body += "\n\n이 메일은 " + serviceName + " 알림 설정에 따라 발송되었습니다."
	return smtpMessage{To: recipient, Subject: "[" + serviceName + "] " + item.Title, Body: body}
}
