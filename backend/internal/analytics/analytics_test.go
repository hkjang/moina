package analytics

import (
	"strings"
	"testing"
	"time"
)

func momento() Config {
	cfg := Default()
	cfg.Enabled, cfg.Provider = true, ProviderMomento
	cfg.MomentoURL, cfg.MomentoSiteID = "https://momento.corp.example", "site-7"
	return cfg
}

func TestDefaultIsOffAndValid(t *testing.T) {
	cfg := Default()
	if cfg.Enabled || cfg.Active("/flow") || cfg.Snippet("n") != "" {
		t.Fatalf("fresh installation must not track: %+v", cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default must validate: %v", err)
	}
	if sources := cfg.PolicySources(); len(sources.Scripts)+len(sources.Connects)+len(sources.Images) != 0 {
		t.Fatalf("default must add no policy sources: %+v", sources)
	}
}

func TestValidateRequiresProviderFields(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
		wantOK bool
	}{
		{"disabled needs nothing", func(c *Config) { c.Enabled = false; c.MomentoURL = "" }, true},
		{"enabled none", func(c *Config) { c.Provider = ProviderNone }, false},
		{"unknown provider", func(c *Config) { c.Provider = "plausible" }, false},
		{"momento ok", func(*Config) {}, true},
		{"momento under a prefix", func(c *Config) { c.MomentoURL = "https://tools.corp.example/momento" }, true},
		{"momento with query", func(c *Config) { c.MomentoURL = "https://momento.corp.example/?x=1" }, false},
		{"momento without site", func(c *Config) { c.MomentoSiteID = "" }, false},
		{"ga4 needs id", func(c *Config) { c.Provider = ProviderGA4 }, false},
		{"ga4 ok", func(c *Config) { c.Provider, c.MeasurementID = ProviderGA4, "G-ABC123" }, true},
		{"ga4 id with markup", func(c *Config) { c.Provider, c.MeasurementID = ProviderGA4, "G-1\"><script>" }, false},
		{"matomo ok", func(c *Config) {
			c.Provider, c.MatomoURL, c.MatomoSiteID = ProviderMatomo, "https://matomo.corp.example", "3"
		}, true},
		{"custom empty", func(c *Config) { c.Provider = ProviderCustom }, false},
		{"custom ok", func(c *Config) {
			c.Provider, c.CustomSnippet = ProviderCustom, `<script src="https://t.example/t.js"></script>`
		}, true},
		{"custom too large", func(c *Config) {
			c.Provider, c.CustomSnippet = ProviderCustom, "<script>"+strings.Repeat("x", MaxSnippetBytes)+"</script>"
		}, false},
		{"allowed host must be an origin", func(c *Config) { c.AllowedHosts = []string{"https://a.example/path"} }, false},
		{"allowed host without scheme", func(c *Config) { c.AllowedHosts = []string{"a.example"} }, false},
		{"allowed host ok", func(c *Config) { c.AllowedHosts = []string{"https://a.example:8443"} }, true},
		{"placement", func(c *Config) { c.Placement = "footer" }, false},
	}
	for _, tc := range cases {
		cfg := momento()
		tc.mutate(&cfg)
		err := cfg.Validate()
		if (err == nil) != tc.wantOK {
			t.Errorf("%s: err=%v wantOK=%v", tc.name, err, tc.wantOK)
		}
	}
}

func TestNormalizeSettlesShape(t *testing.T) {
	cfg := Config{Provider: " Momento ", MomentoURL: " https://m.example/ ", Placement: "BODY", AllowedHosts: []string{" https://a.example/ ", "https://A.example", "", "https://b.example"}}
	Normalize(&cfg)
	if cfg.Provider != ProviderMomento || cfg.MomentoURL != "https://m.example" || cfg.Placement != PlacementBody {
		t.Fatalf("normalized=%+v", cfg)
	}
	if strings.Join(cfg.AllowedHosts, ",") != "https://a.example,https://b.example" {
		t.Fatalf("allowedHosts=%v", cfg.AllowedHosts)
	}
	empty := Config{}
	Normalize(&empty)
	if empty.Provider != ProviderNone || empty.Placement != PlacementHead || empty.AllowedHosts == nil {
		t.Fatalf("empty normalized=%+v", empty)
	}
}

func TestMomentoSnippetPrefersSameOriginProxy(t *testing.T) {
	cfg := momento()
	snippet := cfg.Snippet("abc")
	for _, want := range []string{`src="/momento/tracker.js"`, `data-endpoint="/momento"`, `data-site-id="site-7"`, `data-environment="prd"`, `data-contract-version="1"`, `nonce="abc"`} {
		if !strings.Contains(snippet, want) {
			t.Errorf("proxy snippet lacks %s: %s", want, snippet)
		}
	}
	if strings.Contains(snippet, "momento.corp.example") {
		t.Errorf("proxy snippet must not name the collector: %s", snippet)
	}
	if sources := cfg.PolicySources(); len(sources.Scripts)+len(sources.Connects)+len(sources.Images) != 0 {
		t.Errorf("proxy needs no external policy source: %+v", sources)
	}

	cfg.MomentoProxy = false
	direct := cfg.Snippet("abc")
	if !strings.Contains(direct, `src="https://momento.corp.example/tracker.js"`) || strings.Contains(direct, "data-endpoint") {
		t.Errorf("direct snippet=%s", direct)
	}
	sources := cfg.PolicySources()
	if len(sources.Scripts) != 1 || sources.Scripts[0] != "https://momento.corp.example" || len(sources.Connects) != 1 || len(sources.Images) != 1 {
		t.Errorf("direct sources=%+v", sources)
	}
}

func TestSnippetEscapesSiteID(t *testing.T) {
	cfg := momento()
	cfg.MomentoSiteID = `x" onload="alert(1)`
	if snippet := cfg.Snippet("n"); strings.Contains(snippet, `onload="`) || !strings.Contains(snippet, "&#34;") {
		t.Fatalf("site id not escaped: %s", snippet)
	}
}

func TestWithNonceTagsEveryScriptOnce(t *testing.T) {
	cases := map[string]string{
		`<script src="a.js"></script><SCRIPT>x()</SCRIPT>`:            `<script nonce="n1" src="a.js"></script><SCRIPT nonce="n1">x()</SCRIPT>`,
		`<script nonce="keep" src="a.js"></script><script>y</script>`: `<script nonce="keep" src="a.js"></script><script nonce="n1">y</script>`,
		`<scripts></scripts><noscript><img src="x"></noscript>`:       `<scripts></scripts><noscript><img src="x"></noscript>`,
		`<script>`: `<script nonce="n1">`,
		``:         ``,
	}
	for input, want := range cases {
		if got := withNonce(input, "n1"); got != want {
			t.Errorf("withNonce(%q)=%q want %q", input, got, want)
		}
	}
	if got := withNonce(`<script>a</script>`, ""); got != `<script>a</script>` {
		t.Errorf("empty nonce must leave the snippet alone: %q", got)
	}
	if got := withNonce(`<script>`, `"><script>`); strings.Contains(got, `nonce="">`) || !strings.Contains(got, "&#34;&gt;&lt;script&gt;") {
		t.Errorf("nonce must be escaped: %q", got)
	}
}

func TestSnippetOriginsReadsLoaderAddresses(t *testing.T) {
	snippet := `<script async src="https://Tracker.Example/js/t.js"></script>
<script>var e='https://tracker.example/collect';fetch("http://pixel.example:8080/p?x=1",{method:'POST'});var n=new Image();n.src="https://tracker.example/pixel.gif";var x="ftp://files.example/a";var y="httpstatus";</script>`
	got := SnippetOrigins(snippet)
	want := []string{"https://tracker.example", "http://pixel.example:8080"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("origins=%v want %v", got, want)
	}
	if got := SnippetOrigins("no addresses here"); len(got) != 0 {
		t.Fatalf("origins=%v", got)
	}
}

func TestPolicySourcesCombineProviderSnippetAndAllowList(t *testing.T) {
	cfg := Default()
	cfg.Enabled, cfg.Provider = true, ProviderCustom
	cfg.CustomSnippet = `<script src="https://t.example/t.js"></script>`
	cfg.AllowedHosts = []string{"https://extra.example/"}
	sources := cfg.PolicySources()
	if strings.Join(sources.Scripts, " ") != "https://t.example https://extra.example" {
		t.Fatalf("scripts=%v", sources.Scripts)
	}
	cfg.Provider, cfg.MeasurementID = ProviderGA4, "G-1"
	sources = cfg.PolicySources()
	if sources.Scripts[0] != "https://www.googletagmanager.com" || len(sources.Connects) != 4 {
		t.Fatalf("ga4 sources=%+v", sources)
	}
}

func TestActiveSkipsAdminUnlessAsked(t *testing.T) {
	cfg := momento()
	if !cfg.Active("/flow") || !cfg.Active("/") || !cfg.Active("/login") || !cfg.Active("/administrators") {
		t.Fatal("visitor pages must be active")
	}
	if cfg.Active("/admin") || cfg.Active("/admin/settings") {
		t.Fatal("admin pages must be excluded by default")
	}
	cfg.IncludeAdmin = true
	if !cfg.Active("/admin/settings") {
		t.Fatal("includeAdmin must include admin pages")
	}
	cfg.Enabled = false
	if cfg.Active("/flow") {
		t.Fatal("disabled must be inactive")
	}
}

func TestRecorderKeepsDistinctOriginsOnly(t *testing.T) {
	recorder := NewRecorder()
	moment := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	recorder.now = func() time.Time { moment = moment.Add(time.Second); return moment }
	for i := 0; i < 5; i++ {
		recorder.Record("https://momento.corp.example/collect/v1/events", "connect-src 'self'", "https://moina.example/flow")
	}
	recorder.Record("https://momento.corp.example/tracker.js", "script-src", "https://moina.example/flow")
	recorder.Record("inline", "script-src", "https://moina.example/flow")
	recorder.Record("chrome-extension://abc/x.js", "script-src", "")
	recorder.Record("data:image/png;base64,AAAA", "img-src", "")
	recorder.Record("https://cdn.example/p.gif", "", "https://moina.example/explore")

	items := recorder.List(Default())
	if len(items) != 3 {
		t.Fatalf("items=%+v", items)
	}
	if items[0].Origin != "https://cdn.example" || items[0].Directive != "connect-src" {
		t.Fatalf("newest first with default directive: %+v", items[0])
	}
	var connect Violation
	for _, item := range items {
		if item.Directive == "connect-src" && item.Origin == "https://momento.corp.example" {
			connect = item
		}
	}
	if connect.Count != 5 || connect.Allowed {
		t.Fatalf("connect=%+v", connect)
	}

	cfg := momento()
	cfg.MomentoProxy = false
	for _, item := range recorder.List(cfg) {
		if want := item.Origin == "https://momento.corp.example"; item.Allowed != want {
			t.Errorf("%s allowed=%v want %v", item.Origin, item.Allowed, want)
		}
	}
	ga := Default()
	ga.Enabled, ga.Provider, ga.MeasurementID = true, ProviderGA4, "G-1"
	recorder.Record("https://region1.google-analytics.com/g/collect", "connect-src", "")
	for _, item := range recorder.List(ga) {
		if item.Origin == "https://region1.google-analytics.com" && !item.Allowed {
			t.Errorf("wildcard policy entry must mark %s allowed", item.Origin)
		}
	}

	recorder.Forget()
	if len(recorder.List(Default())) != 0 {
		t.Fatal("Forget must clear")
	}
}

func TestRecorderEvictsOldestBeyondLimit(t *testing.T) {
	recorder := NewRecorder()
	moment := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	recorder.now = func() time.Time { moment = moment.Add(time.Second); return moment }
	for i := 0; i < MaxViolations+10; i++ {
		recorder.Record("https://host-"+strings.Repeat("a", i%7)+"-"+string(rune('a'+i%26))+"-"+itoa(i)+".example/x", "img-src", "")
	}
	items := recorder.List(Default())
	if len(items) != MaxViolations {
		t.Fatalf("len=%d", len(items))
	}
	for _, item := range items {
		if strings.HasSuffix(item.Origin, "-0.example") {
			t.Fatal("oldest entry must be evicted")
		}
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	digits := ""
	for ; i > 0; i /= 10 {
		digits = string(rune('0'+i%10)) + digits
	}
	return digits
}

func TestAddAllowedHostDeduplicates(t *testing.T) {
	hosts := AddAllowedHost([]string{"https://a.example"}, "https://b.example/")
	if strings.Join(hosts, ",") != "https://a.example,https://b.example" {
		t.Fatalf("hosts=%v", hosts)
	}
	if again := AddAllowedHost(hosts, "HTTPS://A.example"); len(again) != 2 {
		t.Fatalf("duplicate added: %v", again)
	}
	if same := AddAllowedHost(hosts, "  "); len(same) != 2 {
		t.Fatalf("blank added: %v", same)
	}
}
