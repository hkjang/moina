package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hkjang/moina/backend/internal/model"
	"github.com/hkjang/moina/backend/internal/secure"
	"golang.org/x/oauth2"
)

// silentTestServer returns a server whose OIDC and general settings are served
// from the in-memory cache, so the handlers under test never touch PostgreSQL.
func silentTestServer(t *testing.T, cfg model.OIDCConfig) *Server {
	t.Helper()
	secrets, err := secure.New(bytes.Repeat([]byte{41}, 32))
	if err != nil {
		t.Fatal(err)
	}
	server := New(nil, secrets, "v0.0.0-test")
	payload, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	server.settings.put(settingOIDC, settingEntry{payload: payload, loadedAt: time.Now()})
	server.settings.put(settingGeneral, settingEntry{missing: true, loadedAt: time.Now()})
	return server
}

func silentFlowCookie(t *testing.T, server *Server, flow oidcFlow) *http.Cookie {
	t.Helper()
	raw, err := json.Marshal(flow)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := server.secrets.Encrypt(raw, "oidc:flow")
	if err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: OIDCCookie, Value: base64.RawURLEncoding.EncodeToString(encrypted)}
}

func TestSilentOIDCRequestedRequiresAutoLogin(t *testing.T) {
	cases := []struct {
		name      string
		autoLogin bool
		query     string
		want      bool
	}{
		{"auto-login 꺼짐이면 prompt=none을 무시", false, "prompt=none", false},
		{"auto-login 켜짐 + prompt=none", true, "prompt=none", true},
		{"auto-login 켜짐이라도 prompt 없음", true, "", false},
		{"다른 prompt 값", true, "prompt=login", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/login?"+tc.query, nil)
			if got := silentOIDCRequested(request, model.OIDCConfig{Enabled: true, AutoLogin: tc.autoLogin}); got != tc.want {
				t.Fatalf("silentOIDCRequested = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestOIDCAuthCodeURLAddsPromptNoneOnlyForSilentFlow(t *testing.T) {
	oauthCfg := &oauth2.Config{ClientID: "moina", RedirectURL: "https://moina.example/api/v1/auth/oidc/callback", Endpoint: oauth2.Endpoint{AuthURL: "https://keycloak.internal/realms/moina/protocol/openid-connect/auth"}, Scopes: []string{"openid"}}
	base := oidcFlow{State: "state-1", Nonce: "nonce-1", Verifier: oauth2.GenerateVerifier()}
	for _, silent := range []bool{false, true} {
		flow := base
		flow.Silent = silent
		parsed, err := url.Parse(oidcAuthCodeURL(oauthCfg, flow))
		if err != nil {
			t.Fatal(err)
		}
		query := parsed.Query()
		if got := query.Get("prompt"); (got == "none") != silent {
			t.Fatalf("silent=%v prompt=%q", silent, got)
		}
		if query.Get("state") != "state-1" || query.Get("nonce") != "nonce-1" || query.Get("code_challenge_method") != "S256" || query.Get("code_challenge") == "" {
			t.Fatalf("silent=%v 인가 요청에 state·nonce·PKCE가 빠졌습니다: %s", silent, parsed.RawQuery)
		}
	}
}

func TestOIDCStatusPublishesAutoLoginOnlyWhenSSOEnabled(t *testing.T) {
	cases := []struct {
		name          string
		enabled, auto bool
		want          bool
	}{
		{"둘 다 켜짐", true, true, true},
		{"SSO 꺼짐이면 auto-login도 꺼짐", false, true, false},
		{"기본값", true, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := defaultOIDC()
			cfg.Enabled, cfg.AutoLogin = tc.enabled, tc.auto
			cfg.IssuerURL, cfg.ClientID = "https://keycloak.internal/realms/moina", "moina"
			server := silentTestServer(t, cfg)
			response := httptest.NewRecorder()
			server.oidcStatus(response, httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/status", nil))
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", response.Code, response.Body.String())
			}
			var body struct {
				Data struct {
					Enabled   bool `json:"enabled"`
					AutoLogin bool `json:"autoLogin"`
				} `json:"data"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Data.AutoLogin != tc.want {
				t.Fatalf("autoLogin = %v, want %v: %s", body.Data.AutoLogin, tc.want, response.Body.String())
			}
		})
	}
}

func TestOIDCCallbackProviderErrorLandsOnLoginWithMarker(t *testing.T) {
	cfg := defaultOIDC()
	cfg.Enabled, cfg.AutoLogin = true, true
	cfg.IssuerURL, cfg.ClientID = "https://keycloak.internal/realms/moina", "moina"
	cases := []struct {
		name     string
		flow     oidcFlow
		query    string
		wantCode int
		wantPath string
	}{
		{
			name:     "조용한 시도의 login_required는 sso=none 표시와 함께 로그인 화면으로",
			flow:     oidcFlow{State: "s1", Silent: true, ReturnTo: "/moin/abc?tab=replies"},
			query:    "error=login_required&error_description=Login+required&state=s1",
			wantCode: http.StatusFound, wantPath: "/login?returnTo=%2Fmoin%2Fabc%3Ftab%3Dreplies&sso=none",
		},
		{
			name:     "조용한 시도의 interaction_required도 같은 답",
			flow:     oidcFlow{State: "s1", Silent: true, ReturnTo: "/"},
			query:    "error=interaction_required&state=s1",
			wantCode: http.StatusFound, wantPath: "/login?sso=none",
		},
		{
			name:     "평범한 로그인의 취소는 sso=error",
			flow:     oidcFlow{State: "s1", ReturnTo: "/flow"},
			query:    "error=access_denied&state=s1",
			wantCode: http.StatusFound, wantPath: "/login?returnTo=%2Fflow&sso=error",
		},
		{
			name:     "state가 맞지 않으면 오류 응답도 redirect하지 않음",
			flow:     oidcFlow{State: "s1", Silent: true, ReturnTo: "/flow"},
			query:    "error=login_required&state=other",
			wantCode: http.StatusBadRequest,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := silentTestServer(t, cfg)
			flow := tc.flow
			flow.Nonce, flow.Verifier, flow.RedirectURL = "n", "v", "https://moina.example/api/v1/auth/oidc/callback"
			flow.ExpiresAt = time.Now().UTC().Add(5 * time.Minute)
			request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/callback?"+tc.query, nil)
			request.AddCookie(silentFlowCookie(t, server, flow))
			response := httptest.NewRecorder()
			server.oidcCallback(response, request)
			if response.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d: %s", response.Code, tc.wantCode, response.Body.String())
			}
			if tc.wantCode != http.StatusFound {
				return
			}
			if got := response.Header().Get("Location"); got != tc.wantPath {
				t.Fatalf("Location = %q, want %q", got, tc.wantPath)
			}
			var cleared bool
			for _, cookie := range response.Result().Cookies() {
				if cookie.Name == OIDCCookie && cookie.MaxAge < 0 {
					cleared = true
				}
			}
			if !cleared {
				t.Fatal("거절된 flow cookie가 지워지지 않았습니다")
			}
		})
	}
}

func TestOIDCProviderErrorTargetKeepsOnlySafeReturnTo(t *testing.T) {
	for _, returnTo := range []string{"//evil.example", "https://evil.example", "\\\\evil", ""} {
		got := oidcProviderErrorTarget(oidcFlow{Silent: true, ReturnTo: returnTo})
		if strings.Contains(got, "returnTo") {
			t.Fatalf("안전하지 않은 returnTo %q가 로그인 주소에 실렸습니다: %s", returnTo, got)
		}
		if got != "/login?sso=none" {
			t.Fatalf("returnTo %q → %s", returnTo, got)
		}
	}
}
