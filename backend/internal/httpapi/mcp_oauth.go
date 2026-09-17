package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/hkjang/moina/backend/internal/model"
	"github.com/hkjang/moina/backend/internal/observability"
	"github.com/hkjang/moina/backend/internal/store"
)

// MCP를 개인 키 없이 — Keycloak이 발급한 액세스 토큰으로.
//
// The MCP authorization specification (2025-06-18 and later) is OAuth 2.1: the
// MCP server is a *resource server* that publishes where its authorization
// server is (RFC 9728, /.well-known/oauth-protected-resource), and a client
// refused with 401 reads that document, sends the person through Keycloak with
// PKCE, and comes back with an access token whose audience (RFC 8707) is this
// server. Nothing about issuing tokens happens here — Keycloak does that — and
// this file only answers two questions: where is the authorization server, and
// is this token one it issued for us, for an account MOINA already knows.
//
// The personal key stays. A token from SSO is a second door into the same
// room: it authenticates an *existing* account, carries the scopes the
// administrator chose, and passes the same checks a key does. It never creates
// an account — signing in to the web once is what provisions one — and it is
// only accepted on the MCP paths, never on REST, WebSocket or admin routes.

const (
	protectedResourceMetadataPath = "/.well-known/oauth-protected-resource"
	mcpOAuthRealm                 = "MOINA"
	// A JWT far beyond what Keycloak mints is not one; the shape test stops
	// before the verifier would parse it.
	maxMCPOAuthTokenBytes = 32 << 10
)

// mcpOAuthSigningAlgs is the asymmetric set. HS* would let anybody holding the
// public JWKS forge a token, and "none" needs no comment; go-oidc refuses both
// when the list is explicit.
var mcpOAuthSigningAlgs = []string{oidc.RS256, oidc.RS384, oidc.RS512, oidc.ES256, oidc.ES384, oidc.ES512, oidc.PS256, oidc.PS384, oidc.PS512}

// defaultMCPOAuthScopes are the permissions a new personal key gets, so an SSO
// subject starts with exactly what a key would.
func defaultMCPOAuthScopes() []string { return []string{"posts:read", "mcp:use"} }

// isMCPPath names the only routes on which an OAuth bearer is examined. Both
// spellings share the handler and therefore share the challenge.
func isMCPPath(path string) bool {
	return path == "/mcp" || path == "/api/v1/mcp"
}

// looksLikeJWT is the cheap shape test that separates "not a key" from "not a
// token of any kind we accept". A bearer that is neither stays the refusal it
// always was, so a deployment with SSO off says nothing new.
func looksLikeJWT(token string) bool {
	if len(token) > maxMCPOAuthTokenBytes {
		return false
	}
	parts := strings.Split(token, ".")
	return len(parts) == 3 && parts[0] != "" && parts[1] != "" && parts[2] != ""
}

// mcpOAuthState is everything a request needs to know about SSO tokens at
// once: the settings, the identifier this deployment claims, and whether the
// feature is actually in force. Enabled is not the same as active — the
// standard turns the switch into a no-op, with the reason in the log, when the
// issuer, the resource or MCP itself is missing.
type mcpOAuthState struct {
	cfg      model.OIDCConfig
	resource string
	// resourceSource says where the identifier came from, for the admin card.
	resourceSource string
	active         bool
	reason         string
}

func (s *Server) mcpOAuthState(r *http.Request) mcpOAuthState {
	cfg, err := s.oidcConfig(r)
	if err != nil {
		return mcpOAuthState{reason: "OIDC 설정을 읽을 수 없습니다: " + err.Error()}
	}
	return s.mcpOAuthStateFor(r, cfg)
}

func (s *Server) mcpOAuthStateFor(r *http.Request, cfg model.OIDCConfig) mcpOAuthState {
	state := mcpOAuthState{cfg: cfg}
	general, err := s.general(r)
	if err != nil {
		state.reason = "일반 설정을 읽을 수 없습니다: " + err.Error()
		return state
	}
	state.resource, state.resourceSource = mcpOAuthResource(cfg.MCPOAuth, general.PublicBaseURL, r)
	if !cfg.MCPOAuth.Enabled {
		state.reason = "mcp.oauth.enabled가 꺼져 있습니다"
		return state
	}
	api, err := s.apiSettings(r)
	switch {
	case cfg.IssuerURL == "" || cfg.ClientID == "":
		state.reason = "oidc.issuer_url 또는 oidc.client_id가 비어 있습니다"
	case state.resource == "":
		state.reason = "리소스 식별자를 만들 수 없습니다. mcp.oauth.resource 또는 사이트 기본 주소를 설정하세요"
	case err != nil:
		state.reason = "API 접근 설정을 읽을 수 없습니다: " + err.Error()
	case !api.Enabled || !api.MCPEnabled:
		state.reason = "관리자가 API 키 접근 또는 MCP를 꺼 두었습니다"
	default:
		state.active = true
	}
	return state
}

// mcpOAuthResource is the RFC 8707 identifier: what the metadata advertises and
// what a token's aud may name. An explicit setting wins, then the site's public
// base URL — the address clients actually connect to, not the one behind the
// proxy — and only when both are empty the request itself, which anybody can
// spell as they like in a Host header.
func mcpOAuthResource(cfg model.MCPOAuthConfig, publicBaseURL string, r *http.Request) (string, string) {
	if cfg.Resource != "" {
		return cfg.Resource, "explicit"
	}
	if publicBaseURL != "" {
		return strings.TrimRight(publicBaseURL, "/") + "/mcp", "publicBaseUrl"
	}
	if r == nil || r.Host == "" {
		return "", "request"
	}
	scheme := "http"
	if isHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/mcp", "request"
}

// metadataURL is where a refused client is sent to learn the above. RFC 9728
// puts the document for a resource with a path under the well-known prefix plus
// that path, so …/mcp becomes …/.well-known/oauth-protected-resource/mcp.
func (state mcpOAuthState) metadataURL() string {
	parsed, err := url.Parse(state.resource)
	if err != nil || parsed.Host == "" {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host + protectedResourceMetadataPath + parsed.Path
}

// mcpOAuthStatusView is what the administrator card shows next to the
// switch: the identifier, the two addresses to paste into a client, and why
// the feature is inert when it is.
func (s *Server) mcpOAuthStatusView(r *http.Request, cfg model.OIDCConfig) map[string]any {
	state := s.mcpOAuthStateFor(r, cfg)
	view := map[string]any{"active": state.active, "resource": state.resource, "resourceSource": state.resourceSource, "metadataUrl": state.metadataURL(), "scopes": cfg.MCPOAuth.Scopes}
	if !state.active && cfg.MCPOAuth.Enabled {
		view["reason"] = state.reason
	}
	return view
}

// mcpOAuthProviders caches discovery for the issuer. Discovery is a network
// round trip to Keycloak and the JWKS behind it verifies every token; doing it
// per request would put Keycloak's latency in front of every MCP call. The
// cache is keyed by the issuer together with the outbound policy so a changed
// setting is never served by a provider built under the old one. go-oidc
// refetches the key set on an unknown key id, so key rotation needs nothing
// here.
type mcpOAuthProviders struct {
	mu       sync.Mutex
	key      string
	provider *oidc.Provider
}

func (c *mcpOAuthProviders) put(key string, provider *oidc.Provider) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.key, c.provider = key, provider
}

// mcpOAuthProviderKey is the cache identity of a provider: issuer plus every
// policy input the discovery client is built from.
func mcpOAuthProviderKey(cfg model.OIDCConfig) string {
	return strings.Join([]string{cfg.IssuerURL, strings.Join(cfg.AllowedHosts, ","), strings.Join(cfg.PrivateAllowedHosts, ","), fmt.Sprint(cfg.AllowInsecureHTTP)}, "\n")
}

func (s *Server) mcpOAuthProvider(ctx context.Context, cfg model.OIDCConfig) (*oidc.Provider, error) {
	key := mcpOAuthProviderKey(cfg)
	s.mcpOAuth.mu.Lock()
	defer s.mcpOAuth.mu.Unlock()
	if s.mcpOAuth.provider != nil && s.mcpOAuth.key == key {
		return s.mcpOAuth.provider, nil
	}
	client, err := s.outboundClient(cfg.AllowedHosts, cfg.PrivateAllowedHosts, cfg.AllowInsecureHTTP)
	if err != nil {
		return nil, err
	}
	// Discovery must outlive this request: the provider keeps the context for
	// later key fetches, and the client in it is the policy-bound one.
	discoveryCtx, cancel := context.WithTimeout(oidc.ClientContext(context.WithoutCancel(ctx), client), 10*time.Second)
	defer cancel()
	provider, err := oidc.NewProvider(discoveryCtx, cfg.IssuerURL)
	if err != nil {
		return nil, err
	}
	s.mcpOAuth.key, s.mcpOAuth.provider = key, provider
	return provider, nil
}

// mcpOAuthRefusal says why a bearer was not turned into a principal. The
// message is what the client sees; the error is the original cause and is
// logged by the caller so an operator can tell a bad signature from a wrong
// issuer without the client learning either.
type mcpOAuthRefusal struct {
	reason  string
	message string
	err     error
}

// mcpOAuthClaims are the access-token claims checked beyond what go-oidc
// verifies (signature, issuer, exp, nbf, algorithm).
type mcpOAuthClaims struct {
	Type              string          `json:"typ"`
	AuthorizedParty   string          `json:"azp"`
	Scope             string          `json:"scope"`
	Confirmation      json.RawMessage `json:"cnf"`
	PreferredUsername string          `json:"preferred_username"`
}

// mcpOAuthPrincipal turns a bearer access token into a principal, or says
// exactly why it will not. Nothing here writes: no account, no session, no
// identity link. The subject must already exist from a web sign-in.
func (s *Server) mcpOAuthPrincipal(r *http.Request, state mcpOAuthState, token string) (principal, *mcpOAuthRefusal) {
	ctx := r.Context()
	cfg := state.cfg
	provider, err := s.mcpOAuthProvider(ctx, cfg)
	if err != nil {
		return principal{}, &mcpOAuthRefusal{reason: "discovery", err: err, message: "Keycloak 발급자 정보를 읽지 못해 SSO 토큰을 확인할 수 없습니다. 잠시 후 다시 시도하거나 관리자에게 알리세요"}
	}
	// Signature, issuer, exp, nbf and algorithm are the library's. The
	// audience is checked below by hand because more than one value is
	// acceptable and the library compares aud alone — it does not know azp.
	verified, err := provider.Verifier(&oidc.Config{SkipClientIDCheck: true, SupportedSigningAlgs: mcpOAuthSigningAlgs}).Verify(ctx, token)
	if err != nil {
		return principal{}, &mcpOAuthRefusal{reason: "token", err: err, message: "SSO 액세스 토큰이 유효하지 않습니다(서명·발급자·만료·nbf·알고리즘). 클라이언트에서 다시 로그인하세요"}
	}
	var claims mcpOAuthClaims
	if err := verified.Claims(&claims); err != nil {
		return principal{}, &mcpOAuthRefusal{reason: "claims", err: err, message: "SSO 토큰의 내용을 읽을 수 없습니다"}
	}
	if strings.EqualFold(claims.Type, "ID") {
		return principal{}, &mcpOAuthRefusal{reason: "id_token", err: errors.New("typ=ID"), message: "ID 토큰은 로그인 증거이지 API 자격이 아닙니다. 액세스 토큰을 보내세요"}
	}
	if len(claims.Confirmation) > 0 && string(claims.Confirmation) != "null" {
		return principal{}, &mcpOAuthRefusal{reason: "cnf", err: errors.New("cnf present"), message: "소지자 증명(cnf)이 묶인 토큰은 이 서버가 검증할 수 없습니다"}
	}
	subject := strings.TrimSpace(verified.Subject)
	if subject == "" {
		return principal{}, &mcpOAuthRefusal{reason: "subject", err: errors.New("sub missing"), message: "SSO 토큰에 사용자 식별자(sub)가 없습니다"}
	}
	// Whom the token was minted for. Measured against a real Keycloak 26: an
	// access token issued to a client carries that client in azp and
	// aud: ["account"] — the client id is not in aud, whatever an ID token
	// does. So the binding is "aud names this resource, or aud/azp names a
	// client the administrator trusts". Either is the token being for this
	// deployment rather than passed through from another application in the
	// realm, which is what RFC 8707 guards against. The web sign-in client is
	// trusted too: a token Keycloak issued to MOINA's own client is for MOINA.
	accepted := append([]string{state.resource}, cfg.MCPOAuth.Audience...)
	if cfg.ClientID != "" {
		accepted = append(accepted, cfg.ClientID)
	}
	bound := append(slices.Clone(verified.Audience), claims.AuthorizedParty)
	if !slices.ContainsFunc(bound, func(value string) bool { return value != "" && slices.Contains(accepted, value) }) {
		return principal{}, &mcpOAuthRefusal{
			reason: "audience",
			err:    fmt.Errorf("aud %v / azp %q not accepted", verified.Audience, claims.AuthorizedParty),
			message: fmt.Sprintf("SSO 토큰이 이 서버를 위해 발급된 것이 아닙니다(aud=%v, azp=%q). 관리자가 허용 대상에 %q을(를) 더하거나, Keycloak 클라이언트의 Audience 매퍼에 %q을(를) 넣어야 합니다",
				verified.Audience, claims.AuthorizedParty, firstNonemptyString(claims.AuthorizedParty, "<client id>"), state.resource),
		}
	}
	// The same lookup the web sign-in uses, without the provisioning half. The
	// identity link is by issuer and subject only: the web sign-in never links
	// an SSO subject to a local account by username, and a machine presenting
	// a token is not the moment to start.
	user, err := s.repo.UserByOIDC(ctx, cfg.IssuerURL, subject)
	if store.IsNotFound(err) {
		return principal{}, &mcpOAuthRefusal{reason: "account", err: errors.New("no identity link for subject"), message: "이 SSO 계정은 MOINA에 등록되지 않았거나 비활성입니다. 먼저 웹으로 한 번 로그인하세요"}
	}
	if err == nil && !user.Active {
		return principal{}, &mcpOAuthRefusal{reason: "account", err: errors.New("account inactive"), message: "이 SSO 계정은 MOINA에 등록되지 않았거나 비활성입니다. 먼저 웹으로 한 번 로그인하세요"}
	}
	if err != nil {
		return principal{}, &mcpOAuthRefusal{reason: "storage", err: err, message: "SSO 사용자를 확인할 수 없습니다"}
	}
	permissions, err := s.permissionsForRoles(ctx, user.Roles)
	if err != nil {
		return principal{}, &mcpOAuthRefusal{reason: "storage", err: err, message: "사용자 권한을 불러올 수 없습니다"}
	}
	// The ceiling is the administrator's scope list, not the token's — nobody
	// has to teach Keycloak MOINA's permission vocabulary before MCP works. If
	// a token does carry that vocabulary, only the intersection is granted.
	// Either way the result passes through the account's role permissions
	// exactly as a key's scopes do.
	return principal{User: user, APIKey: true, OAuth: true, Permissions: intersectPermissions(permissions, mcpOAuthGrantedScopes(cfg.MCPOAuth.Scopes, claims.Scope))}, nil
}

func mcpOAuthGrantedScopes(configured []string, tokenScope string) []string {
	tokenScopes := strings.Fields(tokenScope)
	if !slices.ContainsFunc(tokenScopes, func(scope string) bool { return slices.Contains(configured, scope) }) {
		return configured
	}
	granted := make([]string, 0, len(configured))
	for _, scope := range configured {
		if slices.Contains(tokenScopes, scope) {
			granted = append(granted, scope)
		}
	}
	return granted
}

// logMCPOAuthRefusal keeps the original cause where the client cannot see
// it. The token itself is never logged.
func logMCPOAuthRefusal(r *http.Request, refusal *mcpOAuthRefusal) {
	observability.Logger(r.Context()).WarnContext(r.Context(), "MCP SSO 토큰 거부", "reason", refusal.reason, "error", refusal.err, "path", r.URL.Path)
}

// mcpOAuthChallenge turns a 401 on an MCP path into an invitation: the client
// reads resource_metadata and starts the OAuth flow from there. It is only
// attached on MCP paths and only while SSO tokens are in force — a REST 401
// with this header would send browsers and other clients somewhere wrong, and
// a deployment with the feature off must answer as it always has.
func mcpOAuthChallenge(w http.ResponseWriter, state mcpOAuthState, refused bool) {
	if !state.active {
		return
	}
	header := fmt.Sprintf(`Bearer realm=%q, resource_metadata=%q`, mcpOAuthRealm, state.metadataURL())
	if refused {
		header += `, error="invalid_token"`
	}
	w.Header().Set("WWW-Authenticate", header)
}

// protectedResourceMetadata is RFC 9728: the document a refused MCP client
// reads to find the authorization server. Public by design — it says where to
// sign in, not who is signed in — and answered as a bare document rather than
// the product's {data:…} envelope, because the reader is an OAuth client
// library. With the feature off it is 404: metadata that points at a server
// whose tokens are then refused would trap clients in a login loop.
func (s *Server) protectedResourceMetadata(w http.ResponseWriter, r *http.Request) {
	state := s.mcpOAuthState(r)
	if !state.active {
		if state.cfg.MCPOAuth.Enabled {
			observability.Logger(r.Context()).WarnContext(r.Context(), "MCP SSO(OAuth)가 켜져 있지만 동작하지 않습니다", "reason", state.reason)
		}
		writeError(w, http.StatusNotFound, "mcp_oauth_disabled", "이 서버의 MCP는 SSO 토큰을 받지 않습니다. 개인 API·MCP 키(mk_)를 사용하세요")
		return
	}
	general, err := s.general(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "settings_unavailable", "서비스 설정을 확인할 수 없습니다")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"resource":                 state.resource,
		"authorization_servers":    []string{state.cfg.IssuerURL},
		"bearer_methods_supported": []string{"header"},
		"scopes_supported":         state.cfg.MCPOAuth.Scopes,
		"resource_name":            general.ServiceName + " MCP",
	})
}
