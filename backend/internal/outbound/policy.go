// Package outbound provides per-setting SSRF protection for administrator
// configured OIDC and AI endpoints. Exact host allowlists are enforced both
// before a request and again while resolving the address used by the dialer.
package outbound

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

var (
	ErrHostNotAllowed = errors.New("아웃바운드 대상 호스트가 허용 목록에 없습니다")
	ErrUnsafeAddress  = errors.New("아웃바운드 대상 주소가 안전하지 않습니다")
)

// Policy contains exact DNS names or IP literals, optionally with a port.
// Wildcards are deliberately unsupported because they weaken rebinding checks.
type Policy struct {
	AllowedHosts        []string
	PrivateAllowedHosts []string
	AllowHTTP           bool
	Resolver            *net.Resolver
	Dialer              *net.Dialer
}

// NormalizeHosts canonicalizes, de-duplicates and validates an exact host list.
func NormalizeHosts(values []string) ([]string, error) {
	if len(values) > 64 {
		return nil, errors.New("허용 호스트는 최대 64개까지 등록할 수 있습니다")
	}
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if strings.Contains(value, "://") || strings.ContainsAny(value, "/?#@*") {
			return nil, fmt.Errorf("허용 호스트 %q 형식이 올바르지 않습니다", value)
		}
		canonical, err := canonicalAuthority(value)
		if err != nil {
			return nil, fmt.Errorf("허용 호스트 %q 형식이 올바르지 않습니다", value)
		}
		set[canonical] = struct{}{}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	slices.Sort(result)
	return result, nil
}

// NormalizePrivateHosts accepts DNS hostnames only. Using a literal address as
// the opt-in would remove the DNS identity that is rechecked at dial time.
func NormalizePrivateHosts(values []string) ([]string, error) {
	hosts, err := NormalizeHosts(values)
	if err != nil {
		return nil, err
	}
	for _, authority := range hosts {
		host, _ := splitAuthority(authority)
		if net.ParseIP(strings.Trim(host, "[]")) != nil {
			return nil, fmt.Errorf("사설망 허용 호스트 %q는 IP가 아닌 정확한 DNS 이름이어야 합니다", authority)
		}
	}
	return hosts, nil
}

// EndpointAuthority returns the normalized hostname and non-default port of a
// configured service URL. It is suitable as a backwards-compatible default.
func EndpointAuthority(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" {
		return ""
	}
	host := canonicalHostname(parsed.Hostname())
	port := parsed.Port()
	if port == "" || parsed.Scheme == "https" && port == "443" || parsed.Scheme == "http" && port == "80" {
		return host
	}
	return net.JoinHostPort(host, port)
}

// EnsureEndpointHost adds the configured endpoint host only when upgrading a
// legacy empty allowlist. Once a list exists it is never broadened implicitly.
func EnsureEndpointHost(values []string, endpoint string) []string {
	if len(values) != 0 {
		return values
	}
	if authority := EndpointAuthority(endpoint); authority != "" {
		return []string{authority}
	}
	return []string{}
}

func (p Policy) normalized() (Policy, error) {
	hosts, err := NormalizeHosts(p.AllowedHosts)
	if err != nil {
		return Policy{}, err
	}
	p.AllowedHosts = hosts
	privateHosts, err := NormalizePrivateHosts(p.PrivateAllowedHosts)
	if err != nil {
		return Policy{}, fmt.Errorf("사설망 허용 호스트: %w", err)
	}
	allowed := make(map[string]struct{}, len(hosts))
	for _, host := range hosts {
		allowed[host] = struct{}{}
	}
	for _, host := range privateHosts {
		if _, exists := allowed[host]; !exists {
			return Policy{}, fmt.Errorf("사설망 허용 호스트 %q는 전체 허용 호스트에도 등록해야 합니다", host)
		}
	}
	p.PrivateAllowedHosts = privateHosts
	if p.Resolver == nil {
		p.Resolver = net.DefaultResolver
	}
	if p.Dialer == nil {
		p.Dialer = &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	}
	return p, nil
}

// ValidateURL enforces scheme, exact host membership and URL hygiene.
func (p Policy) ValidateURL(target *url.URL) error {
	policy, err := p.normalized()
	if err != nil {
		return err
	}
	if target == nil || target.Hostname() == "" || target.User != nil || target.Opaque != "" {
		return errors.New("아웃바운드 URL 형식이 올바르지 않습니다")
	}
	if target.Scheme != "https" && !(target.Scheme == "http" && policy.AllowHTTP) {
		return errors.New("허용되지 않은 아웃바운드 URL scheme입니다")
	}
	if !policy.allows(target.Hostname(), target.Port(), target.Scheme) {
		return ErrHostNotAllowed
	}
	return nil
}

func (p Policy) allows(host, port, scheme string) bool {
	return authorityAllowed(p.AllowedHosts, host, port, scheme)
}

func (p Policy) allowsPrivate(host, port, scheme string) bool {
	return authorityAllowed(p.PrivateAllowedHosts, host, port, scheme)
}

func authorityAllowed(allowedHosts []string, host, port, scheme string) bool {
	host = canonicalHostname(host)
	effectivePort := port
	if effectivePort == "" {
		if scheme == "https" {
			effectivePort = "443"
		} else {
			effectivePort = "80"
		}
	}
	defaultPort := "80"
	if scheme == "https" {
		defaultPort = "443"
	}
	for _, allowed := range allowedHosts {
		allowedHost, allowedPort := splitAuthority(allowed)
		if host == allowedHost && (allowedPort == effectivePort || allowedPort == "" && effectivePort == defaultPort) {
			return true
		}
	}
	return false
}

// Client clones the supplied HTTP client and transport, enforcing the policy
// on the initial request, every redirect and the address actually dialed.
func (p Policy) Client(base *http.Client) (*http.Client, error) {
	policy, err := p.normalized()
	if err != nil {
		return nil, err
	}
	if len(policy.AllowedHosts) == 0 {
		return nil, errors.New("아웃바운드 허용 호스트를 하나 이상 등록해야 합니다")
	}
	if base == nil {
		base = &http.Client{}
	}
	client := *base
	baseTransport := base.Transport
	if baseTransport == nil {
		baseTransport = http.DefaultTransport
	}
	transport, ok := baseTransport.(*http.Transport)
	if !ok {
		client.Transport = validatingRoundTripper{next: baseTransport, policy: policy}
	} else {
		clone := transport.Clone()
		// A process-level proxy could otherwise turn the proxy itself into an
		// unvalidated second egress hop and defeat DNS pinning.
		clone.Proxy = nil
		clone.DialContext = policy.dialContext
		clone.DialTLSContext = nil
		client.Transport = validatingRoundTripper{next: clone, policy: policy}
	}
	priorRedirect := base.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if err := policy.ValidateURL(req.URL); err != nil {
			return err
		}
		if len(via) >= 10 {
			return errors.New("아웃바운드 리다이렉트 횟수를 초과했습니다")
		}
		if priorRedirect != nil {
			return priorRedirect(req, via)
		}
		return nil
	}
	return &client, nil
}

type validatingRoundTripper struct {
	next   http.RoundTripper
	policy Policy
}

func (v validatingRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := v.policy.ValidateURL(request.URL); err != nil {
		return nil, err
	}
	return v.next.RoundTrip(request)
}

func (p Policy) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("아웃바운드 주소 형식: %w", err)
	}
	if !p.allows(host, port, schemeForPort(port)) {
		return nil, ErrHostNotAllowed
	}
	addresses, err := p.resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	var lastErr error
	allowPrivate := p.allowsPrivate(host, port, schemeForPort(port))
	for _, ip := range addresses {
		if !validResolvedIP(ip, allowPrivate) {
			lastErr = ErrUnsafeAddress
			continue
		}
		connection, dialErr := p.Dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if dialErr == nil {
			return connection, nil
		}
		lastErr = dialErr
	}
	if lastErr == nil {
		lastErr = errors.New("아웃바운드 호스트에서 사용할 수 있는 IP를 찾지 못했습니다")
	}
	return nil, lastErr
}

func (p Policy) resolve(ctx context.Context, host string) ([]net.IP, error) {
	if literal := net.ParseIP(strings.Trim(host, "[]")); literal != nil {
		return []net.IP{literal}, nil
	}
	values, err := p.Resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("아웃바운드 DNS 해석 실패: %w", err)
	}
	result := make([]net.IP, 0, len(values))
	for _, value := range values {
		result = append(result, net.IP(value.AsSlice()))
	}
	return result, nil
}

func validResolvedIP(ip net.IP, allowPrivate bool) bool {
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || isCarrierGradeNAT(ip) || isMetadataIP(ip) {
		return false
	}
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	address = address.Unmap()
	if address.IsPrivate() {
		return allowPrivate
	}
	if isSpecialUseAddress(address) {
		return false
	}
	// The currently allocated public IPv6 unicast space is 2000::/3. net.IP's
	// IsGlobalUnicast also returns true for discard, translation, deprecated
	// site-local and other non-public special-purpose ranges, so it is not a
	// sufficient SSRF boundary by itself.
	return !address.Is6() || publicIPv6Prefix.Contains(address)
}

var (
	publicIPv6Prefix         = netip.MustParsePrefix("2000::/3")
	deniedSpecialUsePrefixes = []netip.Prefix{
		// IPv4 "this network", protocol-assignment, documentation,
		// deprecated transition, benchmarking and reserved space. RFC1918 is
		// deliberately absent because an exact PrivateAllowedHosts entry may
		// opt a DNS name into those ranges.
		netip.MustParsePrefix("0.0.0.0/8"),
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("192.0.0.0/24"),
		netip.MustParsePrefix("192.0.2.0/24"),
		netip.MustParsePrefix("192.31.196.0/24"),
		netip.MustParsePrefix("192.52.193.0/24"),
		netip.MustParsePrefix("192.88.99.0/24"),
		netip.MustParsePrefix("192.175.48.0/24"),
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("198.51.100.0/24"),
		netip.MustParsePrefix("203.0.113.0/24"),
		netip.MustParsePrefix("240.0.0.0/4"),
		// IPv6 special-purpose assignments inside otherwise public 2000::/3.
		// Translation/discard/site-local ranges outside 2000::/3 are rejected
		// by the public-prefix check above; ULA remains the sole private opt-in.
		netip.MustParsePrefix("2001::/23"),
		netip.MustParsePrefix("2001:db8::/32"),
		netip.MustParsePrefix("2002::/16"),
		netip.MustParsePrefix("2620:4f:8000::/48"),
		netip.MustParsePrefix("3fff::/20"),
	}
)

func isSpecialUseAddress(address netip.Addr) bool {
	for _, prefix := range deniedSpecialUsePrefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func isCarrierGradeNAT(ip net.IP) bool {
	v4 := ip.To4()
	return v4 != nil && v4[0] == 100 && v4[1]&0xc0 == 0x40
}

func isMetadataIP(ip net.IP) bool {
	return ip.Equal(net.ParseIP("169.254.169.254")) || ip.Equal(net.ParseIP("fd00:ec2::254"))
}

func canonicalAuthority(value string) (string, error) {
	if strings.HasSuffix(value, ":") && !strings.HasSuffix(value, "]") {
		return "", errors.New("invalid port")
	}
	host, port := splitAuthority(value)
	if host == "" || !validHostname(host) {
		return "", errors.New("invalid host")
	}
	if port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return "", errors.New("invalid port")
		}
		return net.JoinHostPort(host, port), nil
	}
	return host, nil
}

func validHostname(host string) bool {
	if ip := net.ParseIP(host); ip != nil {
		return true
	}
	if len(host) > 253 || strings.IndexFunc(host, func(r rune) bool {
		return r > unicode.MaxASCII || unicode.IsSpace(r) || unicode.IsControl(r)
	}) >= 0 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if character != '-' && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
				return false
			}
		}
	}
	return true
}

func splitAuthority(value string) (string, string) {
	value = strings.TrimSpace(value)
	if host, port, err := net.SplitHostPort(value); err == nil {
		return canonicalHostname(host), port
	}
	if strings.Count(value, ":") > 1 && net.ParseIP(strings.Trim(value, "[]")) == nil {
		return "", ""
	}
	return canonicalHostname(strings.Trim(value, "[]")), ""
}

func canonicalHostname(value string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
}

func schemeForPort(port string) string {
	if port == "443" {
		return "https"
	}
	return "http"
}
