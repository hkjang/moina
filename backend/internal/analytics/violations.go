package analytics

import (
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// MaxViolations bounds the recorder. A blocked request repeats on every page
// view, so what matters is which origins are blocked, not how often — a small
// ring of distinct origins is enough to fix a snippet.
const MaxViolations = 100

// Violation is one origin the content security policy refused, kept with the
// directive that refused it so the console can say what to allow.
type Violation struct {
	Origin    string    `json:"origin"`
	Directive string    `json:"directive"`
	Page      string    `json:"page"`
	Count     int       `json:"count"`
	FirstSeen time.Time `json:"firstSeen"`
	LastSeen  time.Time `json:"lastSeen"`
	// Allowed marks an origin the current configuration already grants, so a
	// report left over from before the fix stops looking like a problem.
	Allowed bool `json:"allowed"`
}

// Recorder collects the violations browsers report. It is deliberately in
// memory: the reports are a live troubleshooting aid for the person pasting a
// snippet, not an audit record, and keeping them out of PostgreSQL means the
// unauthenticated report endpoint can never grow storage.
type Recorder struct {
	mu         sync.Mutex
	violations map[string]*Violation
	now        func() time.Time
}

func NewRecorder() *Recorder {
	return &Recorder{violations: make(map[string]*Violation), now: func() time.Time { return time.Now().UTC() }}
}

// Record notes one blocked request. Anything that is not an http(s) origin —
// a browser extension, a data: URL, an inline script reported as "inline" — is
// ignored because allowing it is neither possible nor useful.
func (r *Recorder) Record(blockedURI, directive, page string) {
	origin := originOf(blockedURI)
	if origin == "" {
		return
	}
	directive = strings.TrimSpace(strings.ToLower(directive))
	if index := strings.IndexByte(directive, ' '); index > 0 {
		directive = directive[:index]
	}
	if directive == "" {
		directive = "connect-src"
	}
	if len(page) > 512 {
		page = page[:512]
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key := directive + " " + origin
	moment := r.now()
	if existing, found := r.violations[key]; found {
		existing.Count++
		existing.LastSeen = moment
		existing.Page = page
		return
	}
	if len(r.violations) >= MaxViolations {
		r.evictOldest()
	}
	r.violations[key] = &Violation{Origin: origin, Directive: directive, Page: page, Count: 1, FirstSeen: moment, LastSeen: moment}
}

func (r *Recorder) evictOldest() {
	var oldestKey string
	var oldest time.Time
	for key, violation := range r.violations {
		if oldestKey == "" || violation.LastSeen.Before(oldest) {
			oldestKey, oldest = key, violation.LastSeen
		}
	}
	delete(r.violations, oldestKey)
}

// List returns the recorded violations, most recent first, marking the ones
// the given configuration already allows.
func (r *Recorder) List(config Config) []Violation {
	allowed := config.AllowedOrigins()
	r.mu.Lock()
	defer r.mu.Unlock()
	items := make([]Violation, 0, len(r.violations))
	for _, violation := range r.violations {
		copied := *violation
		_, known := allowed[strings.ToLower(copied.Origin)]
		copied.Allowed = known || matchesWildcard(copied.Origin, allowed)
		items = append(items, copied)
	}
	sort.Slice(items, func(first, second int) bool {
		if items[first].LastSeen.Equal(items[second].LastSeen) {
			return items[first].Origin < items[second].Origin
		}
		return items[first].LastSeen.After(items[second].LastSeen)
	})
	return items
}

// Forget drops every recorded violation, which is what an administrator does
// after changing a snippet to see whether anything is still blocked.
func (r *Recorder) Forget() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.violations = make(map[string]*Violation)
}

// matchesWildcard covers policy entries such as https://*.google-analytics.com.
func matchesWildcard(origin string, allowed map[string]struct{}) bool {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return false
	}
	host := strings.ToLower(parsed.Host)
	for pattern := range allowed {
		star := strings.Index(pattern, "*.")
		if star < 0 {
			continue
		}
		if strings.HasPrefix(strings.ToLower(origin), pattern[:star]) && strings.HasSuffix(host, pattern[star+1:]) {
			return true
		}
	}
	return false
}

// AddAllowedHost appends an origin to the allow list unless it is already
// there, leaving the existing entries and their order alone.
func AddAllowedHost(existing []string, origin string) []string {
	origin = strings.TrimSuffix(strings.TrimSpace(origin), "/")
	if origin == "" {
		return existing
	}
	for _, host := range existing {
		if strings.EqualFold(strings.TrimSuffix(strings.TrimSpace(host), "/"), origin) {
			return existing
		}
	}
	return append(append([]string{}, existing...), origin)
}
