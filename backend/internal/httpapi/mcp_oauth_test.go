package httpapi

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	"github.com/hkjang/moina/backend/internal/model"
	"github.com/hkjang/moina/backend/internal/secure"
)

// MCP를 개인 키 없이 Keycloak 토큰으로.
//
// The authorization flow itself — PKCE, the redirect, the code exchange — is
// Keycloak's and the client's. What is this server's is the resource-server
// half of the specification, and that is what these tests hold it to: it says
// where the authorization server is, it turns a 401 into a pointer there, and
// it accepts exactly the tokens that server issued for this resource. Every
// refusal below happens before the account lookup, so these run without
// PostgreSQL; the account half is in the integration test next to this file.

// fakeIDP is a Keycloak stand-in that serves discovery and a JWKS for a real
// RSA key and signs whatever claims a test asks for.
type fakeIDP struct {
	server *httptest.Server
	key    *rsa.PrivateKey
}

func newFakeIDP(t *testing.T) *fakeIDP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idp := &fakeIDP{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                idp.server.URL,
			"authorization_endpoint":                idp.server.URL + "/authorize",
			"token_endpoint":                        idp.server.URL + "/token",
			"jwks_uri":                              idp.server.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: key.Public(), KeyID: "realm", Algorithm: "RS256", Use: "sig"}}})
	})
	idp.server = httptest.NewServer(mux)
	t.Cleanup(idp.server.Close)
	return idp
}

// sign mints a token with the realm key, or with another key when a test
// wants a signature the JWKS does not vouch for.
func (idp *fakeIDP) sign(t *testing.T, alg jose.SignatureAlgorithm, key any, claims map[string]any) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: alg, Key: key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "realm"))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := signer.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	token, err := signed.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// accessToken is what Keycloak hands an MCP client after the person signed
// in: signed by the realm key, issued by the issuer, for an audience.
func (idp *fakeIDP) accessToken(t *testing.T, audience any, extra map[string]any) string {
	t.Helper()
	claims := map[string]any{
		"iss": idp.server.URL, "aud": audience, "sub": "subject-mcp", "typ": "Bearer",
		"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "preferred_username": "ssomember",
	}
	for key, value := range extra {
		claims[key] = value
	}
	return idp.sign(t, jose.RS256, idp.key, claims)
}

const mcpListTools = `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`

// mcpOAuthTestServer serves settings from the in-memory cache and, because the
// exact-host outbound policy rightly refuses a loopback issuer, seeds the
// discovery cache with a provider built against the fake IdP under the key
// production would compute. Everything after discovery — JWKS fetch,
// signature, claims — runs through the real verifier.
func mcpOAuthTestServer(t *testing.T, idp *fakeIDP, cfg model.OIDCConfig, general *generalConfig) *Server {
	t.Helper()
	secrets, err := secure.New(bytes.Repeat([]byte{47}, 32))
	if err != nil {
		t.Fatal(err)
	}
	server := New(nil, secrets, "v0.0.0-test")
	normalizeOIDC(&cfg)
	payload, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	server.settings.put(settingOIDC, settingEntry{payload: payload, loadedAt: time.Now()})
	if general != nil {
		generalPayload, err := json.Marshal(general)
		if err != nil {
			t.Fatal(err)
		}
		server.settings.put(settingGeneral, settingEntry{payload: generalPayload, loadedAt: time.Now()})
	} else {
		server.settings.put(settingGeneral, settingEntry{missing: true, loadedAt: time.Now()})
	}
	server.settings.put(settingAPI, settingEntry{missing: true, loadedAt: time.Now()})
	if idp != nil {
		provider, err := oidc.NewProvider(oidc.ClientContext(t.Context(), idp.server.Client()), idp.server.URL)
		if err != nil {
			t.Fatal(err)
		}
		server.mcpOAuth.put(mcpOAuthProviderKey(cfg), provider)
	}
	return server
}

func mcpOAuthConfig(idp *fakeIDP, enabled bool) model.OIDCConfig {
	cfg := defaultOIDC()
	cfg.IssuerURL = idp.server.URL
	cfg.ClientID = "moina-web"
	cfg.AllowInsecureHTTP = true
	cfg.MCPOAuth.Enabled = enabled
	return cfg
}

func mcpCall(handler http.Handler, path, bearer string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "https://moina.example"+path, strings.NewReader(mcpListTools))
	request.Host = "moina.example"
	request.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestMCPOAuthMetadataIsAbsentUntilTurnedOn(t *testing.T) {
	idp := newFakeIDP(t)
	server := mcpOAuthTestServer(t, idp, mcpOAuthConfig(idp, false), nil)
	handler := server.Handler()
	for _, path := range []string{protectedResourceMetadataPath, protectedResourceMetadataPath + "/mcp"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "https://moina.example"+path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s with SSO off: %d %s", path, recorder.Code, recorder.Body.String())
		}
	}
	// A refusal with the feature off is the refusal it always was: no pointer
	// at an authorization server, and a JWT is just another bad credential.
	for _, bearer := range []string{"", idp.accessToken(t, "https://moina.example/mcp", nil)} {
		refusal := mcpCall(handler, "/mcp", bearer)
		if refusal.Code != http.StatusUnauthorized || refusal.Header().Get("WWW-Authenticate") != "" || !strings.Contains(refusal.Body.String(), `"unauthorized"`) {
			t.Fatalf("SSO off, bearer present=%v: %d %q %s", bearer != "", refusal.Code, refusal.Header().Get("WWW-Authenticate"), refusal.Body.String())
		}
	}
}

// Enabled is not active: without an issuer or with MCP off the switch is inert
// and the document stays 404, so a client is never sent to sign in for
// tokens that would then be refused.
func TestMCPOAuthEnabledWithoutPrerequisitesStaysInert(t *testing.T) {
	idp := newFakeIDP(t)
	cases := []struct {
		name   string
		mutate func(cfg *model.OIDCConfig, server *Server)
	}{
		{"issuer 없음", func(cfg *model.OIDCConfig, _ *Server) { cfg.IssuerURL = "" }},
		{"MCP 꺼짐", func(_ *model.OIDCConfig, server *Server) {
			payload, _ := json.Marshal(apiAccessConfig{Enabled: true, MCPEnabled: false, RateLimitPerMinute: 120})
			server.settings.put(settingAPI, settingEntry{payload: payload, loadedAt: time.Now()})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := mcpOAuthConfig(idp, true)
			server := mcpOAuthTestServer(t, idp, cfg, nil)
			tc.mutate(&cfg, server)
			payload, _ := json.Marshal(cfg)
			server.settings.put(settingOIDC, settingEntry{payload: payload, loadedAt: time.Now()})
			handler := server.Handler()
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "https://moina.example"+protectedResourceMetadataPath+"/mcp", nil))
			if recorder.Code != http.StatusNotFound {
				t.Fatalf("metadata: %d", recorder.Code)
			}
			if header := mcpCall(handler, "/mcp", "").Header().Get("WWW-Authenticate"); header != "" {
				t.Fatalf("challenge while inert: %q", header)
			}
			request := httptest.NewRequest(http.MethodGet, "https://moina.example/", nil)
			if status := server.mcpOAuthStatusView(request, cfg); status["active"] != false || status["reason"] == "" {
				t.Fatalf("status view = %v, want inactive with a reason", status)
			}
		})
	}
}

func TestMCPOAuthMetadataAndChallengePointAtKeycloak(t *testing.T) {
	idp := newFakeIDP(t)
	general := defaultGeneral()
	general.ServiceName = "MOINA 연구소"
	general.PublicBaseURL = "https://moina.example"
	cfg := mcpOAuthConfig(idp, true)
	cfg.MCPOAuth.Scopes = []string{"posts:read mcp:use"}
	server := mcpOAuthTestServer(t, idp, cfg, &general)
	handler := server.Handler()

	for _, path := range []string{protectedResourceMetadataPath, protectedResourceMetadataPath + "/mcp"} {
		recorder := httptest.NewRecorder()
		// The document is derived from settings, not from whatever Host the
		// caller spelled, so a proxy-side address does not leak into it.
		request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080"+path, nil)
		request.Host = "127.0.0.1:8080"
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, recorder.Code, recorder.Body.String())
		}
		if origin := recorder.Header().Get("Access-Control-Allow-Origin"); origin != "*" {
			t.Errorf("%s: Access-Control-Allow-Origin %q, want *", path, origin)
		}
		var document map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &document); err != nil {
			t.Fatalf("%s: bare JSON expected, got %s", path, recorder.Body.String())
		}
		if _, enveloped := document["data"]; enveloped {
			t.Errorf("%s: metadata wrapped in the product envelope", path)
		}
		if document["resource"] != "https://moina.example/mcp" {
			t.Errorf("%s: resource %v, want the public base URL + /mcp", path, document["resource"])
		}
		if servers, _ := document["authorization_servers"].([]any); len(servers) != 1 || servers[0] != idp.server.URL {
			t.Errorf("%s: authorization_servers %v", path, document["authorization_servers"])
		}
		if methods, _ := document["bearer_methods_supported"].([]any); len(methods) != 1 || methods[0] != "header" {
			t.Errorf("%s: bearer_methods_supported %v", path, document["bearer_methods_supported"])
		}
		if scopes, _ := document["scopes_supported"].([]any); len(scopes) != 2 || scopes[0] != "posts:read" || scopes[1] != "mcp:use" {
			t.Errorf("%s: scopes_supported %v", path, document["scopes_supported"])
		}
		if document["resource_name"] != "MOINA 연구소 MCP" {
			t.Errorf("%s: resource_name %v", path, document["resource_name"])
		}
	}

	// The 401 carries the pointer on both MCP spellings, with error=invalid_token
	// only when a token was actually examined and refused.
	for _, path := range []string{"/mcp", "/api/v1/mcp"} {
		refusal := mcpCall(handler, path, "")
		if refusal.Code != http.StatusUnauthorized {
			t.Fatalf("%s no bearer: %d", path, refusal.Code)
		}
		header := refusal.Header().Get("WWW-Authenticate")
		if header != `Bearer realm="MOINA", resource_metadata="https://moina.example/.well-known/oauth-protected-resource/mcp"` {
			t.Errorf("%s: WWW-Authenticate %q", path, header)
		}
		refused := mcpCall(handler, path, idp.accessToken(t, "account", map[string]any{"azp": "some-other-app"}))
		if refused.Code != http.StatusUnauthorized || !strings.HasSuffix(refused.Header().Get("WWW-Authenticate"), `, error="invalid_token"`) {
			t.Errorf("%s refused token: %d %q", path, refused.Code, refused.Header().Get("WWW-Authenticate"))
		}
	}
	// A REST 401 never carries it, and a valid-looking token is not examined
	// there at all: OAuth opens MCP, not the API.
	request := httptest.NewRequest(http.MethodGet, "https://moina.example/api/v1/feed", nil)
	request.Header.Set("Authorization", "Bearer "+idp.accessToken(t, "https://moina.example/mcp", nil))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized || recorder.Header().Get("WWW-Authenticate") != "" || !strings.Contains(recorder.Body.String(), `"unauthorized"`) {
		t.Fatalf("REST with an SSO token: %d %q %s", recorder.Code, recorder.Header().Get("WWW-Authenticate"), recorder.Body.String())
	}
}

func TestMCPOAuthResourceFallsBackFromSettingToPublicURLToRequest(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "https://proxy.internal:8443/mcp", nil)
	request.Host = "proxy.internal:8443"
	cases := []struct {
		name, explicit, publicBaseURL, want, source string
	}{
		{"명시 설정", "https://mcp.example/mcp", "https://moina.example", "https://mcp.example/mcp", "explicit"},
		{"사이트 기본 주소", "", "https://moina.example/", "https://moina.example/mcp", "publicBaseUrl"},
		{"요청이 마지막 수단", "", "", "https://proxy.internal:8443/mcp", "request"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, source := mcpOAuthResource(model.MCPOAuthConfig{Resource: tc.explicit}, tc.publicBaseURL, request)
			if got != tc.want || source != tc.source {
				t.Fatalf("resource = %q (%s), want %q (%s)", got, source, tc.want, tc.source)
			}
		})
	}
	if got := (mcpOAuthState{resource: "https://moina.example/mcp"}).metadataURL(); got != "https://moina.example/.well-known/oauth-protected-resource/mcp" {
		t.Fatalf("metadataURL = %q", got)
	}
}

// Each refusal names what it saw, and the audience one says what to change:
// the operator finishes the Keycloak setup from that one message.
func TestMCPOAuthRefusesTokensNotMintedForThisServer(t *testing.T) {
	idp := newFakeIDP(t)
	other := newFakeIDP(t)
	strangerKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	general := defaultGeneral()
	general.PublicBaseURL = "https://moina.example"
	cfg := mcpOAuthConfig(idp, true)
	cfg.MCPOAuth.Audience = []string{"claude-mcp"}
	server := mcpOAuthTestServer(t, idp, cfg, &general)
	handler := server.Handler()
	resource := "https://moina.example/mcp"
	future := time.Now().Add(time.Hour).Unix()

	cases := []struct {
		name    string
		token   string
		message string
	}{
		{"다른 앱용 토큰(aud=account, azp=다른 앱)", idp.accessToken(t, "account", map[string]any{"azp": "some-other-app"}), `aud=[account], azp="some-other-app"`},
		{"거부 메시지가 고칠 값을 말한다", idp.accessToken(t, "account", map[string]any{"azp": "some-other-app"}), `허용 대상에 "some-other-app"`},
		{"거부 메시지가 Audience 매퍼 값을 말한다", idp.accessToken(t, "account", map[string]any{"azp": "some-other-app"}), resource},
		{"만료", idp.accessToken(t, resource, map[string]any{"exp": time.Now().Add(-time.Minute).Unix()}), "서명·발급자·만료"},
		{"다른 issuer", other.sign(t, jose.RS256, other.key, map[string]any{"iss": other.server.URL, "aud": resource, "sub": "s", "exp": future}), "서명·발급자·만료"},
		{"모르는 키의 서명", idp.sign(t, jose.RS256, strangerKey, map[string]any{"iss": idp.server.URL, "aud": resource, "sub": "s", "exp": future}), "서명·발급자·만료"},
		{"HS256", idp.sign(t, jose.HS256, []byte("0123456789abcdef0123456789abcdef"), map[string]any{"iss": idp.server.URL, "aud": resource, "sub": "s", "exp": future}), "서명·발급자·만료"},
		{"typ=ID", idp.accessToken(t, resource, map[string]any{"typ": "ID"}), "ID 토큰"},
		{"cnf 있음", idp.accessToken(t, resource, map[string]any{"cnf": map[string]any{"jkt": "thumb"}}), "cnf"},
		{"nbf 미래", idp.accessToken(t, resource, map[string]any{"nbf": time.Now().Add(10 * time.Minute).Unix()}), "nbf"},
		{"이 서버가 아닌 리소스를 aud로", idp.accessToken(t, "https://other.example/mcp", nil), `aud=[https://other.example/mcp]`},
		{"sub 없음", idp.accessToken(t, resource, map[string]any{"sub": ""}), "sub"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			refusal := mcpCall(handler, "/mcp", tc.token)
			if refusal.Code != http.StatusUnauthorized {
				t.Fatalf("status %d %s", refusal.Code, refusal.Body.String())
			}
			var body struct{ Code, Message string }
			if err := json.Unmarshal(refusal.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Code != "invalid_token" || !strings.Contains(body.Message, tc.message) {
				t.Fatalf("refusal %q %q, want invalid_token containing %q", body.Code, body.Message, tc.message)
			}
			if !strings.Contains(refusal.Header().Get("WWW-Authenticate"), `error="invalid_token"`) {
				t.Fatalf("WWW-Authenticate %q", refusal.Header().Get("WWW-Authenticate"))
			}
		})
	}
}

// The scope ceiling is the administrator's list; a token that carries MOINA's
// vocabulary narrows it and one that does not leaves it whole.
func TestMCPOAuthGrantedScopes(t *testing.T) {
	configured := []string{"posts:read", "mcp:use"}
	cases := []struct {
		name, tokenScope string
		want             []string
	}{
		{"Keycloak 기본 scope", "openid profile email", configured},
		{"빈 scope", "", configured},
		{"앱 어휘가 실리면 교집합", "openid mcp:use", []string{"mcp:use"}},
		{"앱 어휘로 넓힐 수는 없음", "posts:read mcp:use posts:write", configured},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mcpOAuthGrantedScopes(configured, tc.tokenScope); strings.Join(got, " ") != strings.Join(tc.want, " ") {
				t.Fatalf("granted %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLooksLikeJWT(t *testing.T) {
	cases := map[string]bool{"a.b.c": true, "mk_abc": false, "a.b": false, "a..c": false, ".b.c": false, "": false, strings.Repeat("a", maxMCPOAuthTokenBytes) + ".b.c": false}
	for token, want := range cases {
		if got := looksLikeJWT(token); got != want {
			t.Errorf("looksLikeJWT(%q) = %v, want %v", truncateRunes(token, 20), got, want)
		}
	}
}

// Saving the switch without the values it depends on is refused outright,
// and the space separated forms an administrator types are accepted.
func TestValidateOIDCRequiresIssuerForMCPOAuth(t *testing.T) {
	cfg := defaultOIDC()
	cfg.MCPOAuth.Enabled = true
	normalizeOIDC(&cfg)
	if err := validateOIDC(cfg, true); err == nil || !strings.Contains(err.Error(), "issuerUrl") {
		t.Fatalf("enabled without issuer: %v", err)
	}
	cfg.IssuerURL = "https://keycloak.internal/realms/moina"
	cfg.ClientID = "moina-web"
	cfg.MCPOAuth.Audience = []string{"claude-mcp cursor-mcp"}
	cfg.MCPOAuth.Scopes = nil
	normalizeOIDC(&cfg)
	if err := validateOIDC(cfg, true); err != nil {
		t.Fatalf("valid config refused: %v", err)
	}
	if strings.Join(cfg.MCPOAuth.Audience, ",") != "claude-mcp,cursor-mcp" || strings.Join(cfg.MCPOAuth.Scopes, ",") != "posts:read,mcp:use" {
		t.Fatalf("normalized audience %v scopes %v", cfg.MCPOAuth.Audience, cfg.MCPOAuth.Scopes)
	}
	cfg.MCPOAuth.Scopes = []string{"read everything"}
	if err := validateOIDC(cfg, true); err == nil || !strings.Contains(err.Error(), "scopes") {
		t.Fatalf("bad scope accepted: %v", err)
	}
	cfg.MCPOAuth.Scopes = nil
	cfg.MCPOAuth.Resource = "http://moina.example/mcp"
	cfg.AllowInsecureHTTP = false
	if err := validateOIDC(cfg, true); err == nil || !strings.Contains(err.Error(), "resource") {
		t.Fatalf("plain-HTTP resource accepted without the opt-in: %v", err)
	}
	// The web sign-in stays off while MCP SSO is on: the switches are independent.
	if cfg.Enabled {
		t.Fatal("test premise: web sign-in off")
	}
}
