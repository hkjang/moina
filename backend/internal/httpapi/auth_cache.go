package httpapi

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

const (
	// permissionCacheTTL bounds a stale role policy when the change
	// notification is missed. A role edit normally propagates immediately.
	permissionCacheTTL = 30 * time.Second
	// apiKeyTouchInterval is how precisely "last used" is recorded. Every API
	// request used to write this column, turning a read-only call into a write
	// and a WAL record; per-minute resolution is what the administrator screen
	// actually shows.
	apiKeyTouchInterval = time.Minute
	// rolePermissionsSignal travels on the setting-change channel. Setting keys
	// must match settingKeyPattern, which requires a leading lowercase letter,
	// so this uppercase payload can never collide with a real setting key.
	rolePermissionsSignal = "ROLE_PERMISSIONS"
)

// permissionCache maps a user's role set to the permissions it grants. Roles
// change when an administrator edits the policy; the answer is otherwise the
// same on every request from every user holding those roles.
type permissionCache struct {
	mu      sync.RWMutex
	entries map[string]permissionEntry
}

type permissionEntry struct {
	permissions []string
	loadedAt    time.Time
}

func newPermissionCache() *permissionCache {
	return &permissionCache{entries: make(map[string]permissionEntry)}
}

// roleCacheKey uses NUL as the separator because a role name cannot contain it,
// so two different role sets can never produce the same key.
func roleCacheKey(roles []string) string {
	return strings.Join(roles, "\x00")
}

// get returns a private copy. Callers own what they receive - authenticate
// intersects API key scopes and sorts the result - so handing out the stored
// slice would let one request corrupt every later one.
func (c *permissionCache) get(roles []string, now time.Time) ([]string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.entries[roleCacheKey(roles)]
	if !ok || now.Sub(entry.loadedAt) >= permissionCacheTTL {
		return nil, false
	}
	return copyPermissions(entry.permissions), true
}

func (c *permissionCache) put(roles []string, permissions []string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[roleCacheKey(roles)] = permissionEntry{permissions: copyPermissions(permissions), loadedAt: now}
}

func (c *permissionCache) invalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	clear(c.entries)
}

// permissionsForRoles answers from memory when it can. The returned slice is
// always private to the caller.
func (s *Server) permissionsForRoles(ctx context.Context, roles []string) ([]string, error) {
	now := time.Now()
	if cached, ok := s.permissions.get(roles, now); ok {
		return cached, nil
	}
	permissions, err := s.repo.PermissionsForRoles(ctx, roles)
	if err != nil {
		return nil, err
	}
	s.permissions.put(roles, permissions, now)
	return permissions, nil
}

func copyPermissions(permissions []string) []string {
	return append(make([]string, 0, len(permissions)), permissions...)
}

// invalidateRolePermissions drops this instance's copy and tells the others.
func (s *Server) invalidateRolePermissions(ctx context.Context) {
	s.permissions.invalidateAll()
	if err := s.repo.NotifySettingChange(ctx, rolePermissionsSignal); err != nil {
		slog.WarnContext(ctx, "역할 권한 변경 알림 실패", "error", err)
	}
}

// touchAPIKey records key usage at most once a minute per instance. A failure
// is logged and dropped: the request itself is already authenticated, and a
// missing "last used" timestamp must not turn into a failed API call.
func (s *Server) touchAPIKey(ctx context.Context, keyID string) {
	if keyID == "" || !s.apiKeyTouches.due(keyID, time.Now()) {
		return
	}
	if err := s.repo.TouchAPIKey(ctx, keyID); err != nil {
		slog.WarnContext(ctx, "API key 최근 사용 기록 실패", "error", err)
	}
}

// apiKeyTouch remembers when this instance last recorded a key's usage so a
// steady stream of API requests writes at most one row per key per minute.
type apiKeyTouch struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

func newAPIKeyTouch() *apiKeyTouch {
	return &apiKeyTouch{seen: make(map[string]time.Time)}
}

// due reports whether the key should be written now, recording the attempt so
// concurrent requests for the same key do not all write.
func (t *apiKeyTouch) due(keyID string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if last, ok := t.seen[keyID]; ok && now.Sub(last) < apiKeyTouchInterval {
		return false
	}
	// Keys are bounded by how many exist, but an instance that runs for months
	// should not hold entries for keys that were revoked long ago.
	if len(t.seen) > 10_000 {
		for candidate, last := range t.seen {
			if now.Sub(last) >= apiKeyTouchInterval {
				delete(t.seen, candidate)
			}
		}
	}
	t.seen[keyID] = now
	return true
}
