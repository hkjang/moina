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

func TestPostgreSQLSMTPPasswordIsEncryptedAndWriteOnly(t *testing.T) {
	dsn := os.Getenv("MOINA_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("MOINA_TEST_POSTGRES_DSN is not set")
	}
	repository, err := store.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(repository.Close)
	secrets, err := secure.New(bytes.Repeat([]byte{41}, 32))
	if err != nil {
		t.Fatal(err)
	}
	type savedSetting struct {
		payload   []byte
		sensitive bool
		revision  int64
		updatedBy string
		updatedAt time.Time
	}
	var previous savedSetting
	settingErr := repository.Pool().QueryRow(t.Context(), `SELECT payload,sensitive,revision,updated_by,updated_at FROM settings WHERE key=$1`, settingSMTP).
		Scan(&previous.payload, &previous.sensitive, &previous.revision, &previous.updatedBy, &previous.updatedAt)
	if settingErr != nil && !errors.Is(settingErr, pgx.ErrNoRows) {
		t.Fatal(settingErr)
	}
	hadSetting := settingErr == nil
	actorID := fmt.Sprintf("smtp-settings-admin-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = repository.Pool().Exec(ctx, `DELETE FROM audit_events WHERE actor_id=$1`, actorID)
		if hadSetting {
			_, _ = repository.Pool().Exec(ctx, `INSERT INTO settings(key,payload,sensitive,revision,updated_by,updated_at)
				VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(key) DO UPDATE
				SET payload=EXCLUDED.payload,sensitive=EXCLUDED.sensitive,revision=EXCLUDED.revision,updated_by=EXCLUDED.updated_by,updated_at=EXCLUDED.updated_at`,
				settingSMTP, previous.payload, previous.sensitive, previous.revision, previous.updatedBy, previous.updatedAt)
		} else {
			_, _ = repository.Pool().Exec(ctx, `DELETE FROM settings WHERE key=$1`, settingSMTP)
		}
	})

	server := New(repository, secrets, "v0.1.12-test")
	admin := principal{User: model.User{ID: actorID, Roles: []string{model.RoleSuperAdmin}}, Permissions: []string{"*"}}
	request := func(input map[string]any) *httptest.ResponseRecorder {
		raw, marshalErr := json.Marshal(input)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		httpRequest := httptest.NewRequest(http.MethodPut, "/api/v1/admin/smtp", bytes.NewReader(raw))
		httpRequest.Header.Set("Content-Type", "application/json")
		httpRequest = httpRequest.WithContext(withPrincipal(httpRequest, admin))
		response := httptest.NewRecorder()
		server.adminPutSMTP(response, httpRequest)
		return response
	}
	input := func() map[string]any {
		return map[string]any{
			"enabled": true, "host": "smtp.example.com", "port": 587, "security": "starttls",
			"username": "mailer", "password": "", "fromAddress": "no-reply@example.com",
			"fromName": "MOINA", "timeoutSeconds": 15, "allowPrivateNetwork": false,
		}
	}
	const password = "smtp-password-value"
	create := input()
	create["password"] = password
	response := request(create)
	if response.Code != http.StatusOK || bytes.Contains(response.Body.Bytes(), []byte(password)) {
		t.Fatalf("SMTP password 저장/응답 = %d: %s", response.Code, response.Body.String())
	}
	var encrypted []byte
	var sensitive bool
	if err := repository.Pool().QueryRow(t.Context(), `SELECT payload,sensitive FROM settings WHERE key=$1`, settingSMTP).Scan(&encrypted, &sensitive); err != nil {
		t.Fatal(err)
	}
	if !sensitive || bytes.Contains(encrypted, []byte(password)) {
		t.Fatal("SMTP password가 민감 설정 암호문으로 저장되지 않았습니다")
	}

	response = request(input())
	if response.Code != http.StatusOK {
		t.Fatalf("빈 password로 기존 값 유지 = %d: %s", response.Code, response.Body.String())
	}
	cfg, err := server.smtpConfigContext(t.Context())
	if err != nil || cfg.Password != password {
		t.Fatalf("기존 SMTP password가 유지되지 않았습니다: password=%q err=%v", cfg.Password, err)
	}

	clear := input()
	clear["clearPassword"] = true
	response = request(clear)
	if response.Code != http.StatusOK {
		t.Fatalf("SMTP password 명시적 삭제 = %d: %s", response.Code, response.Body.String())
	}
	cfg, err = server.smtpConfigContext(t.Context())
	if err != nil || cfg.Password != "" {
		t.Fatalf("SMTP password가 삭제되지 않았습니다: password=%q err=%v", cfg.Password, err)
	}
}
