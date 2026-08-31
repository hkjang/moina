package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"golang.org/x/oauth2"
)

func TestProbeOIDCAuthorizationUsesExactRedirectWithoutFollowingIt(t *testing.T) {
	const redirect = "https://moina.example/api/v1/auth/oidc/callback"
	var received url.Values
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r.URL.Query()
		http.Redirect(w, r, redirect+"?error=login_required", http.StatusFound)
	}))
	defer provider.Close()

	cfg := defaultOIDC()
	cfg.ClientID = "moina"
	err := probeOIDCAuthorization(
		context.Background(),
		provider.Client(),
		cfg,
		oauth2.Endpoint{AuthURL: provider.URL + "/authorize"},
		redirect,
	)
	if err != nil {
		t.Fatalf("valid redirect probe failed: %v", err)
	}
	if received.Get("redirect_uri") != redirect {
		t.Fatalf("redirect_uri = %q, want %q", received.Get("redirect_uri"), redirect)
	}
	if received.Get("client_id") != "moina" || received.Get("response_type") != "code" || received.Get("prompt") != "none" || received.Get("code_challenge_method") != "S256" {
		t.Fatalf("authorization probe query = %v", received)
	}
}

func TestProbeOIDCAuthorizationClassifiesKeycloakRedirectRejection(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "Invalid parameter: redirect_uri", http.StatusBadRequest)
	}))
	defer provider.Close()

	cfg := defaultOIDC()
	cfg.ClientID = "moina"
	err := probeOIDCAuthorization(
		context.Background(),
		provider.Client(),
		cfg,
		oauth2.Endpoint{AuthURL: provider.URL + "/authorize"},
		"https://moina.example/api/v1/auth/oidc/callback",
	)
	if !errors.Is(err, errOIDCRedirectRejected) {
		t.Fatalf("probe error = %v, want errOIDCRedirectRejected", err)
	}
}

func TestOIDCRedirectRejectedRequiresRedirectAndRejectionSignals(t *testing.T) {
	tests := []struct {
		name string
		code int
		body string
		want bool
	}{
		{name: "Keycloak", code: http.StatusBadRequest, body: "Invalid parameter: redirect_uri", want: true},
		{name: "generic provider", code: http.StatusBadRequest, body: "redirect URI mismatch", want: true},
		{name: "invalid client", code: http.StatusBadRequest, body: "Invalid client_id", want: false},
		{name: "successful login page", code: http.StatusOK, body: "Invalid parameter: redirect_uri", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := oidcRedirectRejected(test.code, []byte(test.body)); got != test.want {
				t.Fatalf("oidcRedirectRejected(%d, %q) = %t, want %t", test.code, test.body, got, test.want)
			}
		})
	}
}
