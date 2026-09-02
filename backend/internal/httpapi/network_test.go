package httpapi

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"
)

func TestForwardedHeadersRequireTrustedDirectPeer(t *testing.T) {
	server := New(nil, nil, "test")
	server.networkCache = networkConfig{TrustedProxies: []string{"10.0.0.0/24"}}
	server.networkLoadedAt = time.Now()

	var got requestNetwork
	handler := server.resolveRequestNetwork(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = requestNetworkInfo(r)
	}))

	request := httptest.NewRequest(http.MethodGet, "http://moina.internal", nil)
	request.RemoteAddr = "198.51.100.8:49000"
	request.Header.Set("X-Forwarded-For", "203.0.113.9")
	request.Header.Set("X-Forwarded-Proto", "https")
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if got.ClientIP != "198.51.100.8" || got.ForwardedProto != "" || len(got.ProxyChain) != 0 {
		t.Fatalf("untrusted peer spoofing was accepted: %+v", got)
	}
}

func TestTrustedProxyChainUsesFirstUntrustedAddressFromRight(t *testing.T) {
	server := New(nil, nil, "test")
	server.networkCache = networkConfig{TrustedProxies: []string{"10.0.0.0/24"}}
	server.networkLoadedAt = time.Now()

	var got requestNetwork
	handler := server.resolveRequestNetwork(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = requestNetworkInfo(r)
	}))
	request := httptest.NewRequest(http.MethodGet, "http://moina.internal", nil)
	request.RemoteAddr = "10.0.0.5:49000"
	request.Header.Set("Forwarded", `for=192.0.2.44, for=10.0.0.4;proto=https`)
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if got.ClientIP != "192.0.2.44" || got.ForwardedProto != "https" {
		t.Fatalf("trusted chain resolution = %+v", got)
	}
	if !slices.Equal(got.ProxyChain, []string{"192.0.2.44", "10.0.0.4", "10.0.0.5"}) {
		t.Fatalf("proxy chain = %v", got.ProxyChain)
	}
}

func TestTrustedProxyUsesClosestForwardedProto(t *testing.T) {
	server := New(nil, nil, "test")
	server.networkCache = networkConfig{TrustedProxies: []string{"10.0.0.0/24"}}
	server.networkLoadedAt = time.Now()
	handler := server.resolveRequestNetwork(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if got := requestNetworkInfo(r).ForwardedProto; got != "https" {
			t.Fatalf("closest proxy proto = %q, want https", got)
		}
	}))
	for _, headers := range []map[string]string{
		{"Forwarded": `for=192.0.2.44;proto=http, for=10.0.0.4;proto=https`},
		{"X-Forwarded-For": "192.0.2.44, 10.0.0.4", "X-Forwarded-Proto": "http, https"},
	} {
		request := httptest.NewRequest(http.MethodGet, "http://moina.internal", nil)
		request.RemoteAddr = "10.0.0.5:49000"
		for name, value := range headers {
			request.Header.Set(name, value)
		}
		handler.ServeHTTP(httptest.NewRecorder(), request)
	}
}

func TestRepeatedForwardedForLinesFormOneChain(t *testing.T) {
	server := New(nil, nil, "test")
	server.networkCache = networkConfig{TrustedProxies: []string{"10.0.0.0/24"}}
	server.networkLoadedAt = time.Now()

	var got requestNetwork
	handler := server.resolveRequestNetwork(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = requestNetworkInfo(r)
	}))
	request := httptest.NewRequest(http.MethodGet, "http://moina.internal", nil)
	request.RemoteAddr = "10.0.0.5:49000"
	// A client-supplied line followed by the line the trusted proxy appended.
	// Only the rightmost untrusted hop may become the client address.
	request.Header.Add("X-Forwarded-For", "203.0.113.9")
	request.Header.Add("X-Forwarded-For", "192.0.2.44")
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if got.ClientIP != "192.0.2.44" {
		t.Fatalf("client IP = %q, want 192.0.2.44", got.ClientIP)
	}
	if !slices.Equal(got.ProxyChain, []string{"203.0.113.9", "192.0.2.44", "10.0.0.5"}) {
		t.Fatalf("proxy chain = %v", got.ProxyChain)
	}
}

func TestRepeatedForwardedForLinesKeepTrustedHopsTransparent(t *testing.T) {
	server := New(nil, nil, "test")
	server.networkCache = networkConfig{TrustedProxies: []string{"10.0.0.0/24"}}
	server.networkLoadedAt = time.Now()

	var got requestNetwork
	handler := server.resolveRequestNetwork(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = requestNetworkInfo(r)
	}))
	request := httptest.NewRequest(http.MethodGet, "http://moina.internal", nil)
	request.RemoteAddr = "10.0.0.5:49000"
	request.Header.Add("X-Forwarded-For", "192.0.2.44, 10.0.0.3")
	request.Header.Add("X-Forwarded-For", "10.0.0.4")
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if got.ClientIP != "192.0.2.44" {
		t.Fatalf("client IP = %q, want 192.0.2.44", got.ClientIP)
	}
}

func TestNetworkConfigOnlyAcceptsExactIPOrCIDR(t *testing.T) {
	valid := networkConfig{TrustedProxies: []string{"127.0.0.1", "10.20.0.0/16", "127.0.0.1"}}
	if err := validateNetwork(&valid); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(valid.TrustedProxies, []string{"127.0.0.1", "10.20.0.0/16"}) {
		t.Fatalf("normalized proxies = %v", valid.TrustedProxies)
	}
	for _, raw := range []string{"proxy.internal", "10.0.0.1/", "*", ""} {
		invalid := networkConfig{TrustedProxies: []string{raw}}
		if validateNetwork(&invalid) == nil {
			t.Errorf("invalid trusted proxy %q accepted", raw)
		}
	}
}

func TestMemoryRateLimitRemainsAvailableForEmbeddedTests(t *testing.T) {
	server := New(nil, nil, "test")
	for index := 0; index < 2; index++ {
		allowed, err := server.allow(t.Context(), "login|user|127.0.0.1", 2, time.Minute)
		if err != nil || !allowed {
			t.Fatalf("request %d denied: allowed=%t err=%v", index+1, allowed, err)
		}
	}
	allowed, err := server.allow(t.Context(), "login|user|127.0.0.1", 2, time.Minute)
	if err != nil || allowed {
		t.Fatalf("third request allowed=%t err=%v", allowed, err)
	}
}
