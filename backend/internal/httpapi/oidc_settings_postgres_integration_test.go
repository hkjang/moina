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

func TestPostgreSQLOIDCSettingsSecretAndInputContract(t *testing.T) {
	dsn := os.Getenv("MOINA_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("MOINA_TEST_POSTGRES_DSN is not set")
	}
	repository, err := store.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(repository.Close)
	secrets, err := secure.New(bytes.Repeat([]byte{37}, 32))
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
	settingErr := repository.Pool().QueryRow(t.Context(), `SELECT payload,sensitive,revision,updated_by,updated_at FROM settings WHERE key=$1`, settingOIDC).
		Scan(&previous.payload, &previous.sensitive, &previous.revision, &previous.updatedBy, &previous.updatedAt)
	if settingErr != nil && !errors.Is(settingErr, pgx.ErrNoRows) {
		t.Fatal(settingErr)
	}
	hadSetting := settingErr == nil
	actorID := fmt.Sprintf("oidc-settings-admin-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = repository.Pool().Exec(ctx, `DELETE FROM audit_events WHERE actor_id=$1`, actorID)
		if hadSetting {
			_, _ = repository.Pool().Exec(ctx, `INSERT INTO settings(key,payload,sensitive,revision,updated_by,updated_at)
				VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(key) DO UPDATE
				SET payload=EXCLUDED.payload,sensitive=EXCLUDED.sensitive,revision=EXCLUDED.revision,updated_by=EXCLUDED.updated_by,updated_at=EXCLUDED.updated_at`,
				settingOIDC, previous.payload, previous.sensitive, previous.revision, previous.updatedBy, previous.updatedAt)
		} else {
			_, _ = repository.Pool().Exec(ctx, `DELETE FROM settings WHERE key=$1`, settingOIDC)
		}
	})

	server := New(repository, secrets, "v0.1.11-test")
	principal := principal{
		User:        model.User{ID: actorID, Roles: []string{model.RoleSuperAdmin}},
		Permissions: []string{"*"},
	}
	requestJSON := func(body map[string]any) *httptest.ResponseRecorder {
		t.Helper()
		raw, marshalErr := json.Marshal(body)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		request := httptest.NewRequest(http.MethodPut, "/api/v1/admin/oidc", bytes.NewReader(raw))
		request.Header.Set("Content-Type", "application/json")
		request = request.WithContext(withPrincipal(request, principal))
		response := httptest.NewRecorder()
		server.adminPutOIDC(response, request)
		return response
	}
	input := func() map[string]any {
		return map[string]any{
			"enabled": false, "issuerUrl": "https://keycloak.internal/realms/moina",
			"clientId": "moina", "clientSecret": "", "scopes": []string{"openid", "profile", "email"},
			"autoProvision": true, "defaultRoles": []string{model.RoleMember}, "roleClaim": "realm_access.roles",
			"roleMappings": map[string][]string{}, "allowedHosts": []string{"keycloak.internal"},
			"privateAllowedHosts": []string{}, "allowInsecureHttp": false,
		}
	}

	const firstSecret = "oidc-client-secret-value"
	create := input()
	create["clientSecret"] = firstSecret
	response := requestJSON(create)
	if response.Code != http.StatusOK {
		t.Fatalf("OIDC secret 저장 = %d: %s", response.Code, response.Body.String())
	}
	if bytes.Contains(response.Body.Bytes(), []byte(firstSecret)) {
		t.Fatal("OIDC 설정 응답에 Client Secret 원문이 노출됐습니다")
	}
	var view struct {
		Data struct {
			ClientSecretConfigured bool `json:"clientSecretConfigured"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil || !view.Data.ClientSecretConfigured {
		t.Fatalf("OIDC secret 설정 여부 응답이 올바르지 않습니다: %s", response.Body.String())
	}

	response = requestJSON(input())
	if response.Code != http.StatusOK {
		t.Fatalf("빈 secret으로 기존 값 유지 = %d: %s", response.Code, response.Body.String())
	}
	config, err := server.oidcConfig(httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil || config.ClientSecret != firstSecret {
		t.Fatalf("기존 OIDC secret이 유지되지 않았습니다: secret=%q err=%v", config.ClientSecret, err)
	}

	unknownField := input()
	unknownField["clientSecretConfigured"] = true
	response = requestJSON(unknownField)
	var apiError struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &apiError); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusBadRequest || apiError.Code != "invalid_json" {
		t.Fatalf("조회 전용 필드 입력 = %d code=%q: %s", response.Code, apiError.Code, response.Body.String())
	}
	config, err = server.oidcConfig(httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil || config.ClientSecret != firstSecret {
		t.Fatalf("거부된 입력이 기존 OIDC secret을 바꿨습니다: secret=%q err=%v", config.ClientSecret, err)
	}

	clear := input()
	clear["clearClientSecret"] = true
	response = requestJSON(clear)
	if response.Code != http.StatusOK {
		t.Fatalf("OIDC secret 명시적 삭제 = %d: %s", response.Code, response.Body.String())
	}
	config, err = server.oidcConfig(httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil || config.ClientSecret != "" {
		t.Fatalf("OIDC secret이 삭제되지 않았습니다: secret=%q err=%v", config.ClientSecret, err)
	}
}
