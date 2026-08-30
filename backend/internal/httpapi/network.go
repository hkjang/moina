package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/hkjang/moina/backend/internal/store"
)

const settingNetwork = "network.proxy"

type networkConfig struct {
	TrustedProxies []string `json:"trustedProxies"`
}

type requestNetwork struct {
	SocketIP       string
	ClientIP       string
	ProxyChain     []string
	ForwardedProto string
}

type requestNetworkKey struct{}

func defaultNetwork() networkConfig { return networkConfig{TrustedProxies: []string{}} }

func validateNetwork(cfg *networkConfig) error {
	if len(cfg.TrustedProxies) > 128 {
		return errors.New("신뢰 Proxy는 최대 128개까지 등록할 수 있습니다")
	}
	normalized := make([]string, 0, len(cfg.TrustedProxies))
	for _, raw := range cfg.TrustedProxies {
		value := strings.TrimSpace(raw)
		if value == "" {
			return errors.New("빈 신뢰 Proxy는 등록할 수 없습니다")
		}
		if address, err := netip.ParseAddr(value); err == nil {
			value = address.Unmap().String()
		} else if prefix, prefixErr := netip.ParsePrefix(value); prefixErr == nil {
			value = prefix.Masked().String()
		} else {
			return errors.New("신뢰 Proxy는 정확한 IP 또는 CIDR이어야 합니다")
		}
		if !slices.Contains(normalized, value) {
			normalized = append(normalized, value)
		}
	}
	cfg.TrustedProxies = normalized
	return nil
}

func (s *Server) networkSettings(ctx context.Context) networkConfig {
	now := time.Now()
	s.networkMu.Lock()
	defer s.networkMu.Unlock()
	if !s.networkLoadedAt.IsZero() && now.Sub(s.networkLoadedAt) < 30*time.Second {
		return s.networkCache
	}
	cfg := defaultNetwork()
	if s.repo != nil {
		if err := s.loadSettingContext(ctx, settingNetwork, &cfg); err != nil && !store.IsNotFound(err) {
			slog.WarnContext(ctx, "신뢰 Proxy 설정 조회 실패", "error", err)
			// Fail closed: an unavailable or malformed allowlist never makes
			// forwarding headers trustworthy.
			cfg = defaultNetwork()
		}
	}
	if err := validateNetwork(&cfg); err != nil {
		slog.WarnContext(ctx, "신뢰 Proxy 설정 검증 실패", "error", err)
		cfg = defaultNetwork()
	}
	s.networkCache, s.networkLoadedAt = cfg, now
	return cfg
}

func (s *Server) invalidateNetworkSettings() {
	s.networkMu.Lock()
	s.networkLoadedAt = time.Time{}
	s.networkMu.Unlock()
}

func (s *Server) resolveRequestNetwork(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		socket := remoteAddress(r.RemoteAddr)
		info := requestNetwork{SocketIP: socket, ClientIP: socket}
		cfg := s.networkSettings(r.Context())
		prefixes := trustedPrefixes(cfg)
		if address, err := netip.ParseAddr(socket); err == nil && addressTrusted(address.Unmap(), prefixes) {
			forwarded, proto := forwardedAddresses(r)
			if len(forwarded) > 0 {
				chain := make([]string, 0, len(forwarded)+1)
				for _, address := range forwarded {
					chain = append(chain, address.String())
				}
				chain = append(chain, address.Unmap().String())
				info.ProxyChain = chain
				candidate := address.Unmap()
				for index := len(forwarded) - 1; index >= 0 && addressTrusted(candidate, prefixes); index-- {
					candidate = forwarded[index]
				}
				info.ClientIP = candidate.String()
			}
			if proto == "http" || proto == "https" {
				info.ForwardedProto = proto
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestNetworkKey{}, info)))
	})
}

func trustedPrefixes(cfg networkConfig) []netip.Prefix {
	result := make([]netip.Prefix, 0, len(cfg.TrustedProxies))
	for _, value := range cfg.TrustedProxies {
		if address, err := netip.ParseAddr(value); err == nil {
			address = address.Unmap()
			result = append(result, netip.PrefixFrom(address, address.BitLen()))
		} else if prefix, err := netip.ParsePrefix(value); err == nil {
			result = append(result, prefix.Masked())
		}
	}
	return result
}

func addressTrusted(address netip.Addr, prefixes []netip.Prefix) bool {
	address = address.Unmap()
	for _, prefix := range prefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func forwardedAddresses(r *http.Request) ([]netip.Addr, string) {
	values := make([]string, 0, 8)
	proto := ""
	if header := r.Header.Values("Forwarded"); len(header) > 0 {
		for _, line := range header {
			for _, element := range strings.Split(line, ",") {
				elementProto := ""
				for _, parameter := range strings.Split(element, ";") {
					name, value, found := strings.Cut(strings.TrimSpace(parameter), "=")
					if !found {
						continue
					}
					value = strings.Trim(strings.TrimSpace(value), `"`)
					switch strings.ToLower(name) {
					case "for":
						values = append(values, value)
					case "proto":
						if elementProto != "" {
							return nil, ""
						}
						elementProto = strings.ToLower(value)
					}
				}
				// Forwarded elements are appended left-to-right. Only the closest
				// hop may assert the transport used by the trusted direct peer;
				// an attacker-controlled value at the left must never win.
				proto = elementProto
			}
		}
	} else {
		for _, value := range strings.Split(r.Header.Get("X-Forwarded-For"), ",") {
			if strings.TrimSpace(value) != "" {
				values = append(values, value)
			}
		}
		for _, line := range r.Header.Values("X-Forwarded-Proto") {
			for _, value := range strings.Split(line, ",") {
				proto = strings.ToLower(strings.TrimSpace(value))
			}
		}
	}
	if len(values) > 20 {
		return nil, ""
	}
	addresses := make([]netip.Addr, 0, len(values))
	for _, value := range values {
		address, err := parseForwardedAddress(value)
		if err != nil {
			return nil, ""
		}
		addresses = append(addresses, address)
	}
	return addresses, proto
}

func parseForwardedAddress(value string) (netip.Addr, error) {
	value = strings.Trim(strings.TrimSpace(value), `"`)
	if value == "" || strings.HasPrefix(value, "_") || strings.EqualFold(value, "unknown") {
		return netip.Addr{}, errors.New("invalid forwarded address")
	}
	if address, err := netip.ParseAddr(value); err == nil {
		return address.Unmap(), nil
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		address, parseErr := netip.ParseAddr(strings.Trim(host, "[]"))
		if parseErr == nil {
			return address.Unmap(), nil
		}
	}
	return netip.Addr{}, errors.New("invalid forwarded address")
}

func remoteAddress(value string) string {
	if host, _, err := net.SplitHostPort(value); err == nil {
		return strings.Trim(host, "[]")
	}
	return strings.Trim(value, "[]")
}

func requestNetworkInfo(r *http.Request) requestNetwork {
	if info, ok := r.Context().Value(requestNetworkKey{}).(requestNetwork); ok {
		return info
	}
	socket := remoteAddress(r.RemoteAddr)
	return requestNetwork{SocketIP: socket, ClientIP: socket}
}

func clientIP(r *http.Request) string { return requestNetworkInfo(r).ClientIP }

func auditDetail(r *http.Request, detail any) json.RawMessage {
	value := map[string]any{}
	if detail != nil {
		if raw, err := json.Marshal(detail); err == nil {
			if err := json.Unmarshal(raw, &value); err != nil {
				value = map[string]any{"detail": detail}
			}
		}
	}
	info := requestNetworkInfo(r)
	value["socketIp"] = info.SocketIP
	value["clientIp"] = info.ClientIP
	if len(info.ProxyChain) > 0 {
		value["proxyChain"] = info.ProxyChain
	} else {
		value["proxyChain"] = []string{}
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}
