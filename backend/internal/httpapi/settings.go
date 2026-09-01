package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/hkjang/moina/backend/internal/model"
	"github.com/hkjang/moina/backend/internal/outbound"
	"github.com/hkjang/moina/backend/internal/store"
)

const (
	settingGeneral  = "service.general"
	settingOIDC     = "auth.oidc"
	settingAI       = "ai.config"
	settingWorkflow = "workflow.approval"
	settingAPI      = "api.access"
	settingMedia    = "media.config"
)

type generalConfig struct {
	ServiceName       string `json:"serviceName"`
	PublicBaseURL     string `json:"publicBaseUrl,omitempty"`
	AllowRegistration bool   `json:"allowRegistration"`
	SessionMinutes    int    `json:"sessionMinutes"`
	DefaultTimezone   string `json:"defaultTimezone"`
}

type apiAccessConfig struct {
	Enabled            bool `json:"enabled"`
	MCPEnabled         bool `json:"mcpEnabled"`
	RateLimitPerMinute int  `json:"rateLimitPerMinute"`
}

type mediaConfig struct {
	MaxUploadBytes int64 `json:"maxUploadBytes"`
	MaxPerPost     int   `json:"maxPerPost"`
	OrphanTTLHours int   `json:"orphanTtlHours"`
}

func defaultGeneral() generalConfig {
	return generalConfig{ServiceName: "moina", AllowRegistration: false, SessionMinutes: 720, DefaultTimezone: "Asia/Seoul"}
}

func defaultAPIAccess() apiAccessConfig {
	return apiAccessConfig{Enabled: true, MCPEnabled: true, RateLimitPerMinute: 120}
}

func defaultMedia() mediaConfig {
	return mediaConfig{MaxUploadBytes: 10 << 20, MaxPerPost: 4, OrphanTTLHours: 24}
}

func defaultOIDC() model.OIDCConfig {
	return model.OIDCConfig{Enabled: false, Scopes: []string{"openid", "profile", "email"}, AutoProvision: true, DefaultRoles: []string{model.RoleMember}, RoleClaim: "realm_access.roles", RoleMappings: map[string][]string{}}
}

func defaultAI() model.AIConfig {
	return model.AIConfig{Enabled: false, APIStyle: "responses", DefaultMaxTokens: 4096, MaxTokens: 262144, TimeoutSeconds: 300}
}

func defaultWorkflow() model.WorkflowConfig {
	return model.WorkflowConfig{Enabled: false, Actions: []string{}, ApproverRoles: []string{model.RoleTeamLead, model.RoleAdmin, model.RoleSuperAdmin}}
}

func (s *Server) loadSetting(r *http.Request, key string, destination any) error {
	return s.loadSettingContext(r.Context(), key, destination)
}

func (s *Server) loadSettingContext(ctx context.Context, key string, destination any) error {
	record, err := s.repo.GetSetting(ctx, key)
	if err != nil {
		return err
	}
	payload := record.Payload
	if record.Sensitive {
		payload, err = s.secrets.Decrypt(payload, "setting:"+key)
		if err != nil {
			return err
		}
	}
	return json.Unmarshal(payload, destination)
}

func (s *Server) saveSetting(r *http.Request, key string, value any, sensitive bool, revision *int64) (model.SettingRecord, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return model.SettingRecord{}, err
	}
	if sensitive {
		payload, err = s.secrets.Encrypt(payload, "setting:"+key)
		if err != nil {
			return model.SettingRecord{}, err
		}
	}
	return s.repo.PutSetting(r.Context(), model.SettingRecord{Key: key, Payload: payload, Sensitive: sensitive, UpdatedBy: getPrincipal(r).User.ID}, revision)
}

func (s *Server) general(r *http.Request) (generalConfig, error) {
	cfg := defaultGeneral()
	if err := s.loadSetting(r, settingGeneral, &cfg); err != nil && !store.IsNotFound(err) {
		return generalConfig{}, err
	}
	normalizeGeneral(&cfg)
	return cfg, validateGeneral(cfg)
}

func (s *Server) oidcConfig(r *http.Request) (model.OIDCConfig, error) {
	cfg := defaultOIDC()
	if err := s.loadSetting(r, settingOIDC, &cfg); err != nil && !store.IsNotFound(err) {
		return model.OIDCConfig{}, err
	}
	normalizeOIDC(&cfg)
	return cfg, nil
}

func (s *Server) aiConfig(r *http.Request) (model.AIConfig, error) {
	cfg := defaultAI()
	if err := s.loadSetting(r, settingAI, &cfg); err != nil && !store.IsNotFound(err) {
		return model.AIConfig{}, err
	}
	normalizeAI(&cfg)
	return cfg, nil
}

func (s *Server) workflowConfig(r *http.Request) (model.WorkflowConfig, error) {
	return s.workflowConfigContext(r.Context())
}

func (s *Server) workflowConfigContext(ctx context.Context) (model.WorkflowConfig, error) {
	cfg := defaultWorkflow()
	if err := s.loadSettingContext(ctx, settingWorkflow, &cfg); err != nil && !store.IsNotFound(err) {
		return model.WorkflowConfig{}, err
	}
	if cfg.Actions == nil {
		cfg.Actions = []string{}
	}
	if cfg.ApproverRoles == nil {
		cfg.ApproverRoles = []string{}
	}
	return cfg, nil
}

func (s *Server) mediaSettings(r *http.Request) (mediaConfig, error) {
	cfg := defaultMedia()
	if err := s.loadSetting(r, settingMedia, &cfg); err != nil && !store.IsNotFound(err) {
		return mediaConfig{}, err
	}
	return cfg, validateMedia(cfg)
}

func (s *Server) apiSettings(r *http.Request) (apiAccessConfig, error) {
	cfg := defaultAPIAccess()
	if err := s.loadSetting(r, settingAPI, &cfg); err != nil && !store.IsNotFound(err) {
		return apiAccessConfig{}, err
	}
	return cfg, validateAPI(cfg)
}

func validateGeneral(cfg generalConfig) error {
	if strings.TrimSpace(cfg.ServiceName) == "" || len([]rune(cfg.ServiceName)) > 80 || cfg.SessionMinutes < 5 || cfg.SessionMinutes > 1440 {
		return errors.New("서비스 일반 설정이 올바르지 않습니다")
	}
	if cfg.PublicBaseURL != "" {
		parsed, err := url.Parse(cfg.PublicBaseURL)
		if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.RawPath != "" || parsed.Fragment != "" || parsed.ForceQuery || strings.ContainsAny(cfg.PublicBaseURL, "?#") || parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.Path != "" && parsed.Path != "/" {
			return errors.New("사이트 기본 주소는 path/query/fragment가 없는 HTTP(S) origin이어야 합니다")
		}
	}
	if _, err := time.LoadLocation(cfg.DefaultTimezone); err != nil {
		return errors.New("시간대가 올바르지 않습니다")
	}
	return nil
}

func normalizeGeneral(cfg *generalConfig) {
	cfg.ServiceName = strings.TrimSpace(cfg.ServiceName)
	cfg.PublicBaseURL = strings.TrimRight(strings.TrimSpace(cfg.PublicBaseURL), "/")
}

func validateAPI(cfg apiAccessConfig) error {
	if cfg.RateLimitPerMinute < 1 || cfg.RateLimitPerMinute > 10000 || cfg.MCPEnabled && !cfg.Enabled {
		return errors.New("API 접근 설정이 올바르지 않습니다")
	}
	return nil
}

func validateMedia(cfg mediaConfig) error {
	if cfg.MaxUploadBytes < 64*1024 || cfg.MaxUploadBytes > 50<<20 || cfg.MaxPerPost < 1 || cfg.MaxPerPost > 12 || cfg.OrphanTTLHours < 1 || cfg.OrphanTTLHours > 720 {
		return errors.New("미디어 설정이 올바르지 않습니다")
	}
	return nil
}

var settingKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{1,127}$`)

func (s *Server) adminListSettings(w http.ResponseWriter, r *http.Request) {
	if !hasPermission(getPrincipal(r).Permissions, "settings:manage") {
		writeError(w, http.StatusForbidden, "forbidden", "설정 관리 권한이 없습니다")
		return
	}
	records, err := s.repo.ListSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "설정을 불러올 수 없습니다")
		return
	}
	items := make([]map[string]any, 0, len(records))
	for _, record := range records {
		item := map[string]any{"key": record.Key, "sensitive": record.Sensitive, "configured": true, "revision": record.Revision, "updatedBy": record.UpdatedBy, "updatedAt": record.UpdatedAt}
		if !record.Sensitive {
			var value any
			if json.Unmarshal(record.Payload, &value) == nil {
				item["value"] = value
			}
		}
		items = append(items, item)
	}
	writeData(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) adminPutSetting(w http.ResponseWriter, r *http.Request) {
	if !hasPermission(getPrincipal(r).Permissions, "settings:manage") {
		writeError(w, http.StatusForbidden, "forbidden", "설정 관리 권한이 없습니다")
		return
	}
	key := chi.URLParam(r, "settingKey")
	if !settingKeyPattern.MatchString(key) {
		writeError(w, http.StatusBadRequest, "invalid_key", "설정 키 형식이 올바르지 않습니다")
		return
	}
	if slices.Contains([]string{settingOIDC, settingAI, settingWorkflow, settingSMTP}, key) {
		writeError(w, http.StatusConflict, "reserved_setting", "이 설정은 전용 관리 API를 사용해야 합니다")
		return
	}
	var input struct {
		Value     json.RawMessage `json:"value"`
		Sensitive bool            `json:"sensitive"`
		Revision  *int64          `json:"revision"`
	}
	if !decodeJSON(w, r, &input) || len(input.Value) == 0 || !json.Valid(input.Value) {
		return
	}
	var value any
	if err := json.Unmarshal(input.Value, &value); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_value", "설정 값은 유효한 JSON이어야 합니다")
		return
	}
	sensitive := input.Sensitive || sensitiveSettingKey(key)
	switch key {
	case settingGeneral:
		var cfg generalConfig
		if strictUnmarshal(input.Value, &cfg) != nil {
			writeError(w, http.StatusBadRequest, "invalid_value", "서비스 일반 설정이 올바르지 않습니다")
			return
		}
		normalizeGeneral(&cfg)
		if validateGeneral(cfg) != nil {
			writeError(w, http.StatusBadRequest, "invalid_value", "서비스 일반 설정이 올바르지 않습니다")
			return
		}
		value, sensitive = cfg, false
	case settingAPI:
		var cfg apiAccessConfig
		if strictUnmarshal(input.Value, &cfg) != nil || validateAPI(cfg) != nil {
			writeError(w, http.StatusBadRequest, "invalid_value", "API 접근 설정이 올바르지 않습니다")
			return
		}
		value, sensitive = cfg, false
	case settingMedia:
		cfg := defaultMedia()
		if strictUnmarshal(input.Value, &cfg) != nil || validateMedia(cfg) != nil {
			writeError(w, http.StatusBadRequest, "invalid_value", "미디어 설정이 올바르지 않습니다")
			return
		}
		value, sensitive = cfg, false
	case settingNetwork:
		cfg := defaultNetwork()
		if strictUnmarshal(input.Value, &cfg) != nil || validateNetwork(&cfg) != nil {
			writeError(w, http.StatusBadRequest, "invalid_value", "신뢰 Proxy 설정은 정확한 IP 또는 CIDR 목록이어야 합니다")
			return
		}
		value, sensitive = cfg, false
	}
	record, err := s.saveSetting(r, key, value, sensitive, input.Revision)
	if store.IsNotFound(err) {
		writeError(w, http.StatusConflict, "revision_conflict", "다른 관리자가 설정을 변경했습니다. 새로고침 후 다시 시도해 주세요")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "설정을 저장할 수 없습니다")
		return
	}
	if key == settingNetwork {
		s.invalidateNetworkSettings()
	}
	s.audit(r, "setting.update", "setting", key, true, map[string]any{"sensitive": sensitive, "revision": record.Revision})
	writeData(w, http.StatusOK, map[string]any{"key": key, "sensitive": sensitive, "configured": true, "revision": record.Revision, "updatedAt": record.UpdatedAt})
}

func strictUnmarshal(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(destination)
}

func sensitiveSettingKey(key string) bool {
	lower := strings.ToLower(key)
	return strings.Contains(lower, "secret") || strings.Contains(lower, "token") || strings.Contains(lower, "password") || strings.Contains(lower, "credential") || strings.Contains(lower, "api_key") || strings.Contains(lower, "apikey")
}

func (s *Server) adminGetOIDC(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.oidcConfig(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "OIDC 설정을 불러올 수 없습니다")
		return
	}
	effectiveRedirect, defaultRedirect, redirectSource, defaultRedirectSource, _ := s.oidcRedirectDetails(r, cfg)
	writeData(w, http.StatusOK, oidcView(cfg, effectiveRedirect, defaultRedirect, redirectSource, defaultRedirectSource))
}

func oidcView(cfg model.OIDCConfig, effectiveRedirect, defaultRedirect, redirectSource, defaultRedirectSource string) map[string]any {
	return map[string]any{"enabled": cfg.Enabled, "issuerUrl": cfg.IssuerURL, "clientId": cfg.ClientID, "redirectUrl": cfg.RedirectURL, "effectiveRedirectUrl": effectiveRedirect, "defaultRedirectUrl": defaultRedirect, "redirectUrlSource": redirectSource, "defaultRedirectUrlSource": defaultRedirectSource, "scopes": cfg.Scopes, "autoProvision": cfg.AutoProvision, "defaultRoles": cfg.DefaultRoles, "roleClaim": cfg.RoleClaim, "roleMappings": cfg.RoleMappings, "allowedHosts": cfg.AllowedHosts, "privateAllowedHosts": cfg.PrivateAllowedHosts, "allowInsecureHttp": cfg.AllowInsecureHTTP, "clientSecretConfigured": cfg.ClientSecret != ""}
}

func (s *Server) adminPutOIDC(w http.ResponseWriter, r *http.Request) {
	var cfg model.OIDCConfig
	if !decodeJSON(w, r, &cfg) {
		return
	}
	old, _ := s.oidcConfig(r)
	if cfg.ClearClientSecret && cfg.ClientSecret != "" {
		writeError(w, http.StatusBadRequest, "ambiguous_secret", "Client Secret 입력과 삭제를 동시에 요청할 수 없습니다")
		return
	}
	if cfg.ClearClientSecret {
		cfg.ClientSecret = ""
	} else if cfg.ClientSecret == "" || cfg.ClientSecret == "********" {
		cfg.ClientSecret = old.ClientSecret
	}
	cfg.ClearClientSecret = false
	normalizeOIDC(&cfg)
	if err := validateOIDC(cfg, true); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_config", err.Error())
		return
	}
	if !s.rolesExist(r, cfg.DefaultRoles) || !s.rolesExist(r, mappedRoles(cfg.RoleMappings)) {
		writeError(w, http.StatusBadRequest, "invalid_roles", "OIDC 역할 매핑에 존재하지 않는 역할이 있습니다")
		return
	}
	allAssigned := append(append([]string{}, cfg.DefaultRoles...), mappedRoles(cfg.RoleMappings)...)
	if !s.canAssignRoles(r, allAssigned) {
		writeError(w, http.StatusForbidden, "role_escalation_forbidden", "현재 관리자 권한을 넘는 OIDC 역할은 자동 부여할 수 없습니다")
		return
	}
	if _, err := s.saveSetting(r, settingOIDC, cfg, true, nil); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "OIDC 설정을 저장할 수 없습니다")
		return
	}
	s.audit(r, "oidc.config.update", "setting", settingOIDC, true, map[string]any{"issuerUrl": cfg.IssuerURL, "enabled": cfg.Enabled})
	effectiveRedirect, defaultRedirect, redirectSource, defaultRedirectSource, _ := s.oidcRedirectDetails(r, cfg)
	writeData(w, http.StatusOK, oidcView(cfg, effectiveRedirect, defaultRedirect, redirectSource, defaultRedirectSource))
}

func normalizeOIDC(cfg *model.OIDCConfig) {
	// The issuer is an exact identifier. Some providers intentionally advertise
	// a trailing slash and go-oidc compares that value byte-for-byte with the
	// discovery document, so only surrounding whitespace is safe to remove.
	cfg.IssuerURL = strings.TrimSpace(cfg.IssuerURL)
	cfg.AllowedHosts = outbound.EnsureEndpointHost(cfg.AllowedHosts, cfg.IssuerURL)
	if normalized, err := outbound.NormalizeHosts(cfg.AllowedHosts); err == nil {
		cfg.AllowedHosts = normalized
	}
	if normalized, err := outbound.NormalizePrivateHosts(cfg.PrivateAllowedHosts); err == nil {
		cfg.PrivateAllowedHosts = normalized
	}
	cfg.ClientID = strings.TrimSpace(cfg.ClientID)
	cfg.RedirectURL = strings.TrimSpace(cfg.RedirectURL)
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = []string{"openid", "profile", "email"}
	}
	if !slices.Contains(cfg.Scopes, "openid") {
		cfg.Scopes = append([]string{"openid"}, cfg.Scopes...)
	}
	if len(cfg.DefaultRoles) == 0 {
		cfg.DefaultRoles = []string{model.RoleMember}
	}
	if cfg.RoleClaim == "" {
		cfg.RoleClaim = "realm_access.roles"
	}
	if cfg.RoleMappings == nil {
		cfg.RoleMappings = map[string][]string{}
	}
}

func validateOIDC(cfg model.OIDCConfig, allowAutomaticRedirect bool) error {
	if !cfg.Enabled {
		return nil
	}
	return validateConfiguredOIDC(cfg, allowAutomaticRedirect)
}

func validateConfiguredOIDC(cfg model.OIDCConfig, allowAutomaticRedirect bool) error {
	if cfg.IssuerURL == "" || cfg.ClientID == "" || cfg.RedirectURL == "" && !allowAutomaticRedirect {
		return errors.New("활성화하려면 issuerUrl과 clientId가 필요합니다")
	}
	if err := validateServiceURL(cfg.IssuerURL, cfg.AllowInsecureHTTP); err != nil {
		return fmt.Errorf("issuerUrl: %w", err)
	}
	if err := validateOutboundEndpoint(cfg.IssuerURL, cfg.AllowedHosts, cfg.PrivateAllowedHosts, cfg.AllowInsecureHTTP); err != nil {
		return fmt.Errorf("issuerUrl: %w", err)
	}
	if cfg.RedirectURL != "" {
		if err := validateServiceURL(cfg.RedirectURL, cfg.AllowInsecureHTTP); err != nil {
			return fmt.Errorf("redirectUrl: %w", err)
		}
		parsed, _ := url.Parse(cfg.RedirectURL)
		if parsed.RawQuery != "" || parsed.Fragment != "" {
			return errors.New("redirectUrl에는 query 또는 fragment를 사용할 수 없습니다")
		}
	}
	if len(cfg.Scopes) > 20 || len(cfg.DefaultRoles) > 20 || len(cfg.RoleMappings) > 100 {
		return errors.New("OIDC scope 또는 역할 매핑이 너무 많습니다")
	}
	return nil
}

func validateServiceURL(raw string, allowHTTP bool) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("query/fragment/userinfo가 없는 URL이어야 합니다")
	}
	if parsed.Scheme == "https" {
		return nil
	}
	if parsed.Scheme == "http" && allowHTTP {
		return nil
	}
	return errors.New("HTTPS URL이어야 합니다. 폐쇄망 HTTP는 allowInsecureHttp를 명시적으로 켜야 합니다")
}

func mappedRoles(mappings map[string][]string) []string {
	set := map[string]bool{}
	for _, values := range mappings {
		for _, value := range values {
			set[value] = true
		}
	}
	roles := make([]string, 0, len(set))
	for role := range set {
		roles = append(roles, role)
	}
	return roles
}

func (s *Server) rolesExist(r *http.Request, roles []string) bool {
	if len(roles) == 0 {
		return true
	}
	var count int
	if err := s.repo.Pool().QueryRow(r.Context(), `SELECT count(*) FROM roles WHERE name=ANY($1)`, roles).Scan(&count); err != nil {
		return false
	}
	unique := map[string]bool{}
	for _, role := range roles {
		unique[role] = true
	}
	return count == len(unique)
}

func (s *Server) canAssignRoles(r *http.Request, roles []string) bool {
	p := getPrincipal(r)
	if hasRole(p.User, model.RoleSuperAdmin) {
		return true
	}
	for _, role := range roles {
		permissions, err := s.repo.PermissionsForRoles(r.Context(), []string{role})
		if err != nil {
			return false
		}
		for _, permission := range permissions {
			if !hasPermission(p.Permissions, permission) {
				return false
			}
		}
	}
	return true
}

func (s *Server) adminGetAI(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.aiConfig(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "AI 설정을 불러올 수 없습니다")
		return
	}
	writeData(w, http.StatusOK, aiView(cfg))
}

func aiView(cfg model.AIConfig) map[string]any {
	return map[string]any{"enabled": cfg.Enabled, "baseUrl": cfg.BaseURL, "model": cfg.Model, "apiStyle": cfg.APIStyle, "defaultMaxTokens": cfg.DefaultMaxTokens, "maxTokens": cfg.MaxTokens, "timeoutSeconds": cfg.TimeoutSeconds, "allowedHosts": cfg.AllowedHosts, "privateAllowedHosts": cfg.PrivateAllowedHosts, "allowInsecureHttp": cfg.AllowInsecureHTTP, "apiKeyConfigured": cfg.APIKey != ""}
}

func normalizeAI(cfg *model.AIConfig) {
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	cfg.AllowedHosts = outbound.EnsureEndpointHost(cfg.AllowedHosts, cfg.BaseURL)
	if normalized, err := outbound.NormalizeHosts(cfg.AllowedHosts); err == nil {
		cfg.AllowedHosts = normalized
	}
	if normalized, err := outbound.NormalizePrivateHosts(cfg.PrivateAllowedHosts); err == nil {
		cfg.PrivateAllowedHosts = normalized
	}
}

func validateOutboundEndpoint(raw string, hosts, privateHosts []string, allowHTTP bool) error {
	normalized, err := outbound.NormalizeHosts(hosts)
	if err != nil {
		return err
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return errors.New("URL 형식이 올바르지 않습니다")
	}
	privateNormalized, err := outbound.NormalizePrivateHosts(privateHosts)
	if err != nil {
		return err
	}
	return (outbound.Policy{AllowedHosts: normalized, PrivateAllowedHosts: privateNormalized, AllowHTTP: allowHTTP}).ValidateURL(parsed)
}

func (s *Server) mediaConfigStatus(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.mediaSettings(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "settings_unavailable", "미디어 설정을 확인할 수 없습니다")
		return
	}
	writeData(w, http.StatusOK, map[string]any{
		"maxUploadBytes": cfg.MaxUploadBytes,
		"maxPerPost":     cfg.MaxPerPost,
		"acceptedTypes":  []string{"image/jpeg", "image/png", "image/gif", "image/webp", "video/mp4", "video/webm"},
	})
}

func (s *Server) adminPutAI(w http.ResponseWriter, r *http.Request) {
	var cfg model.AIConfig
	if !decodeJSON(w, r, &cfg) {
		return
	}
	old, _ := s.aiConfig(r)
	if cfg.ClearAPIKey && cfg.APIKey != "" {
		writeError(w, http.StatusBadRequest, "ambiguous_secret", "API Key 입력과 삭제를 동시에 요청할 수 없습니다")
		return
	}
	if cfg.ClearAPIKey {
		cfg.APIKey = ""
	} else if cfg.APIKey == "" || cfg.APIKey == "********" {
		cfg.APIKey = old.APIKey
	}
	cfg.ClearAPIKey = false
	if err := validateAI(&cfg); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_config", err.Error())
		return
	}
	if _, err := s.saveSetting(r, settingAI, cfg, true, nil); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "AI 설정을 저장할 수 없습니다")
		return
	}
	s.audit(r, "ai.config.update", "setting", settingAI, true, map[string]any{"baseUrl": cfg.BaseURL, "model": cfg.Model, "apiStyle": cfg.APIStyle})
	writeData(w, http.StatusOK, aiView(cfg))
}

var approvalActionPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*(?:\.[a-z][a-z0-9_-]*)+$`)
var approvalNamespacePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*(?:\.[a-z][a-z0-9_-]*)*$`)

type parsedApprovalActionPattern struct {
	value     string
	namespace string
	global    bool
	wildcard  bool
}

// parseApprovalActionPattern accepts only the global wildcard, an exact
// dot-delimited action, or a wildcard in the terminal namespace position.
// Keeping parsing and matching together prevents a persisted malformed rule
// from becoming a broader prefix match.
func parseApprovalActionPattern(raw string) (parsedApprovalActionPattern, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "*" {
		return parsedApprovalActionPattern{value: value, global: true}, nil
	}
	if strings.Contains(value, "*") {
		if strings.Count(value, "*") != 1 || !strings.HasSuffix(value, ".*") {
			return parsedApprovalActionPattern{}, errors.New("wildcard must be the terminal namespace segment")
		}
		namespace := strings.TrimSuffix(value, ".*")
		if !approvalNamespacePattern.MatchString(namespace) {
			return parsedApprovalActionPattern{}, errors.New("invalid wildcard namespace")
		}
		return parsedApprovalActionPattern{value: value, namespace: namespace, wildcard: true}, nil
	}
	if !approvalActionPattern.MatchString(value) {
		return parsedApprovalActionPattern{}, errors.New("action must be dot-delimited")
	}
	return parsedApprovalActionPattern{value: value}, nil
}

func normalizeApprovalAction(raw string) (string, bool) {
	value := strings.ToLower(strings.TrimSpace(raw))
	return value, approvalActionPattern.MatchString(value)
}

func (pattern parsedApprovalActionPattern) matches(action string) bool {
	value, valid := normalizeApprovalAction(action)
	if !valid {
		return false
	}
	if pattern.global {
		return true
	}
	if pattern.wildcard {
		return strings.HasPrefix(value, pattern.namespace+".")
	}
	return pattern.value == value
}

func implementedApprovalPattern(pattern parsedApprovalActionPattern) bool {
	return pattern.matches("post.publish")
}

func (s *Server) adminGetWorkflow(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.workflowConfig(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "승인 설정을 불러올 수 없습니다")
		return
	}
	writeData(w, http.StatusOK, cfg)
}

func (s *Server) adminPutWorkflow(w http.ResponseWriter, r *http.Request) {
	var cfg model.WorkflowConfig
	if !decodeJSON(w, r, &cfg) {
		return
	}
	if cfg.Actions == nil {
		cfg.Actions = []string{}
	}
	if cfg.Enabled && len(cfg.Actions) == 0 {
		cfg.Actions = []string{"post.publish"}
	}
	if len(cfg.ApproverRoles) == 0 {
		cfg.ApproverRoles = []string{model.RoleTeamLead, model.RoleAdmin, model.RoleSuperAdmin}
	}
	if len(cfg.Actions) > 128 || len(cfg.ApproverRoles) > 32 {
		writeError(w, http.StatusBadRequest, "invalid_workflow", "승인 규칙 또는 승인 역할이 너무 많습니다")
		return
	}
	seen := map[string]bool{}
	for index, action := range cfg.Actions {
		parsed, err := parseApprovalActionPattern(action)
		if err != nil || seen[parsed.value] {
			writeError(w, http.StatusBadRequest, "invalid_actions", "승인 작업 패턴이 올바르지 않거나 중복되었습니다")
			return
		}
		if !implementedApprovalPattern(parsed) {
			writeError(w, http.StatusBadRequest, "unsupported_actions", "현재 승인 요청을 생성할 수 있는 작업은 post.publish뿐입니다")
			return
		}
		seen[parsed.value], cfg.Actions[index] = true, parsed.value
	}
	if !s.rolesExist(r, cfg.ApproverRoles) {
		writeError(w, http.StatusBadRequest, "invalid_roles", "승인 역할이 현재 역할 정책에 없습니다")
		return
	}
	for _, role := range cfg.ApproverRoles {
		permissions, err := s.repo.PermissionsForRoles(r.Context(), []string{role})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error", "승인 역할 권한을 확인할 수 없습니다")
			return
		}
		if !hasPermission(permissions, "approvals:review") {
			writeError(w, http.StatusBadRequest, "no_eligible_approver", "선택한 모든 승인 역할에 approvals:review 권한이 필요합니다")
			return
		}
	}
	if _, err := s.saveSetting(r, settingWorkflow, cfg, false, nil); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "승인 설정을 저장할 수 없습니다")
		return
	}
	s.audit(r, "workflow.config.update", "setting", settingWorkflow, true, cfg)
	writeData(w, http.StatusOK, cfg)
}

func workflowMatches(cfg model.WorkflowConfig, action string) bool {
	if !cfg.Enabled {
		return false
	}
	for _, raw := range cfg.Actions {
		pattern, err := parseApprovalActionPattern(raw)
		if err == nil && pattern.matches(action) {
			return true
		}
	}
	return false
}
