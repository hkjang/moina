package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/hkjang/moina/backend/internal/model"
	"github.com/hkjang/moina/backend/internal/outbound"
	"github.com/hkjang/moina/backend/internal/secure"
	"github.com/hkjang/moina/backend/internal/store"
	"golang.org/x/oauth2"
)

type oidcFlow struct {
	State       string    `json:"state"`
	Nonce       string    `json:"nonce"`
	Verifier    string    `json:"verifier"`
	ReturnTo    string    `json:"returnTo"`
	RedirectURL string    `json:"redirectUrl"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

func (s *Server) oidcStatus(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.oidcConfig(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "oidc_unavailable", "OIDC 설정을 확인할 수 없습니다")
		return
	}
	general, err := s.general(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "oidc_unavailable", "서비스 기본 설정을 확인할 수 없습니다")
		return
	}
	redirect := resolveOIDCRedirect(cfg, general.PublicBaseURL, r)
	writeData(w, http.StatusOK, map[string]any{"enabled": cfg.Enabled, "configured": cfg.IssuerURL != "" && cfg.ClientID != "", "issuerUrl": cfg.IssuerURL, "clientId": cfg.ClientID, "redirectUrl": redirect, "clientSecretConfigured": cfg.ClientSecret != "", "allowRegistration": general.AllowRegistration, "registrationEnabled": general.AllowRegistration})
}

func (s *Server) oidcLogin(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.oidcConfig(r)
	if err != nil || !cfg.Enabled {
		writeError(w, http.StatusNotFound, "oidc_disabled", "SSO 로그인이 활성화되지 않았습니다")
		return
	}
	if err := validateOIDC(cfg, true); err != nil {
		writeError(w, http.StatusServiceUnavailable, "oidc_misconfigured", "SSO 설정을 확인해 주세요")
		return
	}
	redirect, err := s.effectiveOIDCRedirect(r, cfg)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "oidc_unavailable", "서비스 기본 설정을 확인할 수 없습니다")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	client, err := s.outboundClient(cfg.AllowedHosts, cfg.PrivateAllowedHosts, cfg.AllowInsecureHTTP)
	if err != nil {
		writeError(w, http.StatusBadGateway, "oidc_egress_denied", "SSO 제공자 호스트가 아웃바운드 정책에 의해 차단되었습니다")
		return
	}
	ctx = oidc.ClientContext(ctx, client)
	provider, err := oidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		writeOIDCDiscoveryError(w, r, err)
		return
	}
	if err := validateOIDCProvider(cfg, provider); err != nil {
		writeError(w, http.StatusBadGateway, "oidc_egress_denied", "SSO discovery가 허용되지 않은 호스트를 반환했습니다")
		return
	}
	state, err := secure.RandomToken(24)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "oidc_flow_failed", "SSO 로그인을 시작할 수 없습니다")
		return
	}
	nonce, err := secure.RandomToken(24)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "oidc_flow_failed", "SSO 로그인을 시작할 수 없습니다")
		return
	}
	returnTo := r.URL.Query().Get("returnTo")
	if !safeReturnTo(returnTo) {
		returnTo = "/"
	}
	flow := oidcFlow{State: state, Nonce: nonce, Verifier: oauth2.GenerateVerifier(), ReturnTo: returnTo, RedirectURL: redirect, ExpiresAt: time.Now().UTC().Add(10 * time.Minute)}
	raw, _ := json.Marshal(flow)
	encrypted, err := s.secrets.Encrypt(raw, "oidc:flow")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "oidc_flow_failed", "SSO 로그인 상태를 보호할 수 없습니다")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: OIDCCookie, Value: base64.RawURLEncoding.EncodeToString(encrypted), Path: "/api/v1/auth/oidc", HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteLaxMode, MaxAge: 600, Expires: flow.ExpiresAt})
	oauthCfg := oauthConfig(cfg, provider, redirect)
	authURL := oauthCfg.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.S256ChallengeOption(flow.Verifier), oauth2.SetAuthURLParam("nonce", nonce))
	http.Redirect(w, r, authURL, http.StatusFound)
}

func (s *Server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.oidcConfig(r)
	if err != nil || !cfg.Enabled {
		writeError(w, http.StatusNotFound, "oidc_disabled", "SSO 로그인이 활성화되지 않았습니다")
		return
	}
	cookie, err := r.Cookie(OIDCCookie)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_oidc_flow", "SSO 로그인 상태가 만료되었습니다")
		return
	}
	clearOIDCCookie(w, r)
	encrypted, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_oidc_flow", "SSO 로그인 상태가 올바르지 않습니다")
		return
	}
	raw, err := s.secrets.Decrypt(encrypted, "oidc:flow")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_oidc_flow", "SSO 로그인 상태가 올바르지 않습니다")
		return
	}
	var flow oidcFlow
	state := r.URL.Query().Get("state")
	if json.Unmarshal(raw, &flow) != nil || time.Now().After(flow.ExpiresAt) || subtle.ConstantTimeCompare([]byte(flow.State), []byte(state)) != 1 {
		writeError(w, http.StatusBadRequest, "invalid_oidc_state", "SSO state 검증에 실패했습니다")
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		writeError(w, http.StatusBadRequest, "missing_code", "SSO 인증 코드가 없습니다")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	client, err := s.outboundClient(cfg.AllowedHosts, cfg.PrivateAllowedHosts, cfg.AllowInsecureHTTP)
	if err != nil {
		writeError(w, http.StatusBadGateway, "oidc_egress_denied", "SSO 제공자 호스트가 아웃바운드 정책에 의해 차단되었습니다")
		return
	}
	ctx = oidc.ClientContext(ctx, client)
	provider, err := oidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		writeOIDCDiscoveryError(w, r, err)
		return
	}
	if err := validateOIDCProvider(cfg, provider); err != nil {
		writeError(w, http.StatusBadGateway, "oidc_egress_denied", "SSO discovery가 허용되지 않은 호스트를 반환했습니다")
		return
	}
	tokens, err := oauthConfig(cfg, provider, flow.RedirectURL).Exchange(ctx, code, oauth2.VerifierOption(flow.Verifier))
	if err != nil {
		writeOIDCExchangeError(w, r, err)
		return
	}
	rawIDToken, ok := tokens.Extra("id_token").(string)
	if !ok {
		writeError(w, http.StatusUnauthorized, "missing_id_token", "SSO ID token이 없습니다")
		return
	}
	idToken, err := provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}).Verify(ctx, rawIDToken)
	if err != nil || subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(flow.Nonce)) != 1 {
		writeError(w, http.StatusUnauthorized, "invalid_id_token", "SSO ID token 검증에 실패했습니다")
		return
	}
	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		writeError(w, http.StatusUnauthorized, "invalid_claims", "SSO 사용자 정보를 확인할 수 없습니다")
		return
	}
	subject := claimString(claims, "sub")
	if subject == "" || !utf8.ValidString(subject) || len([]rune(subject)) > 512 || strings.ContainsAny(subject, "\x00\r\n") {
		writeError(w, http.StatusUnauthorized, "invalid_claims", "SSO 사용자 식별자가 올바르지 않습니다")
		return
	}
	existing, lookupErr := s.repo.UserByOIDC(r.Context(), cfg.IssuerURL, subject)
	if store.IsNotFound(lookupErr) && !cfg.AutoProvision {
		writeError(w, http.StatusForbidden, "provisioning_disabled", "등록되지 않은 SSO 사용자입니다")
		return
	}
	if lookupErr != nil && !store.IsNotFound(lookupErr) {
		writeError(w, http.StatusInternalServerError, "storage_error", "SSO 사용자를 확인할 수 없습니다")
		return
	}
	username := normalizeOIDCUsername(firstNonemptyString(claimString(claims, "preferred_username"), claimString(claims, "email")), subject)
	displayName := strings.TrimSpace(firstNonemptyString(claimString(claims, "name"), username))
	email := strings.TrimSpace(claimString(claims, "email"))
	if !validDisplayName(displayName) || !validEmail(email) {
		writeError(w, http.StatusUnauthorized, "invalid_claims", "SSO 프로필 형식이 올바르지 않습니다")
		return
	}
	roles := oidcRoles(cfg, claims)
	if lookupErr == nil {
		roles = existing.Roles
	}
	candidate := model.User{ID: secure.NewID("usr"), Username: username, DisplayName: displayName, Email: email, AccountType: "human", Provider: "oidc", Roles: roles, Active: true}
	user, err := s.repo.UpsertOIDCUser(r.Context(), candidate, cfg.IssuerURL, subject)
	if err != nil {
		writeError(w, http.StatusConflict, "oidc_user_conflict", "동일한 아이디의 사용자가 있습니다. 관리자에게 문의해 주세요")
		return
	}
	if !user.Active {
		writeError(w, http.StatusForbidden, "inactive_user", "비활성화된 사용자입니다")
		return
	}
	if err := s.issueSession(w, r, user); err != nil {
		writeError(w, http.StatusInternalServerError, "session_failed", "로그인 세션을 만들 수 없습니다")
		return
	}
	permissions, _ := s.repo.PermissionsForRoles(r.Context(), user.Roles)
	r = r.WithContext(withPrincipal(r, principal{User: user, Permissions: permissions}))
	s.audit(r, "auth.oidc.login", "user", user.ID, true, map[string]string{"issuer": cfg.IssuerURL})
	http.Redirect(w, r, flow.ReturnTo, http.StatusFound)
}

func oauthConfig(cfg model.OIDCConfig, provider *oidc.Provider, redirect string) *oauth2.Config {
	endpoint := provider.Endpoint()
	endpoint.AuthStyle = oidcTokenAuthStyle(cfg.ClientSecret, oidcTokenAuthMethods(provider))
	return &oauth2.Config{ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, RedirectURL: redirect, Endpoint: endpoint, Scopes: cfg.Scopes}
}

func validateOIDCProvider(cfg model.OIDCConfig, provider *oidc.Provider) error {
	policy := outbound.Policy{AllowedHosts: cfg.AllowedHosts, PrivateAllowedHosts: cfg.PrivateAllowedHosts, AllowHTTP: cfg.AllowInsecureHTTP}
	endpoint := provider.Endpoint()
	for _, raw := range []string{endpoint.AuthURL, endpoint.TokenURL} {
		parsed, err := url.Parse(raw)
		if err != nil {
			return err
		}
		if err := policy.ValidateURL(parsed); err != nil {
			return err
		}
	}
	return nil
}

func automaticOIDCRedirect(r *http.Request) string {
	scheme := "http"
	if isHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/api/v1/auth/oidc/callback"
}

func resolveOIDCRedirect(cfg model.OIDCConfig, publicBaseURL string, r *http.Request) string {
	if cfg.RedirectURL != "" {
		return cfg.RedirectURL
	}
	redirect, _ := defaultOIDCRedirect(publicBaseURL, r)
	return redirect
}

func defaultOIDCRedirect(publicBaseURL string, r *http.Request) (string, string) {
	if publicBaseURL != "" {
		return strings.TrimRight(publicBaseURL, "/") + "/api/v1/auth/oidc/callback", "publicBaseUrl"
	}
	return automaticOIDCRedirect(r), "request"
}

func (s *Server) effectiveOIDCRedirect(r *http.Request, cfg model.OIDCConfig) (string, error) {
	if cfg.RedirectURL != "" {
		return cfg.RedirectURL, nil
	}
	general, err := s.general(r)
	if err != nil {
		return "", err
	}
	return resolveOIDCRedirect(cfg, general.PublicBaseURL, r), nil
}

func (s *Server) oidcRedirectDetails(r *http.Request, cfg model.OIDCConfig) (effective, fallback, source, fallbackSource string, err error) {
	general, err := s.general(r)
	if err != nil {
		return "", "", "", "", err
	}
	fallback, fallbackSource = defaultOIDCRedirect(general.PublicBaseURL, r)
	if cfg.RedirectURL != "" {
		return cfg.RedirectURL, fallback, "explicit", fallbackSource, nil
	}
	return fallback, fallback, fallbackSource, fallbackSource, nil
}

func safeReturnTo(value string) bool {
	if !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || strings.ContainsAny(value, "\\\r\n") {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && !parsed.IsAbs() && parsed.Host == "" && parsed.Scheme == "" && parsed.Opaque == ""
}

func clearOIDCCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: OIDCCookie, Value: "", Path: "/api/v1/auth/oidc", HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteLaxMode, MaxAge: -1, Expires: time.Unix(1, 0)})
}

func claimString(claims map[string]any, name string) string {
	value, _ := claims[name].(string)
	return value
}

func firstNonemptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

var oidcUsernameInvalid = regexp.MustCompile(`[^\pL\pN._-]+`)

func normalizeOIDCUsername(candidate, subject string) string {
	candidate = strings.TrimSpace(candidate)
	candidate = oidcUsernameInvalid.ReplaceAllString(candidate, "-")
	candidate = strings.Trim(candidate, "-._")
	if len([]rune(candidate)) > 40 {
		candidate = string([]rune(candidate)[:40])
	}
	if validUsername(candidate) {
		return candidate
	}
	suffix := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return -1
	}, subject)
	if len([]rune(suffix)) > 24 {
		suffix = string([]rune(suffix)[:24])
	}
	if suffix == "" {
		suffix = secure.NewID("id")
	}
	return "oidc-" + suffix
}

func oidcRoles(cfg model.OIDCConfig, claims map[string]any) []string {
	set := map[string]bool{}
	for _, role := range cfg.DefaultRoles {
		set[role] = true
	}
	for _, external := range claimStringsAtPath(claims, cfg.RoleClaim) {
		for _, local := range cfg.RoleMappings[external] {
			set[local] = true
		}
	}
	roles := make([]string, 0, len(set))
	for role := range set {
		roles = append(roles, role)
	}
	slices.Sort(roles)
	return roles
}

func claimStringsAtPath(claims map[string]any, path string) []string {
	var value any = claims
	for _, part := range strings.Split(path, ".") {
		object, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		value = object[part]
	}
	switch typed := value.(type) {
	case string:
		return []string{typed}
	case []any:
		items := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				items = append(items, text)
			}
		}
		return items
	default:
		return nil
	}
}

var errOIDCRedirectRejected = errors.New("OIDC redirect URI rejected")

// probeOIDCAuthorization asks the provider to validate the client and redirect
// URI without following its response back to MOINA. Keycloak returns a 400 page
// containing "Invalid parameter: redirect_uri" before it starts a login flow,
// which discovery alone cannot detect.
func probeOIDCAuthorization(ctx context.Context, client *http.Client, cfg model.OIDCConfig, endpoint oauth2.Endpoint, redirect string) error {
	checker := *client
	checker.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	oauthCfg := &oauth2.Config{
		ClientID:    cfg.ClientID,
		RedirectURL: redirect,
		Endpoint:    endpoint,
		Scopes:      cfg.Scopes,
	}
	authURL := oauthCfg.AuthCodeURL(
		"moina-redirect-check",
		oauth2.S256ChallengeOption(oauth2.GenerateVerifier()),
		oauth2.SetAuthURLParam("nonce", "moina-redirect-check"),
		oauth2.SetAuthURLParam("prompt", "none"),
	)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, authURL, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "text/html,application/json")
	response, err := checker.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if err != nil {
		return err
	}
	if oidcRedirectRejected(response.StatusCode, body) {
		return errOIDCRedirectRejected
	}
	if response.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("OIDC authorization endpoint returned status %d", response.StatusCode)
	}
	return nil
}

func oidcRedirectRejected(status int, body []byte) bool {
	if status < http.StatusBadRequest {
		return false
	}
	message := strings.ToLower(string(body))
	mentionsRedirect := strings.Contains(message, "redirect_uri") || strings.Contains(message, "redirect uri") || strings.Contains(message, "redirect url")
	mentionsRejection := strings.Contains(message, "invalid") || strings.Contains(message, "mismatch") || strings.Contains(message, "not registered") || strings.Contains(message, "not allowed")
	return mentionsRedirect && (status == http.StatusBadRequest || mentionsRejection)
}

func (s *Server) adminTestOIDC(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.oidcConfig(r)
	if err != nil || validateConfiguredOIDC(cfg, true) != nil {
		writeError(w, http.StatusBadRequest, "invalid_config", "OIDC 설정을 확인해 주세요")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	client, err := s.outboundClient(cfg.AllowedHosts, cfg.PrivateAllowedHosts, cfg.AllowInsecureHTTP)
	if err != nil {
		writeError(w, http.StatusBadGateway, "oidc_egress_denied", "SSO 제공자 호스트가 아웃바운드 정책에 의해 차단되었습니다")
		return
	}
	ctx = oidc.ClientContext(ctx, client)
	provider, err := oidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		writeAdminOIDCDiscoveryError(w, r, err)
		return
	}
	if err := validateOIDCProvider(cfg, provider); err != nil {
		if !writeAdminOIDCPolicyError(w, r, err, "discovery_endpoint") {
			writeError(w, http.StatusBadGateway, "oidc_egress_denied", "SSO discovery가 허용되지 않은 호스트를 반환했습니다")
		}
		return
	}
	endpoint := provider.Endpoint()
	redirect, err := s.effectiveOIDCRedirect(r, cfg)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "oidc_unavailable", "서비스 기본 설정을 확인할 수 없습니다")
		return
	}
	probeCtx, cancelProbe := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancelProbe()
	if err := probeOIDCAuthorization(probeCtx, client, cfg, endpoint, redirect); err != nil {
		if errors.Is(err, errOIDCRedirectRejected) {
			writeError(w, http.StatusBadRequest, "oidc_redirect_rejected", "Keycloak이 Redirect URI를 거부했습니다. Client의 Valid redirect URIs에 다음 주소를 그대로 등록해 주세요: "+redirect)
			return
		}
		if !writeAdminOIDCPolicyError(w, r, err, "authorization_endpoint") {
			writeError(w, http.StatusBadGateway, "oidc_authorization_failed", "OIDC 인증 endpoint가 Client ID 또는 Redirect URI를 확인하지 못했습니다")
		}
		return
	}
	tokenProbeCtx, cancelTokenProbe := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancelTokenProbe()
	tokenProbeCtx = oidc.ClientContext(tokenProbeCtx, client)
	tokenConfig := oauthConfig(cfg, provider, redirect)
	if err := probeOIDCTokenExchange(tokenProbeCtx, tokenConfig); err != nil {
		writeAdminOIDCExchangeError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"connected": true, "authorizationEndpoint": endpoint.AuthURL, "tokenEndpoint": endpoint.TokenURL, "redirectUrl": redirect, "redirectValidated": true, "clientAuthenticationValidated": true, "tokenAuthMethod": oidcTokenAuthMethod(cfg.ClientSecret, tokenConfig.Endpoint.AuthStyle)})
}
