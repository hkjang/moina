package outbound

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestNormalizeHosts(t *testing.T) {
	got, err := NormalizeHosts([]string{" AI.INTERNAL. ", "ai.internal", "[2001:db8::1]:8443"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"[2001:db8::1]:8443", "ai.internal"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for _, invalid := range []string{"*.internal", "https://ai.internal", "ai.internal/path", "host:70000", "bad_host", "host:", "bad\nhost"} {
		if _, err := NormalizeHosts([]string{invalid}); err == nil {
			t.Fatalf("expected %q to be rejected", invalid)
		}
	}
}

func TestClientRevalidatesRequestAndRedirect(t *testing.T) {
	redirectTarget := ""
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/redirect" {
			return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{redirectTarget}}, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
		}
		return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
	})
	client, err := (Policy{AllowedHosts: []string{"service.example:8080"}, AllowHTTP: true}).Client(&http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Get("http://service.example:8080")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	redirectTarget = "http://localhost:8080/escaped"
	if _, err := client.Get("http://service.example:8080/redirect"); !errors.Is(err, ErrHostNotAllowed) {
		t.Fatalf("redirect should be blocked, got %v", err)
	}
}

func TestPolicyValidateURL(t *testing.T) {
	policy := Policy{AllowedHosts: []string{"keycloak.internal", "ai.internal:8443"}, AllowHTTP: true}
	for _, raw := range []string{"https://keycloak.internal/realms/moina", "http://ai.internal:8443/v1"} {
		parsed, _ := url.Parse(raw)
		if err := policy.ValidateURL(parsed); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
	}
	parsed, _ := url.Parse("https://evil.internal/v1")
	if err := policy.ValidateURL(parsed); !errors.Is(err, ErrHostNotAllowed) {
		t.Fatalf("unexpected error: %v", err)
	} else {
		var policyError *PolicyError
		if !errors.As(err, &policyError) || policyError.Authority != "evil.internal" || policyError.Reason != PolicyReasonHostNotAllowed {
			t.Fatalf("host diagnostic = %#v", policyError)
		}
	}
	parsed, _ = url.Parse("http://keycloak.internal/realms/moina")
	if err := (Policy{AllowedHosts: []string{"keycloak.internal"}}).ValidateURL(parsed); err == nil {
		t.Fatal("plain HTTP must require an explicit policy")
	}
	parsed, _ = url.Parse("https://keycloak.internal:8443/realms/moina")
	if err := policy.ValidateURL(parsed); !errors.Is(err, ErrHostNotAllowed) {
		t.Fatalf("port 없는 허용 항목은 기본 포트만 허용해야 합니다: %v", err)
	}
}

func TestPolicyPrivateHostValidation(t *testing.T) {
	policy := Policy{AllowedHosts: []string{"keycloak.internal"}, PrivateAllowedHosts: []string{"keycloak.internal"}}
	if _, err := policy.normalized(); err != nil {
		t.Fatal(err)
	}
	if _, err := (Policy{AllowedHosts: []string{"keycloak.internal"}, PrivateAllowedHosts: []string{"other.internal"}}).normalized(); err == nil {
		t.Fatal("사설망 허용 호스트는 전체 허용 목록의 부분집합이어야 합니다")
	}
	if _, err := (Policy{AllowedHosts: []string{"10.0.0.10"}, PrivateAllowedHosts: []string{"10.0.0.10"}}).normalized(); err == nil {
		t.Fatal("사설망 예외는 DNS hostname만 허용해야 합니다")
	}
	for _, raw := range []string{
		"127.0.0.1", "169.254.169.254", "100.64.0.1", "224.0.0.1", "0.0.0.0", "::1", "fe80::1", "fd00:ec2::254",
		"0.0.0.1", "192.0.0.192", "192.0.2.1", "192.31.196.1", "192.52.193.1", "192.88.99.1", "192.175.48.1", "198.18.0.1", "198.51.100.1", "203.0.113.1", "240.0.0.1", "255.255.255.255",
		"64:ff9b::808:808", "64:ff9b:1::1", "100::1", "2001::1", "2001:2::1", "2001:db8::1", "2002:808:808::1", "2620:4f:8000::1", "3fff::1", "5f00::1", "fec0::1", "::ffff:198.18.0.1",
	} {
		if validResolvedIP(net.ParseIP(raw), true) {
			t.Fatalf("%s must always be blocked", raw)
		}
	}
	for _, raw := range []string{"10.0.0.10", "172.16.0.1", "192.168.1.10", "fd00::1"} {
		ip := net.ParseIP(raw)
		if validResolvedIP(ip, false) || !validResolvedIP(ip, true) {
			t.Fatalf("%s must require explicit private permission", raw)
		}
		if allowed, reason, canAllow := resolvedIPDecision(ip, false); allowed || reason != PolicyReasonPrivateNotAllowed || !canAllow {
			t.Fatalf("%s decision = allowed:%t reason:%q canAllow:%t", raw, allowed, reason, canAllow)
		}
	}
	for raw, wantReason := range map[string]string{
		"127.0.0.1":       PolicyReasonLoopback,
		"169.254.1.10":    PolicyReasonLinkLocal,
		"169.254.169.254": PolicyReasonMetadata,
		"100.64.0.1":      PolicyReasonCarrierGradeNAT,
		"192.0.2.1":       PolicyReasonSpecialUse,
	} {
		allowed, reason, canAllow := resolvedIPDecision(net.ParseIP(raw), true)
		if allowed || reason != wantReason || canAllow {
			t.Fatalf("%s decision = allowed:%t reason:%q canAllow:%t, want %q", raw, allowed, reason, canAllow, wantReason)
		}
	}
	if !validResolvedIP(net.ParseIP("8.8.8.8"), false) {
		t.Fatal("public global unicast address should be allowed")
	}
	if !validResolvedIP(net.ParseIP("2606:4700:4700::1111"), false) {
		t.Fatal("public IPv6 global unicast address should be allowed")
	}
}

func TestDialContextReturnsActionableLoopbackDiagnostic(t *testing.T) {
	policy := Policy{AllowedHosts: []string{"127.0.0.1"}, Dialer: &net.Dialer{}}
	_, err := policy.dialContext(t.Context(), "tcp", "127.0.0.1:443")
	if !errors.Is(err, ErrUnsafeAddress) {
		t.Fatalf("dial error = %v", err)
	}
	var policyError *PolicyError
	if !errors.As(err, &policyError) {
		t.Fatalf("dial error type = %T", err)
	}
	if policyError.Authority != "127.0.0.1" || policyError.Reason != PolicyReasonLoopback || policyError.CanAllowPrivate || !reflect.DeepEqual(policyError.ResolvedAddresses, []string{"127.0.0.1"}) {
		t.Fatalf("dial diagnostic = %+v", policyError)
	}
}

func TestEndpointAuthorityAndLegacyDefault(t *testing.T) {
	if got := EndpointAuthority("https://AI.Internal:8443/v1"); got != "ai.internal:8443" {
		t.Fatalf("got %q", got)
	}
	if got := EnsureEndpointHost(nil, "https://keycloak.internal/realms/moina"); !reflect.DeepEqual(got, []string{"keycloak.internal"}) {
		t.Fatalf("got %v", got)
	}
	if got := EnsureEndpointHost([]string{"fixed.internal"}, "https://other.internal"); !reflect.DeepEqual(got, []string{"fixed.internal"}) {
		t.Fatalf("existing list was broadened: %v", got)
	}
}
