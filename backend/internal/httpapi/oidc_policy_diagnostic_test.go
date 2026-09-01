package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hkjang/moina/backend/internal/outbound"
)

func TestClassifyAdminOIDCPolicyErrorNamesExactPrivateHost(t *testing.T) {
	err := &outbound.PolicyError{
		Cause:             outbound.ErrUnsafeAddress,
		Authority:         "keycloak.internal:8443",
		ResolvedAddresses: []string{"10.20.30.40"},
		Reason:            outbound.PolicyReasonPrivateNotAllowed,
		CanAllowPrivate:   true,
	}
	failure, ok := classifyAdminOIDCPolicyError(err, "discovery")
	if !ok || failure.Code != "oidc_private_host_required" || failure.Diagnostic.Action != "add_private_host" || failure.Diagnostic.TargetHost != "keycloak.internal:8443" {
		t.Fatalf("failure = %+v, ok=%t", failure, ok)
	}
	for _, value := range []string{"keycloak.internal:8443", "10.20.30.40", "양쪽"} {
		if !strings.Contains(failure.Message, value) {
			t.Errorf("message is missing %q: %s", value, failure.Message)
		}
	}
}

func TestClassifyAdminOIDCPolicyErrorExplainsAlwaysBlockedAddress(t *testing.T) {
	err := &outbound.PolicyError{
		Cause:             outbound.ErrUnsafeAddress,
		Authority:         "keycloak.internal",
		ResolvedAddresses: []string{"127.0.0.1"},
		Reason:            outbound.PolicyReasonLoopback,
	}
	failure, ok := classifyAdminOIDCPolicyError(err, "discovery")
	if !ok || failure.Code != "oidc_address_forbidden" || failure.Diagnostic.Action != "change_dns_or_endpoint" {
		t.Fatalf("failure = %+v, ok=%t", failure, ok)
	}
	if !strings.Contains(failure.Message, "127.0.0.1") || !strings.Contains(failure.Message, "등록해도 항상 차단") {
		t.Fatalf("message = %s", failure.Message)
	}
}

func TestAdminOIDCPolicyDetailsAreProtectedFromPublicLoginError(t *testing.T) {
	err := &outbound.PolicyError{
		Cause:             outbound.ErrUnsafeAddress,
		Authority:         "keycloak.internal",
		ResolvedAddresses: []string{"10.20.30.40"},
		Reason:            outbound.PolicyReasonPrivateNotAllowed,
		CanAllowPrivate:   true,
	}

	publicResponse := httptest.NewRecorder()
	writeOIDCDiscoveryError(publicResponse, httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/login", nil), err)
	if strings.Contains(publicResponse.Body.String(), "10.20.30.40") {
		t.Fatal("public OIDC error exposed an internal resolved address")
	}

	adminResponse := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/oidc/test", nil)
	if !writeAdminOIDCPolicyError(adminResponse, request, err, "discovery") {
		t.Fatal("admin policy error was not handled")
	}
	var payload struct {
		Code    string               `json:"code"`
		Details oidcPolicyDiagnostic `json:"details"`
	}
	if json.Unmarshal(adminResponse.Body.Bytes(), &payload) != nil {
		t.Fatalf("response = %s", adminResponse.Body.String())
	}
	if payload.Code != "oidc_private_host_required" || payload.Details.TargetHost != "keycloak.internal" || len(payload.Details.ResolvedAddresses) != 1 || payload.Details.ResolvedAddresses[0] != "10.20.30.40" {
		t.Fatalf("payload = %+v", payload)
	}
}
