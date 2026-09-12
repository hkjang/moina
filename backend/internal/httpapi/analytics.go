package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hkjang/moina/backend/internal/analytics"
	"github.com/hkjang/moina/backend/internal/outbound"
	"github.com/hkjang/moina/backend/internal/secure"
	"github.com/hkjang/moina/backend/internal/store"
)

const (
	settingAnalytics = "analytics.tracking"

	// cspReportPath is where browsers post the requests the content security
	// policy refused. It is unauthenticated because the browser sends the
	// report without credentials, and it keeps nothing but a bounded list of
	// origins in memory.
	cspReportPath = "/api/v1/analytics/csp-report"
	// maxReportBytes keeps the unauthenticated endpoint from being used to push
	// large bodies at the server.
	maxReportBytes = 8 * 1024
	// maxMomentoProxyBytes bounds one forwarded tracker event. A page view is a
	// few hundred bytes of JSON.
	maxMomentoProxyBytes = 64 * 1024
	momentoProxyTimeout  = 10 * time.Second

	// basePagePolicy is the policy every page has always carried. Tracking only
	// ever appends to it and only while it is switched on.
	basePagePolicy = "default-src 'self'; img-src 'self' data: blob:; media-src 'self' blob:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self' ws: wss:; frame-ancestors 'none'; base-uri 'self'; form-action 'self'"
	// apiPolicy is for responses that are never a page: JSON, metrics, the
	// MCP stream, proxied tracker traffic. img-src and media-src stay so an
	// attachment opened directly in a tab still renders.
	apiPolicy = "default-src 'none'; img-src 'self'; media-src 'self'; frame-ancestors 'none'"
)

// nonceKey carries the per-request script nonce from the security middleware
// to the SPA handler so the policy header and the injected snippet agree.
type nonceKey struct{}

func requestNonce(r *http.Request) string {
	value, _ := r.Context().Value(nonceKey{}).(string)
	return value
}

// pagePath reports whether a request can be answered with the web app shell.
// Everything else gets the narrow API policy and never a tracking snippet.
func pagePath(path string) bool {
	if path == "/mcp" || path == "/healthz" || path == "/readyz" || path == "/metrics" {
		return false
	}
	if strings.HasPrefix(path, "/api/") || path == analytics.MomentoProxyPath || strings.HasPrefix(path, analytics.MomentoProxyPath+"/") {
		return false
	}
	return true
}

// analyticsConfigContext reads the tracking setting. Any failure — no
// repository, a storage error, a malformed payload — is treated as "tracking
// off" so a settings outage never changes what a page is allowed to run.
func (s *Server) analyticsConfigContext(ctx context.Context) analytics.Config {
	cfg := analytics.Default()
	payload, err := s.analyticsPayload(ctx)
	if err != nil {
		if !store.IsNotFound(err) {
			slog.WarnContext(ctx, "방문 추적 설정을 읽을 수 없어 추적을 끈 채로 응답합니다", "error", err)
		}
		return cfg
	}
	if json.Unmarshal(payload, &cfg) != nil {
		return analytics.Default()
	}
	analytics.Normalize(&cfg)
	if cfg.Validate() != nil {
		return analytics.Default()
	}
	return cfg
}

// analyticsPayload reads the stored setting. Without a repository — tests and
// embedded callers — only a primed cache can supply a configuration.
func (s *Server) analyticsPayload(ctx context.Context) (json.RawMessage, error) {
	if s.repo == nil {
		if s.settings != nil {
			if entry, ok := s.settings.get(settingAnalytics, time.Now()); ok && !entry.missing {
				return entry.payload, nil
			}
		}
		return nil, store.ErrNotFound
	}
	return s.cachedSettingPayload(ctx, settingAnalytics)
}

// pagePolicy keeps the strict page policy and adds only what the configured
// snippet needs: the nonce for its script tags, the origins it loads from and
// posts to, and a report-uri so whatever is still refused becomes visible.
func pagePolicy(cfg analytics.Config, path, nonce string) string {
	if !cfg.Active(path) || nonce == "" {
		return basePagePolicy
	}
	sources := cfg.PolicySources()
	scripts := append([]string{"'self'", "'nonce-" + nonce + "'"}, sources.Scripts...)
	connects := append([]string{"'self'", "ws:", "wss:"}, sources.Connects...)
	images := append([]string{"'self'", "data:", "blob:"}, sources.Images...)
	return "default-src 'self'; img-src " + strings.Join(images, " ") +
		"; media-src 'self' blob:; style-src 'self' 'unsafe-inline'; script-src " + strings.Join(scripts, " ") +
		"; connect-src " + strings.Join(connects, " ") +
		"; frame-ancestors 'none'; base-uri 'self'; form-action 'self'; report-uri " + cspReportPath
}

// contentSecurityPolicy decides the header for one request and returns the
// request carrying the nonce the SPA handler must use.
func (s *Server) contentSecurityPolicy(r *http.Request) (string, *http.Request) {
	if !pagePath(r.URL.Path) {
		return apiPolicy, r
	}
	cfg := s.analyticsConfigContext(r.Context())
	if !cfg.Active(r.URL.Path) {
		return basePagePolicy, r
	}
	nonce, err := secure.RandomToken(16)
	if err != nil {
		return basePagePolicy, r
	}
	r = r.WithContext(context.WithValue(r.Context(), nonceKey{}, nonce))
	return pagePolicy(cfg, r.URL.Path, nonce), r
}

// injectSnippet places the markup just before the closing tag of the chosen
// placement, or at the end of the document when that tag is missing.
func injectSnippet(page []byte, snippet, placement string) []byte {
	marker := "</head>"
	if placement == analytics.PlacementBody {
		marker = "</body>"
	}
	text := string(page)
	index := strings.LastIndex(strings.ToLower(text), marker)
	if index < 0 {
		return []byte(text + "\n" + snippet + "\n")
	}
	return []byte(text[:index] + snippet + "\n" + text[index:])
}

type cspReport struct {
	Report struct {
		BlockedURI         string `json:"blocked-uri"`
		ViolatedDirective  string `json:"violated-directive"`
		EffectiveDirective string `json:"effective-directive"`
		DocumentURI        string `json:"document-uri"`
	} `json:"csp-report"`
}

// receiveCSPReport records what a browser refused to load. Reports are always
// answered with 204: a misbehaving page must never see an error from us, and
// an attacker must learn nothing from the response.
func (s *Server) receiveCSPReport(w http.ResponseWriter, r *http.Request) {
	defer w.WriteHeader(http.StatusNoContent)
	if s.violations == nil {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxReportBytes))
	if err != nil || len(body) == 0 {
		return
	}
	var report cspReport
	if json.Unmarshal(body, &report) != nil {
		return
	}
	directive := report.Report.EffectiveDirective
	if directive == "" {
		directive = report.Report.ViolatedDirective
	}
	s.violations.Record(report.Report.BlockedURI, directive, report.Report.DocumentURI)
}

func analyticsView(cfg analytics.Config) map[string]any {
	return map[string]any{
		"enabled": cfg.Enabled, "provider": cfg.Provider,
		"momentoUrl": cfg.MomentoURL, "momentoSiteId": cfg.MomentoSiteID, "momentoProxy": cfg.MomentoProxy, "momentoAllowPrivateNetwork": cfg.MomentoAllowPrivateNetwork,
		"measurementId": cfg.MeasurementID, "matomoUrl": cfg.MatomoURL, "matomoSiteId": cfg.MatomoSiteID,
		"customSnippet": cfg.CustomSnippet, "allowedHosts": cfg.AllowedHosts, "includeAdmin": cfg.IncludeAdmin, "placement": cfg.Placement,
		"providers": analytics.Providers, "maxSnippetBytes": analytics.MaxSnippetBytes,
		// snippetPreview shows the administrator what a page will carry, with
		// the nonce left as a placeholder because it differs per request.
		"snippetPreview": cfg.Snippet("{nonce}"),
	}
}

func (s *Server) adminGetAnalytics(w http.ResponseWriter, r *http.Request) {
	cfg := analytics.Default()
	if err := s.loadSetting(r, settingAnalytics, &cfg); err != nil && !store.IsNotFound(err) {
		writeError(w, http.StatusInternalServerError, "storage_error", "방문 추적 설정을 불러올 수 없습니다")
		return
	}
	analytics.Normalize(&cfg)
	writeData(w, http.StatusOK, analyticsView(cfg))
}

func (s *Server) adminPutAnalytics(w http.ResponseWriter, r *http.Request) {
	cfg := analytics.Default()
	if !decodeJSON(w, r, &cfg) {
		return
	}
	if err := s.saveAnalytics(r, &cfg); err != nil {
		var invalid *invalidAnalyticsError
		if errors.As(err, &invalid) {
			writeError(w, http.StatusBadRequest, "invalid_config", invalid.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "storage_error", "방문 추적 설정을 저장할 수 없습니다")
		return
	}
	s.audit(r, "analytics.config.update", "setting", settingAnalytics, true, map[string]any{"enabled": cfg.Enabled, "provider": cfg.Provider, "momentoProxy": cfg.MomentoProxy, "includeAdmin": cfg.IncludeAdmin, "placement": cfg.Placement, "allowedHosts": cfg.AllowedHosts})
	writeData(w, http.StatusOK, analyticsView(cfg))
}

type invalidAnalyticsError struct{ cause error }

func (e *invalidAnalyticsError) Error() string { return e.cause.Error() }

// saveAnalytics normalizes, validates and stores the configuration. Momento's
// same-origin proxy is an outbound connection, so its address goes through the
// same exact-host policy as the other administrator configured endpoints.
func (s *Server) saveAnalytics(r *http.Request, cfg *analytics.Config) error {
	analytics.Normalize(cfg)
	if err := cfg.Validate(); err != nil {
		return &invalidAnalyticsError{cause: err}
	}
	if cfg.Enabled && cfg.Provider == analytics.ProviderMomento && cfg.MomentoProxy {
		if _, err := momentoPolicy(*cfg); err != nil {
			return &invalidAnalyticsError{cause: err}
		}
	}
	_, err := s.saveSetting(r, settingAnalytics, cfg, false, nil)
	return err
}

// adminListAnalyticsViolations shows which addresses the policy is blocking so
// a snippet can be fixed without reading the browser console.
func (s *Server) adminListAnalyticsViolations(w http.ResponseWriter, r *http.Request) {
	items := []analytics.Violation{}
	if s.violations != nil {
		items = s.violations.List(s.analyticsConfigContext(r.Context()))
	}
	writeData(w, http.StatusOK, map[string]any{"items": items})
}

// adminClearAnalyticsViolations forgets the recorded reports, which is how an
// administrator checks whether a change actually fixed the snippet.
func (s *Server) adminClearAnalyticsViolations(w http.ResponseWriter, r *http.Request) {
	if s.violations != nil {
		s.violations.Forget()
	}
	s.audit(r, "analytics.violations.clear", "setting", settingAnalytics, true, nil)
	w.WriteHeader(http.StatusNoContent)
}

// adminAllowAnalyticsHost adds one blocked origin to the allow list: the
// one-click fix for a listed violation.
func (s *Server) adminAllowAnalyticsHost(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Origin string `json:"origin"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	origin := analytics.OriginOf(input.Origin)
	if origin == "" || origin != strings.ToLower(strings.TrimSuffix(strings.TrimSpace(input.Origin), "/")) {
		writeError(w, http.StatusBadRequest, "invalid_origin", "허용할 출처는 https://host[:port] 형태여야 합니다")
		return
	}
	cfg := analytics.Default()
	if err := s.loadSetting(r, settingAnalytics, &cfg); err != nil && !store.IsNotFound(err) {
		writeError(w, http.StatusInternalServerError, "storage_error", "방문 추적 설정을 불러올 수 없습니다")
		return
	}
	cfg.AllowedHosts = analytics.AddAllowedHost(cfg.AllowedHosts, origin)
	if err := s.saveAnalytics(r, &cfg); err != nil {
		var invalid *invalidAnalyticsError
		if errors.As(err, &invalid) {
			writeError(w, http.StatusBadRequest, "invalid_config", invalid.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "storage_error", "방문 추적 설정을 저장할 수 없습니다")
		return
	}
	s.audit(r, "analytics.allowed_host.add", "setting", settingAnalytics, true, map[string]any{"origin": origin})
	writeData(w, http.StatusOK, analyticsView(cfg))
}

// momentoPolicy builds the outbound policy for the proxy: exactly the
// configured collector authority, private addresses only when the
// administrator said so, plain HTTP only when the address itself says so.
func momentoPolicy(cfg analytics.Config) (outbound.Policy, error) {
	parsed, err := url.Parse(cfg.MomentoURL)
	if err != nil || parsed.Hostname() == "" {
		return outbound.Policy{}, errors.New("수집기 주소가 올바르지 않아 프록시를 열 수 없습니다")
	}
	authority := outbound.EndpointAuthority(cfg.MomentoURL)
	hosts, err := outbound.NormalizeHosts([]string{authority})
	if err != nil {
		return outbound.Policy{}, errors.New("수집기 주소가 올바르지 않아 프록시를 열 수 없습니다")
	}
	policy := outbound.Policy{AllowedHosts: hosts, AllowHTTP: parsed.Scheme == "http"}
	if cfg.MomentoAllowPrivateNetwork {
		private, err := outbound.NormalizePrivateHosts([]string{authority})
		if err != nil {
			return outbound.Policy{}, errors.New("사설망 Momento 수집기는 IP가 아닌 정확한 DNS 이름을 사용해야 합니다")
		}
		policy.PrivateAllowedHosts = private
	}
	return policy, nil
}

// momentoClient reuses one policy-bound client per collector address so the
// proxy keeps its connections instead of opening a pool per tracker event.
func (s *Server) momentoClient(cfg analytics.Config) (*http.Client, error) {
	key := momentoClientKey(cfg)
	s.momentoMu.Lock()
	defer s.momentoMu.Unlock()
	if s.momentoProxyClient != nil && s.momentoProxyKey == key {
		return s.momentoProxyClient, nil
	}
	policy, err := momentoPolicy(cfg)
	if err != nil {
		return nil, err
	}
	client, err := policy.Client(s.client)
	if err != nil {
		return nil, err
	}
	client.Timeout = momentoProxyTimeout
	s.momentoProxyClient, s.momentoProxyKey = client, key
	return client, nil
}

func momentoClientKey(cfg analytics.Config) string {
	if cfg.MomentoAllowPrivateNetwork {
		return cfg.MomentoURL + "\x00private"
	}
	return cfg.MomentoURL + "\x00public"
}

// momentoProxy forwards /momento/* to the configured collector so the tracker
// and its events stay same-origin for the browser. Cookies and authorization
// never cross: the collector sees a visitor, not a MOINA session.
func (s *Server) momentoProxy(w http.ResponseWriter, r *http.Request) {
	cfg := s.analyticsConfigContext(r.Context())
	if !cfg.Enabled || cfg.Provider != analytics.ProviderMomento || !cfg.MomentoProxy {
		writeError(w, http.StatusNotFound, "not_found", "요청한 경로를 찾을 수 없습니다")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "허용되지 않은 메서드입니다")
		return
	}
	client, err := s.momentoClient(cfg)
	if err != nil {
		writeError(w, http.StatusBadGateway, "momento_egress_denied", "Momento 수집기 주소가 아웃바운드 정책에 의해 차단되었습니다")
		return
	}
	suffix := strings.TrimPrefix(r.URL.Path, analytics.MomentoProxyPath)
	if strings.Contains(suffix, "..") {
		writeError(w, http.StatusNotFound, "not_found", "요청한 경로를 찾을 수 없습니다")
		return
	}
	target := cfg.MomentoURL + suffix
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	ctx, cancel := context.WithTimeout(r.Context(), momentoProxyTimeout)
	defer cancel()
	var body io.Reader
	if r.Method == http.MethodPost {
		body = http.MaxBytesReader(w, r.Body, maxMomentoProxyBytes)
	}
	upstream, err := http.NewRequestWithContext(ctx, r.Method, target, body)
	if err != nil {
		writeError(w, http.StatusBadGateway, "momento_unavailable", "Momento 수집기에 연결할 수 없습니다")
		return
	}
	for _, name := range []string{"Content-Type", "Accept", "Accept-Language", "User-Agent", "Referer"} {
		if value := r.Header.Get(name); value != "" {
			upstream.Header.Set(name, value)
		}
	}
	upstream.Header.Set("X-Forwarded-For", clientIP(r))
	upstream.Header.Set("X-Forwarded-Host", r.Host)
	if isHTTPS(r) {
		upstream.Header.Set("X-Forwarded-Proto", "https")
	} else {
		upstream.Header.Set("X-Forwarded-Proto", "http")
	}
	response, err := client.Do(upstream)
	if err != nil {
		var maxBytes *http.MaxBytesError
		if errors.As(err, &maxBytes) {
			writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "추적 이벤트가 너무 큽니다")
			return
		}
		writeError(w, http.StatusBadGateway, "momento_unavailable", "Momento 수집기에 연결할 수 없습니다")
		return
	}
	defer response.Body.Close()
	for _, name := range []string{"Content-Type", "Cache-Control", "ETag", "Last-Modified", "Expires", "Vary"} {
		if value := response.Header.Get(name); value != "" {
			w.Header().Set(name, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	if r.Method != http.MethodHead {
		_, _ = io.Copy(w, response.Body)
	}
}
