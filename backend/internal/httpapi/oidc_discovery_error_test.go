package httpapi

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/hkjang/moina/backend/internal/observability"
	"github.com/hkjang/moina/backend/internal/outbound"
)

func TestClassifyOIDCDiscoveryError(t *testing.T) {
	tlsError := &url.Error{
		Op:  http.MethodGet,
		URL: "https://idp.example/.well-known/openid-configuration",
		Err: &tls.CertificateVerificationError{
			Err: x509.UnknownAuthorityError{Cert: &x509.Certificate{}},
		},
	}
	tests := []struct {
		name   string
		err    error
		code   string
		reason string
	}{
		{name: "unsafe private address", err: &url.Error{Op: http.MethodGet, URL: "https://idp.internal", Err: outbound.ErrUnsafeAddress}, code: "oidc_private_host_denied", reason: "unsafe_address"},
		{name: "host not allowed", err: &url.Error{Op: http.MethodGet, URL: "https://redirect.example", Err: outbound.ErrHostNotAllowed}, code: "oidc_egress_denied", reason: "host_not_allowed"},
		{name: "DNS", err: &url.Error{Op: http.MethodGet, URL: "https://idp.internal", Err: &net.DNSError{Err: "no such host", Name: "idp.internal", IsNotFound: true}}, code: "oidc_dns_failed", reason: "dns"},
		{name: "TLS certificate", err: tlsError, code: "oidc_tls_failed", reason: "tls_certificate"},
		{name: "timeout", err: &url.Error{Op: http.MethodGet, URL: "https://idp.example", Err: context.DeadlineExceeded}, code: "oidc_timeout", reason: "timeout"},
		{name: "issuer mismatch", err: &oidc.IssuerMismatchError{Provided: "https://idp.example", Discovered: "https://other.example"}, code: "oidc_issuer_mismatch", reason: "issuer_mismatch"},
		{name: "generic response", err: errors.New("502 Bad Gateway: PRIVATE-UPSTREAM-BODY"), code: "oidc_unavailable", reason: "invalid_discovery_response"},
		{name: "nil remains generic", err: nil, code: "oidc_unavailable", reason: "invalid_discovery_response"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			failure := classifyOIDCDiscoveryError(test.err)
			if failure.Status != http.StatusBadGateway || failure.Code != test.code || failure.Reason != test.reason {
				t.Fatalf("failure = %+v, want status=%d code=%q reason=%q", failure, http.StatusBadGateway, test.code, test.reason)
			}
			if failure.Message == "" {
				t.Fatal("safe client message is empty")
			}
		})
	}
}

func TestWriteOIDCDiscoveryErrorDoesNotExposeUpstreamBody(t *testing.T) {
	const secretBody = "PRIVATE-UPSTREAM-BODY"
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	handler := observability.HTTPMiddleware(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeOIDCDiscoveryError(w, r, errors.New("502 Bad Gateway: "+secretBody))
	}))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/oidc/test", nil)
	request.Header.Set(observability.RequestIDHeader, "oidc-discovery-test")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadGateway)
	}
	var payload map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["code"] != "oidc_unavailable" || !strings.Contains(payload["message"], "openid-configuration") {
		t.Fatalf("response = %v", payload)
	}
	if strings.Contains(response.Body.String(), secretBody) || strings.Contains(logs.String(), secretBody) {
		t.Fatal("upstream discovery body was exposed")
	}
	for _, field := range []string{
		`"request_id":"oidc-discovery-test"`,
		`"error_code":"oidc_unavailable"`,
		`"failure_reason":"invalid_discovery_response"`,
		`"cause_type":"*errors.errorString"`,
	} {
		if !strings.Contains(logs.String(), field) {
			t.Errorf("log is missing %s: %s", field, logs.String())
		}
	}
}

func TestOIDCDiscoveryAcceptsProviderWithTrailingSlashIssuer(t *testing.T) {
	var issuer string
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tenant/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                issuer,
			"authorization_endpoint":                issuer + "authorize",
			"token_endpoint":                        issuer + "token",
			"jwks_uri":                              issuer + "keys",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	}))
	defer providerServer.Close()
	issuer = providerServer.URL + "/tenant/"

	cfg := defaultOIDC()
	cfg.Enabled = true
	cfg.IssuerURL = "  " + issuer + "  "
	cfg.ClientID = "moina"
	cfg.AllowInsecureHTTP = true
	normalizeOIDC(&cfg)
	if cfg.IssuerURL != issuer {
		t.Fatalf("normalized issuer = %q, want %q", cfg.IssuerURL, issuer)
	}

	ctx := oidc.ClientContext(t.Context(), providerServer.Client())
	provider, err := oidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		t.Fatalf("trailing-slash issuer discovery failed: %v", err)
	}
	if err := validateOIDCProvider(cfg, provider); err != nil {
		t.Fatalf("discovered endpoints rejected: %v", err)
	}
}
