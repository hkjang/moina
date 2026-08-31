package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hkjang/moina/backend/internal/model"
)

func TestGeneralPublicBaseURLValidationAndNormalization(t *testing.T) {
	valid := defaultGeneral()
	valid.ServiceName = "  MOINA  "
	valid.PublicBaseURL = " https://moina.example:8443/ "
	normalizeGeneral(&valid)
	if valid.ServiceName != "MOINA" || valid.PublicBaseURL != "https://moina.example:8443" {
		t.Fatalf("normalized general config = %+v", valid)
	}
	if err := validateGeneral(valid); err != nil {
		t.Fatalf("valid public base URL rejected: %v", err)
	}

	for _, raw := range []string{
		"moina.example",
		"ftp://moina.example",
		"https://user@moina.example",
		"https://moina.example/subpath",
		"https://moina.example?tenant=one",
		"https://moina.example#fragment",
	} {
		cfg := defaultGeneral()
		cfg.PublicBaseURL = raw
		normalizeGeneral(&cfg)
		if err := validateGeneral(cfg); err == nil {
			t.Errorf("invalid public base URL %q accepted", raw)
		}
	}
}

func TestResolveOIDCRedirectPrecedence(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "http://internal-container:8080/api/v1/auth/oidc/login", nil)
	request.Host = "internal-container:8080"

	if got := resolveOIDCRedirect(model.OIDCConfig{}, "https://moina.example", request); got != "https://moina.example/api/v1/auth/oidc/callback" {
		t.Fatalf("public base redirect = %q", got)
	}
	configured := model.OIDCConfig{RedirectURL: "https://login.example/custom/callback"}
	if got := resolveOIDCRedirect(configured, "https://moina.example", request); got != configured.RedirectURL {
		t.Fatalf("explicit redirect = %q", got)
	}
	if got := resolveOIDCRedirect(model.OIDCConfig{}, "", request); got != "http://internal-container:8080/api/v1/auth/oidc/callback" {
		t.Fatalf("request fallback redirect = %q", got)
	}
}

func TestOIDCIssuerNormalizationPreservesTrailingSlash(t *testing.T) {
	cfg := model.OIDCConfig{
		IssuerURL:    "  https://idp.example/tenant/  ",
		AllowedHosts: []string{"idp.example"},
	}
	normalizeOIDC(&cfg)
	if cfg.IssuerURL != "https://idp.example/tenant/" {
		t.Fatalf("issuer = %q, want exact trailing slash", cfg.IssuerURL)
	}
}

func TestOIDCConnectionValidationDoesNotSkipDisabledConfig(t *testing.T) {
	cfg := defaultOIDC()
	if err := validateOIDC(cfg, true); err != nil {
		t.Fatalf("disabled stored config should remain valid: %v", err)
	}
	if err := validateConfiguredOIDC(cfg, true); err == nil {
		t.Fatal("connection test accepted an unconfigured disabled OIDC provider")
	}
}
