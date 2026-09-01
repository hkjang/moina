package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/hkjang/moina/backend/internal/observability"
	"golang.org/x/oauth2"
)

func TestOIDCTokenAuthStyleUsesDiscoveryAndPublicClientRules(t *testing.T) {
	tests := []struct {
		name      string
		secret    string
		methods   []string
		wantStyle oauth2.AuthStyle
		wantName  string
	}{
		{name: "public client", methods: []string{"client_secret_basic"}, wantStyle: oauth2.AuthStyleInParams, wantName: "none"},
		{name: "confidential basic", secret: "secret", methods: []string{"client_secret_post", "client_secret_basic"}, wantStyle: oauth2.AuthStyleInHeader, wantName: "client_secret_basic"},
		{name: "confidential post", secret: "secret", methods: []string{" CLIENT_SECRET_POST "}, wantStyle: oauth2.AuthStyleInParams, wantName: "client_secret_post"},
		{name: "unknown discovery", secret: "secret", methods: nil, wantStyle: oauth2.AuthStyleAutoDetect, wantName: "auto"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			style := oidcTokenAuthStyle(test.secret, test.methods)
			if style != test.wantStyle {
				t.Fatalf("auth style = %d, want %d", style, test.wantStyle)
			}
			if got := oidcTokenAuthMethod(test.secret, style); got != test.wantName {
				t.Fatalf("auth method = %q, want %q", got, test.wantName)
			}
		})
	}
}

func TestOAuthConfigUsesDiscoveredTokenAuthenticationMethod(t *testing.T) {
	var issuer string
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                issuer,
			"authorization_endpoint":                issuer + "/authorize",
			"token_endpoint":                        issuer + "/token",
			"jwks_uri":                              issuer + "/keys",
			"id_token_signing_alg_values_supported": []string{"RS256"},
			"token_endpoint_auth_methods_supported": []string{"client_secret_post", "client_secret_basic"},
		})
	}))
	defer providerServer.Close()
	issuer = providerServer.URL

	ctx := oidc.ClientContext(t.Context(), providerServer.Client())
	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		t.Fatal(err)
	}
	cfg := defaultOIDC()
	cfg.ClientID = "moina"
	cfg.ClientSecret = "secret"
	config := oauthConfig(cfg, provider, "https://moina.example/api/v1/auth/oidc/callback")
	if config.Endpoint.AuthStyle != oauth2.AuthStyleInHeader {
		t.Fatalf("discovered auth style = %d, want Basic header", config.Endpoint.AuthStyle)
	}
	cfg.ClientSecret = ""
	if style := oauthConfig(cfg, provider, config.RedirectURL).Endpoint.AuthStyle; style != oauth2.AuthStyleInParams {
		t.Fatalf("public client auth style = %d, want form params", style)
	}
}

func TestProbeOIDCTokenExchangeValidatesPublicClientWithoutBasicAuth(t *testing.T) {
	const redirect = "https://moina.example/api/v1/auth/oidc/callback"
	var authorization string
	var form url.Values
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		form = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant", "error_description": "Code not valid"})
	}))
	defer provider.Close()

	config := &oauth2.Config{
		ClientID:    "moina-public",
		RedirectURL: redirect,
		Endpoint: oauth2.Endpoint{
			TokenURL:  provider.URL,
			AuthStyle: oauth2.AuthStyleInParams,
		},
	}
	ctx := context.WithValue(t.Context(), oauth2.HTTPClient, provider.Client())
	if err := probeOIDCTokenExchange(ctx, config); err != nil {
		t.Fatalf("public client probe failed: %v", err)
	}
	if authorization != "" {
		t.Fatalf("public client sent Authorization header %q", authorization)
	}
	if form.Get("client_id") != "moina-public" || form.Get("client_secret") != "" {
		t.Fatalf("public client credentials = %v", form)
	}
	if form.Get("grant_type") != "authorization_code" || form.Get("code") == "" || form.Get("code_verifier") == "" || form.Get("redirect_uri") != redirect {
		t.Fatalf("token probe form = %v", form)
	}
}

func TestProbeOIDCTokenExchangeReturnsClientAuthenticationFailure(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_client", "error_description": "Invalid client credentials"})
	}))
	defer provider.Close()

	config := &oauth2.Config{
		ClientID:     "moina",
		ClientSecret: "wrong-secret",
		Endpoint: oauth2.Endpoint{
			TokenURL:  provider.URL,
			AuthStyle: oauth2.AuthStyleInHeader,
		},
	}
	ctx := context.WithValue(t.Context(), oauth2.HTTPClient, provider.Client())
	err := probeOIDCTokenExchange(ctx, config)
	if err == nil {
		t.Fatal("invalid client credentials passed token probe")
	}
	failure := classifyOIDCExchangeError(err)
	if failure.Status != http.StatusUnauthorized || failure.Code != "oidc_client_auth_failed" || failure.Reason != "client_authentication" || failure.OAuthErrorCode != "invalid_client" || failure.UpstreamStatus != http.StatusUnauthorized {
		t.Fatalf("failure = %+v", failure)
	}
}

func TestClassifyOIDCExchangeError(t *testing.T) {
	retrieveError := func(code string) error {
		return &oauth2.RetrieveError{Response: &http.Response{StatusCode: http.StatusBadRequest}, ErrorCode: code}
	}
	tests := []struct {
		name   string
		err    error
		status int
		code   string
		reason string
	}{
		{name: "invalid client", err: retrieveError("invalid_client"), status: http.StatusUnauthorized, code: "oidc_client_auth_failed", reason: "client_authentication"},
		{name: "unauthorized client", err: retrieveError("unauthorized_client"), status: http.StatusUnauthorized, code: "oidc_client_not_allowed", reason: "client_authorization"},
		{name: "invalid grant", err: retrieveError("invalid_grant"), status: http.StatusUnauthorized, code: "oidc_code_rejected", reason: "authorization_code"},
		{name: "invalid request", err: retrieveError("invalid_request"), status: http.StatusBadRequest, code: "oidc_token_request_rejected", reason: "token_request"},
		{name: "provider unavailable", err: retrieveError("temporarily_unavailable"), status: http.StatusBadGateway, code: "oidc_token_unavailable", reason: "provider_unavailable"},
		{name: "generic", err: errors.New("PRIVATE TOKEN RESPONSE"), status: http.StatusBadGateway, code: "oidc_exchange_failed", reason: "invalid_token_response"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			failure := classifyOIDCExchangeError(test.err)
			if failure.Status != test.status || failure.Code != test.code || failure.Reason != test.reason || failure.Message == "" {
				t.Fatalf("failure = %+v, want status=%d code=%q reason=%q", failure, test.status, test.code, test.reason)
			}
		})
	}
}

func TestWriteOIDCExchangeErrorDoesNotExposeUpstreamDetails(t *testing.T) {
	const secretBody = "PRIVATE-TOKEN-RESPONSE"
	const secretDescription = "secret from upstream"
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	handler := observability.HTTPMiddleware(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeOIDCExchangeError(w, r, &oauth2.RetrieveError{
			Response:         &http.Response{StatusCode: http.StatusUnauthorized},
			Body:             []byte(secretBody),
			ErrorCode:        "invalid_client",
			ErrorDescription: secretDescription,
		})
	}))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/callback", nil)
	request.Header.Set(observability.RequestIDHeader, "oidc-exchange-test")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
	for _, exposed := range []string{secretBody, secretDescription} {
		if strings.Contains(response.Body.String(), exposed) || strings.Contains(logs.String(), exposed) {
			t.Fatalf("upstream token detail %q was exposed", exposed)
		}
	}
	for _, field := range []string{
		`"request_id":"oidc-exchange-test"`,
		`"error_code":"oidc_client_auth_failed"`,
		`"failure_reason":"client_authentication"`,
		`"oauth_error_code":"invalid_client"`,
		`"upstream_status":401`,
		`"cause_type":"*oauth2.RetrieveError"`,
	} {
		if !strings.Contains(logs.String(), field) {
			t.Errorf("log is missing %s: %s", field, logs.String())
		}
	}
}
