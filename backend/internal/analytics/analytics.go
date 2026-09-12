// Package analytics lets an administrator attach a visitor tracking snippet to
// the pages MOINA serves, without loosening the content security policy.
//
// The page policy allows scripts from the application origin only, so a pasted
// snippet would be refused silently. This package produces both halves of the
// answer: the markup to inject, with a per-request nonce on every script tag,
// and the extra policy sources that snippet needs. 'unsafe-inline' is never
// used — once added it would stay open after tracking is switched off again.
package analytics

import (
	"errors"
	"fmt"
	"html"
	"net/url"
	"strings"
)

const (
	ProviderNone    = "none"
	ProviderMomento = "momento"
	ProviderGA4     = "ga4"
	ProviderGTM     = "gtm"
	ProviderMatomo  = "matomo"
	ProviderCustom  = "custom"

	PlacementHead = "head"
	PlacementBody = "body"

	// MaxSnippetBytes bounds a pasted snippet. A loader is a few hundred bytes;
	// anything larger is a whole library and belongs on a server, not in a
	// setting that is rewritten into every page.
	MaxSnippetBytes = 8 * 1024
	// MaxAllowedHosts bounds the manual allow list, mirroring the outbound
	// host lists elsewhere in the server.
	MaxAllowedHosts = 64

	// MomentoProxyPath is the same-origin prefix the server forwards to the
	// Momento collector when the proxy is on. The snippet then needs no
	// external origin in the policy at all.
	MomentoProxyPath = "/momento"
)

var Providers = []string{ProviderNone, ProviderMomento, ProviderGA4, ProviderGTM, ProviderMatomo, ProviderCustom}

// Config is the stored tracking setting. Field names follow the camelCase
// convention of the other MOINA settings.
type Config struct {
	Enabled  bool   `json:"enabled"`
	Provider string `json:"provider"`
	// Momento is the self-hosted collector and the only provider whose data
	// never leaves the network, which is why it comes first.
	MomentoURL                 string `json:"momentoUrl"`
	MomentoSiteID              string `json:"momentoSiteId"`
	MomentoProxy               bool   `json:"momentoProxy"`
	MomentoAllowPrivateNetwork bool   `json:"momentoAllowPrivateNetwork"`
	MeasurementID              string `json:"measurementId"`
	MatomoURL                  string `json:"matomoUrl"`
	MatomoSiteID               string `json:"matomoSiteId"`
	CustomSnippet              string `json:"customSnippet"`
	// AllowedHosts holds http(s) origins the administrator adds by hand when
	// the snippet did not name them itself.
	AllowedHosts []string `json:"allowedHosts"`
	IncludeAdmin bool     `json:"includeAdmin"`
	Placement    string   `json:"placement"`
}

// Default is the configuration of a fresh installation: tracking is off and
// nothing about the served pages or their policy changes.
func Default() Config {
	return Config{Enabled: false, Provider: ProviderNone, MomentoProxy: true, AllowedHosts: []string{}, Placement: PlacementHead}
}

// Normalize trims the text fields and settles the enumerations on their
// defaults so Validate and the renderers see one canonical shape.
func Normalize(c *Config) {
	c.Provider = strings.ToLower(strings.TrimSpace(c.Provider))
	if c.Provider == "" {
		c.Provider = ProviderNone
	}
	c.MomentoURL = strings.TrimRight(strings.TrimSpace(c.MomentoURL), "/")
	c.MomentoSiteID = strings.TrimSpace(c.MomentoSiteID)
	c.MeasurementID = strings.TrimSpace(c.MeasurementID)
	c.MatomoURL = strings.TrimRight(strings.TrimSpace(c.MatomoURL), "/")
	c.MatomoSiteID = strings.TrimSpace(c.MatomoSiteID)
	c.CustomSnippet = strings.TrimSpace(c.CustomSnippet)
	c.Placement = strings.ToLower(strings.TrimSpace(c.Placement))
	if c.Placement != PlacementBody {
		c.Placement = PlacementHead
	}
	hosts := make([]string, 0, len(c.AllowedHosts))
	seen := make(map[string]struct{}, len(c.AllowedHosts))
	for _, host := range c.AllowedHosts {
		host = strings.TrimSuffix(strings.TrimSpace(host), "/")
		if host == "" {
			continue
		}
		if _, duplicate := seen[strings.ToLower(host)]; duplicate {
			continue
		}
		seen[strings.ToLower(host)] = struct{}{}
		hosts = append(hosts, host)
	}
	c.AllowedHosts = hosts
}

// Validate reports what is missing for the chosen provider. A disabled
// configuration is always acceptable so an administrator can switch tracking
// off without first fixing every field.
func (c Config) Validate() error {
	if c.Placement != PlacementHead && c.Placement != PlacementBody {
		return errors.New("삽입 위치는 head 또는 body 중 하나여야 합니다")
	}
	if len(c.AllowedHosts) > MaxAllowedHosts {
		return fmt.Errorf("허용 출처는 최대 %d개까지 등록할 수 있습니다", MaxAllowedHosts)
	}
	for _, host := range c.AllowedHosts {
		if !validOrigin(host) {
			return fmt.Errorf("허용 출처 %q는 https://host[:port] 형태의 출처여야 합니다", host)
		}
	}
	if len(c.CustomSnippet) > MaxSnippetBytes {
		return fmt.Errorf("추적 코드는 %d바이트를 넘을 수 없습니다", MaxSnippetBytes)
	}
	switch c.Provider {
	case ProviderNone, ProviderMomento, ProviderGA4, ProviderGTM, ProviderMatomo, ProviderCustom:
	default:
		return errors.New("provider는 none, momento, ga4, gtm, matomo, custom 중 하나여야 합니다")
	}
	if !c.Enabled {
		return nil
	}
	switch c.Provider {
	case ProviderNone:
		return errors.New("추적을 켜려면 provider를 선택해야 합니다")
	case ProviderMomento:
		if c.MomentoURL == "" || c.MomentoSiteID == "" {
			return errors.New("수집기 주소와 사이트 ID를 모두 입력해야 Momento를 쓸 수 있습니다")
		}
		if !validOrigin(c.MomentoURL) && !validBaseURL(c.MomentoURL) {
			return errors.New("수집기 주소는 query·fragment가 없는 HTTP(S) URL이어야 합니다")
		}
	case ProviderGA4, ProviderGTM:
		if c.MeasurementID == "" {
			return errors.New("GA4·GTM에는 measurementId가 필요합니다")
		}
		if strings.ContainsAny(c.MeasurementID, "<>\"'&/ ") {
			return errors.New("measurementId 형식이 올바르지 않습니다")
		}
	case ProviderMatomo:
		if c.MatomoURL == "" || c.MatomoSiteID == "" {
			return errors.New("주소와 사이트 ID를 모두 입력해야 Matomo를 쓸 수 있습니다")
		}
		if !validOrigin(c.MatomoURL) && !validBaseURL(c.MatomoURL) {
			return errors.New("주소는 query·fragment가 없는 HTTP(S) URL이어야 합니다")
		}
	case ProviderCustom:
		if c.CustomSnippet == "" {
			return errors.New("붙여 넣은 추적 코드가 비어 있습니다")
		}
	}
	return nil
}

// Active reports whether the page at path should carry the snippet.
// Administrative pages are excluded unless asked for, because console traffic
// is rarely the visitor data anybody wants to measure.
func (c Config) Active(path string) bool {
	if !c.Enabled || c.Provider == ProviderNone || c.Provider == "" {
		return false
	}
	if !c.IncludeAdmin && IsAdminPath(path) {
		return false
	}
	return strings.TrimSpace(c.Snippet("")) != ""
}

// IsAdminPath matches the administrator console routes of the web app.
func IsAdminPath(path string) bool {
	return path == "/admin" || strings.HasPrefix(path, "/admin/")
}

// Snippet renders the markup to inject with nonce on every script tag.
func (c Config) Snippet(nonce string) string {
	switch c.Provider {
	case ProviderMomento:
		if c.MomentoSiteID == "" || c.MomentoURL == "" {
			return ""
		}
		site := html.EscapeString(c.MomentoSiteID)
		if c.MomentoProxy {
			return withNonce(fmt.Sprintf(`<script async src="%s/tracker.js" data-site-id="%s" data-environment="prd" data-contract-version="1" data-endpoint="%s"></script>`, MomentoProxyPath, site, MomentoProxyPath), nonce)
		}
		return withNonce(fmt.Sprintf(`<script async src="%s/tracker.js" data-site-id="%s" data-environment="prd" data-contract-version="1"></script>`, html.EscapeString(c.MomentoURL), site), nonce)
	case ProviderGA4:
		if c.MeasurementID == "" {
			return ""
		}
		id := html.EscapeString(c.MeasurementID)
		return withNonce(fmt.Sprintf(`<script async src="https://www.googletagmanager.com/gtag/js?id=%s"></script>
<script>window.dataLayer=window.dataLayer||[];function gtag(){dataLayer.push(arguments);}gtag('js',new Date());gtag('config','%s');</script>`, id, id), nonce)
	case ProviderGTM:
		if c.MeasurementID == "" {
			return ""
		}
		return withNonce(fmt.Sprintf(`<script>(function(w,d,s,l,i){w[l]=w[l]||[];w[l].push({'gtm.start':new Date().getTime(),event:'gtm.js'});var f=d.getElementsByTagName(s)[0],j=d.createElement(s),dl=l!='dataLayer'?'&l='+l:'';j.async=true;j.src='https://www.googletagmanager.com/gtm.js?id='+i+dl;f.parentNode.insertBefore(j,f);})(window,document,'script','dataLayer','%s');</script>`, html.EscapeString(c.MeasurementID)), nonce)
	case ProviderMatomo:
		if c.MatomoURL == "" || c.MatomoSiteID == "" {
			return ""
		}
		return withNonce(fmt.Sprintf(`<script>var _paq=window._paq=window._paq||[];_paq.push(['trackPageView']);_paq.push(['enableLinkTracking']);(function(){var u="%s/";_paq.push(['setTrackerUrl',u+'matomo.php']);_paq.push(['setSiteId','%s']);var d=document,g=d.createElement('script'),s=d.getElementsByTagName('script')[0];g.async=true;g.src=u+'matomo.js';s.parentNode.insertBefore(g,s);})();</script>`, html.EscapeString(c.MatomoURL), html.EscapeString(c.MatomoSiteID)), nonce)
	case ProviderCustom:
		return withNonce(c.CustomSnippet, nonce)
	}
	return ""
}

// withNonce adds the nonce to every script tag that does not already carry
// one. This is what lets a pasted snippet run under the strict policy as is.
func withNonce(snippet, nonce string) string {
	if nonce == "" || snippet == "" {
		return snippet
	}
	var builder strings.Builder
	lower := strings.ToLower(snippet)
	for index := 0; ; {
		start := strings.Index(lower[index:], "<script")
		if start < 0 {
			builder.WriteString(snippet[index:])
			return builder.String()
		}
		start += index
		// "<scripts" or "<script-x" would be some other element.
		next := start + len("<script")
		if next < len(snippet) && snippet[next] != '>' && snippet[next] != ' ' && snippet[next] != '\t' && snippet[next] != '\n' && snippet[next] != '\r' && snippet[next] != '/' {
			builder.WriteString(snippet[index:next])
			index = next
			continue
		}
		builder.WriteString(snippet[index:next])
		closing := strings.IndexByte(snippet[next:], '>')
		attributes := lower[next:]
		if closing >= 0 {
			attributes = lower[next : next+closing]
		}
		if !strings.Contains(attributes, "nonce=") {
			builder.WriteString(` nonce="` + html.EscapeString(nonce) + `"`)
		}
		index = next
	}
}

// Sources is the set of extra origins a policy must allow, by directive.
type Sources struct {
	Scripts  []string
	Connects []string
	Images   []string
}

// PolicySources lists the origins the configured snippet needs. Built-in
// providers contribute their known addresses; a pasted snippet contributes
// whatever addresses it names; the manual allow list covers the rest.
func (c Config) PolicySources() Sources {
	var sources Sources
	add := func(origin string) {
		sources.Scripts = append(sources.Scripts, origin)
		sources.Connects = append(sources.Connects, origin)
		sources.Images = append(sources.Images, origin)
	}
	switch c.Provider {
	case ProviderMomento:
		// Through the same-origin proxy everything is 'self' already.
		if !c.MomentoProxy {
			if origin := originOf(c.MomentoURL); origin != "" {
				add(origin)
			}
		}
	case ProviderGA4, ProviderGTM:
		sources.Scripts = append(sources.Scripts, "https://www.googletagmanager.com")
		sources.Connects = append(sources.Connects, "https://www.google-analytics.com", "https://analytics.google.com", "https://*.google-analytics.com")
		sources.Images = append(sources.Images, "https://www.google-analytics.com", "https://www.googletagmanager.com")
	case ProviderMatomo:
		if origin := originOf(c.MatomoURL); origin != "" {
			add(origin)
		}
	case ProviderCustom:
		for _, origin := range SnippetOrigins(c.CustomSnippet) {
			add(origin)
		}
	}
	for _, host := range c.AllowedHosts {
		if origin := originOf(host); origin != "" {
			add(origin)
		}
	}
	return sources
}

// AllowedOrigins is the lower-cased set of every origin PolicySources grants,
// used to mark a recorded violation as already fixed.
func (c Config) AllowedOrigins() map[string]struct{} {
	sources := c.PolicySources()
	allowed := make(map[string]struct{})
	for _, group := range [][]string{sources.Scripts, sources.Connects, sources.Images} {
		for _, origin := range group {
			allowed[strings.ToLower(strings.TrimSuffix(origin, "/"))] = struct{}{}
		}
	}
	return allowed
}

// SnippetOrigins lists every http(s) origin written into a snippet: the
// script it loads, the endpoint it posts to, the pixel it requests. Trackers
// write their own address into the loader, so reading it here is what keeps a
// pasted snippet working without the administrator translating a policy
// error in the browser console into a host name.
func SnippetOrigins(snippet string) []string {
	origins := make([]string, 0, 2)
	seen := make(map[string]struct{}, 2)
	lower := strings.ToLower(snippet)
	for index := 0; index < len(snippet); {
		start := strings.Index(lower[index:], "http")
		if start < 0 {
			break
		}
		start += index
		end := start
		for end < len(snippet) && !urlBoundary(snippet[end]) {
			end++
		}
		index = end
		candidate := snippet[start:end]
		if !strings.HasPrefix(lower[start:end], "http://") && !strings.HasPrefix(lower[start:end], "https://") {
			continue
		}
		origin := originOf(candidate)
		if origin == "" {
			continue
		}
		if _, duplicate := seen[origin]; duplicate {
			continue
		}
		seen[origin] = struct{}{}
		origins = append(origins, origin)
	}
	return origins
}

// urlBoundary reports the characters that end an address written inside HTML
// or JavaScript source.
func urlBoundary(letter byte) bool {
	switch letter {
	case '"', '\'', '`', '<', '>', ' ', '\t', '\n', '\r', ')', '(', ',', ';', '\\', '+', '}', '{':
		return true
	}
	return false
}

// originOf reduces an address to scheme://host[:port]; anything that is not an
// http(s) address yields "".
func originOf(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return ""
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return ""
	}
	return scheme + "://" + strings.ToLower(parsed.Host)
}

// validOrigin accepts exactly scheme://host[:port] with nothing after it.
func validOrigin(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || originOf(raw) == "" {
		return false
	}
	return parsed.Path == "" && parsed.RawQuery == "" && parsed.Fragment == "" && !parsed.ForceQuery && parsed.Opaque == ""
}

// validBaseURL accepts an http(s) URL with an optional path and no query or
// fragment, which is how a collector mounted under a prefix is written.
func validBaseURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || originOf(raw) == "" {
		return false
	}
	return parsed.RawQuery == "" && parsed.Fragment == "" && !parsed.ForceQuery && parsed.Opaque == "" && !strings.Contains(parsed.Path, "..")
}

// OriginOf is the exported form used by the HTTP layer for reported URIs.
func OriginOf(raw string) string { return originOf(raw) }
