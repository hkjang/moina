package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hkjang/moina/backend/internal/analytics"
)

// analyticsServer builds a handler without a repository whose tracking setting
// comes from the primed settings cache, which is how the page middleware and
// the SPA handler read it in production.
func analyticsServer(t *testing.T, cfg *analytics.Config) (*Server, http.Handler) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<!doctype html><html><head><title>MOINA</title></head><body><div id=\"root\"></div></body></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	server := New(nil, nil, "v0.0.0-test")
	server.staticRoot = root
	if cfg != nil {
		payload, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		server.settings.put(settingAnalytics, settingEntry{payload: payload, loadedAt: time.Now()})
	}
	return server, server.Handler()
}

func momentoConfig() *analytics.Config {
	cfg := analytics.Default()
	cfg.Enabled, cfg.Provider = true, analytics.ProviderMomento
	cfg.MomentoURL, cfg.MomentoSiteID = "https://momento.corp.example", "site-7"
	return &cfg
}

func get(handler http.Handler, path string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://moina.internal"+path, nil))
	return recorder
}

var nonceAttribute = regexp.MustCompile(`nonce="([^"]+)"`)

func TestTrackingOffLeavesPagesAndPolicyUntouched(t *testing.T) {
	for name, cfg := range map[string]*analytics.Config{"no setting": nil, "disabled setting": func() *analytics.Config { c := momentoConfig(); c.Enabled = false; return c }()} {
		_, handler := analyticsServer(t, cfg)
		page := get(handler, "/flow")
		if page.Code != http.StatusOK || strings.Contains(page.Body.String(), "<script") {
			t.Fatalf("%s: status=%d body=%q", name, page.Code, page.Body.String())
		}
		if got := page.Header().Get("Content-Security-Policy"); got != basePagePolicy || strings.Contains(got, "nonce") || strings.Contains(got, "report-uri") {
			t.Fatalf("%s: policy=%q", name, got)
		}
		if got := page.Header().Get("Last-Modified"); got == "" {
			t.Fatalf("%s: the untouched shell keeps its Last-Modified revalidation", name)
		}
	}
}

func TestTrackingOnInjectsNoncedSnippetMatchingPolicy(t *testing.T) {
	_, handler := analyticsServer(t, momentoConfig())
	page := get(handler, "/flow")
	body := page.Body.String()
	if page.Code != http.StatusOK || !strings.Contains(body, `<script nonce="`) {
		t.Fatalf("status=%d body=%q", page.Code, body)
	}
	match := nonceAttribute.FindStringSubmatch(body)
	if match == nil {
		t.Fatalf("no nonce in %q", body)
	}
	policy := page.Header().Get("Content-Security-Policy")
	if !strings.Contains(policy, "script-src 'self' 'nonce-"+match[1]+"'") {
		t.Fatalf("policy %q lacks nonce %q", policy, match[1])
	}
	scriptSrc := strings.SplitN(strings.SplitN(policy, "script-src", 2)[1], ";", 2)[0]
	if strings.Contains(scriptSrc, "'unsafe-inline'") || strings.Contains(scriptSrc, "'unsafe-eval'") {
		t.Fatalf("script-src must never use unsafe-inline: %q", policy)
	}
	if !strings.Contains(policy, "report-uri "+cspReportPath) {
		t.Fatalf("policy must ask for reports while tracking is on: %q", policy)
	}
	if strings.Contains(policy, "momento.corp.example") || !strings.Contains(body, `src="/momento/tracker.js"`) {
		t.Fatalf("proxy mode must keep the collector out of the policy: policy=%q body=%q", policy, body)
	}
	head := strings.Index(body, "</head>")
	if snippet := strings.Index(body, "<script"); snippet < 0 || snippet > head {
		t.Fatalf("head placement must put the snippet before </head>: %q", body)
	}
	if page.Header().Get("Last-Modified") != "" {
		t.Fatal("a per-request shell must not offer Last-Modified revalidation")
	}

	second := get(handler, "/flow")
	if again := nonceAttribute.FindStringSubmatch(second.Body.String()); again == nil || again[1] == match[1] {
		t.Fatalf("nonce must differ per request: %v vs %v", match, again)
	}
}

func TestTrackingBodyPlacementAndDirectOrigins(t *testing.T) {
	cfg := momentoConfig()
	cfg.MomentoProxy, cfg.Placement = false, analytics.PlacementBody
	cfg.AllowedHosts = []string{"https://extra.example"}
	_, handler := analyticsServer(t, cfg)
	page := get(handler, "/")
	body := page.Body.String()
	if snippet, tail := strings.Index(body, "<script"), strings.Index(body, "</body>"); snippet < strings.Index(body, "</head>") || snippet > tail {
		t.Fatalf("body placement wrong: %q", body)
	}
	policy := page.Header().Get("Content-Security-Policy")
	for _, directive := range []string{"script-src", "connect-src", "img-src"} {
		section := policy[strings.Index(policy, directive):]
		section = section[:strings.Index(section, ";")]
		if !strings.Contains(section, "https://momento.corp.example") || !strings.Contains(section, "https://extra.example") {
			t.Fatalf("%s lacks the collector or the allow list: %q", directive, section)
		}
	}
}

func TestTrackingSkipsNonPagePathsAndAdmin(t *testing.T) {
	_, handler := analyticsServer(t, momentoConfig())
	for _, path := range []string{"/healthz", "/api/v1/version", "/metrics"} {
		response := get(handler, path)
		if got := response.Header().Get("Content-Security-Policy"); got != apiPolicy {
			t.Errorf("%s policy=%q", path, got)
		}
		if strings.Contains(response.Body.String(), "<script") {
			t.Errorf("%s carries a snippet", path)
		}
	}
	admin := get(handler, "/admin/settings")
	if strings.Contains(admin.Body.String(), "<script") || admin.Header().Get("Content-Security-Policy") != basePagePolicy {
		t.Fatalf("admin pages must be excluded by default: %q", admin.Header().Get("Content-Security-Policy"))
	}

	cfg := momentoConfig()
	cfg.IncludeAdmin = true
	_, handler = analyticsServer(t, cfg)
	if admin = get(handler, "/admin/settings"); !strings.Contains(admin.Body.String(), "<script") {
		t.Fatal("includeAdmin must attach the snippet to admin pages")
	}
}

func TestCSPReportIsRecordedWithoutCredentialsOrOriginCheck(t *testing.T) {
	server, handler := analyticsServer(t, momentoConfig())
	body := `{"csp-report":{"blocked-uri":"https://momento.corp.example/collect/v1/events","effective-directive":"connect-src","document-uri":"https://moina.example/flow"}}`
	request := httptest.NewRequest(http.MethodPost, "http://moina.internal"+cspReportPath, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/csp-report")
	request.Header.Set("Origin", "null")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	items := server.violations.List(analytics.Default())
	if len(items) != 1 || items[0].Origin != "https://momento.corp.example" || items[0].Directive != "connect-src" || items[0].Page != "https://moina.example/flow" {
		t.Fatalf("items=%+v", items)
	}

	for _, broken := range []string{"", "not json", `{"csp-report":{"blocked-uri":"inline"}}`, strings.Repeat("x", maxReportBytes*2)} {
		recorder = httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "http://moina.internal"+cspReportPath, strings.NewReader(broken)))
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("broken report %q status=%d", broken[:min(len(broken), 16)], recorder.Code)
		}
	}
	if len(server.violations.List(analytics.Default())) != 1 {
		t.Fatal("broken reports must not be recorded")
	}
}

func TestInjectSnippetFallsBackToDocumentEnd(t *testing.T) {
	got := string(injectSnippet([]byte("<html><body>x</body></html>"), "<s>", analytics.PlacementHead))
	if got != "<html><body>x</body></html>\n<s>\n" {
		t.Fatalf("got %q", got)
	}
	got = string(injectSnippet([]byte("<HTML><HEAD></HEAD><BODY></BODY></HTML>"), "<s>", analytics.PlacementBody))
	if got != "<HTML><HEAD></HEAD><BODY><s>\n</BODY></HTML>" {
		t.Fatalf("got %q", got)
	}
}

func TestMomentoProxyIsClosedUntilConfigured(t *testing.T) {
	_, handler := analyticsServer(t, nil)
	if response := get(handler, "/momento/tracker.js"); response.Code != http.StatusNotFound {
		t.Fatalf("status=%d", response.Code)
	}
	direct := momentoConfig()
	direct.MomentoProxy = false
	_, handler = analyticsServer(t, direct)
	if response := get(handler, "/momento/tracker.js"); response.Code != http.StatusNotFound {
		t.Fatalf("direct mode status=%d", response.Code)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, "http://moina.internal/momento/x", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("delete status=%d", recorder.Code)
	}
}

func TestMomentoProxyForwardsWithoutCookies(t *testing.T) {
	var seen *http.Request
	var seenBody string
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Clone(r.Context())
		buffer := make([]byte, 1024)
		n, _ := r.Body.Read(buffer)
		seenBody = string(buffer[:n])
		w.Header().Set("Set-Cookie", "collector=1")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer collector.Close()

	cfg := momentoConfig()
	cfg.MomentoURL = collector.URL + "/base"
	server, handler := analyticsServer(t, cfg)
	// The exact-host outbound policy refuses loopback collectors, which is the
	// right answer in production; the forwarding itself is exercised with a
	// pre-built client under the key the proxy would compute.
	server.momentoProxyClient, server.momentoProxyKey = collector.Client(), momentoClientKey(*cfg)
	request := httptest.NewRequest(http.MethodPost, "http://moina.internal/momento/collect/v1/events?site=7", strings.NewReader(`{"event":"pageview"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Cookie", SessionCookie+"=secret")
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Referer", "http://moina.internal/flow")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted || recorder.Body.String() != `{"ok":true}` {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if seen == nil || seen.URL.Path != "/base/collect/v1/events" || seen.URL.RawQuery != "site=7" || seenBody != `{"event":"pageview"}` {
		t.Fatalf("upstream request=%+v body=%q", seen, seenBody)
	}
	if seen.Header.Get("Cookie") != "" || seen.Header.Get("Authorization") != "" {
		t.Fatalf("credentials must not be forwarded: %v", seen.Header)
	}
	if seen.Header.Get("Referer") != "http://moina.internal/flow" || seen.Header.Get("X-Forwarded-For") == "" {
		t.Fatalf("visitor context must be forwarded: %v", seen.Header)
	}
	if recorder.Header().Get("Set-Cookie") != "" {
		t.Fatal("collector cookies must not reach the browser")
	}
	if recorder.Header().Get("Content-Security-Policy") != apiPolicy {
		t.Fatalf("proxy responses are not pages: %q", recorder.Header().Get("Content-Security-Policy"))
	}
}

func TestMomentoProxyRefusesLoopbackCollectorByPolicy(t *testing.T) {
	cfg := momentoConfig()
	cfg.MomentoURL = "http://127.0.0.1:9/base"
	_, handler := analyticsServer(t, cfg)
	response := get(handler, "/momento/tracker.js")
	if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "momento_") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAnalyticsRoutesRequireSession(t *testing.T) {
	_, handler := analyticsServer(t, nil)
	for _, path := range []string{"/api/v1/admin/analytics", "/api/v1/admin/analytics/violations"} {
		if response := get(handler, path); response.Code != http.StatusUnauthorized {
			t.Errorf("%s must require a session: status=%d", path, response.Code)
		}
	}
}
