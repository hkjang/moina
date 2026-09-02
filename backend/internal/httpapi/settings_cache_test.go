package httpapi

import (
	"testing"
	"time"
)

func TestSettingCacheExpiresAfterTTL(t *testing.T) {
	cache := newSettingCache()
	start := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	cache.put("media.config", settingEntry{payload: []byte(`{"maxPerPost":4}`), loadedAt: start})

	if _, ok := cache.get("media.config", start.Add(settingCacheTTL-time.Millisecond)); !ok {
		t.Fatal("TTL 이내인데 캐시가 비었습니다")
	}
	if _, ok := cache.get("media.config", start.Add(settingCacheTTL)); ok {
		t.Fatal("TTL이 지난 값이 그대로 반환되었습니다")
	}
}

func TestSettingCacheRemembersAMissingRow(t *testing.T) {
	// A service that never customised a setting would otherwise re-query for a
	// row that does not exist on every single request.
	cache := newSettingCache()
	now := time.Now()
	cache.put("service.retention", settingEntry{missing: true, loadedAt: now})
	entry, ok := cache.get("service.retention", now)
	if !ok || !entry.missing {
		t.Fatalf("missing 표시가 캐시되지 않았습니다: ok=%t entry=%+v", ok, entry)
	}
}

func TestSettingCacheInvalidation(t *testing.T) {
	cache := newSettingCache()
	now := time.Now()
	cache.put("a", settingEntry{payload: []byte(`1`), loadedAt: now})
	cache.put("b", settingEntry{payload: []byte(`2`), loadedAt: now})

	cache.invalidate("a")
	if _, ok := cache.get("a", now); ok {
		t.Fatal("무효화한 키가 남았습니다")
	}
	if _, ok := cache.get("b", now); !ok {
		t.Fatal("다른 키까지 무효화되었습니다")
	}

	cache.invalidateAll()
	if _, ok := cache.get("b", now); ok {
		t.Fatal("전체 무효화 후에도 값이 남았습니다")
	}
}

func TestRolePermissionsSignalCannotCollideWithASettingKey(t *testing.T) {
	// The permission cache shares the setting-change channel. That is only safe
	// while its sentinel is impossible to produce as a real setting key.
	if settingKeyPattern.MatchString(rolePermissionsSignal) {
		t.Fatalf("%q가 유효한 설정 키와 충돌합니다", rolePermissionsSignal)
	}
}

func TestPermissionCacheKeysOnTheWholeRoleSet(t *testing.T) {
	cache := newPermissionCache()
	now := time.Now()
	cache.put([]string{"member"}, []string{"posts:read"}, now)
	cache.put([]string{"member", "admin"}, []string{"posts:read", "admin:access"}, now)

	single, ok := cache.get([]string{"member"}, now)
	if !ok || len(single) != 1 {
		t.Fatalf("member 권한=%v ok=%t", single, ok)
	}
	both, ok := cache.get([]string{"member", "admin"}, now)
	if !ok || len(both) != 2 {
		t.Fatalf("member+admin 권한=%v ok=%t", both, ok)
	}
	if _, ok := cache.get([]string{"admin", "member"}, now); ok {
		t.Fatal("역할 순서가 다른 집합이 같은 캐시 항목을 재사용했습니다")
	}
}

func TestPermissionCacheDoesNotShareItsSlice(t *testing.T) {
	// authenticate intersects API key scopes against these permissions and
	// sorts the result, so a shared backing array would corrupt the cache.
	cache := newPermissionCache()
	now := time.Now()
	original := []string{"posts:read", "posts:write"}
	cache.put([]string{"member"}, original, now)
	original[0] = "mutated"

	cached, ok := cache.get([]string{"member"}, now)
	if !ok || cached[0] != "posts:read" {
		t.Fatalf("호출자의 slice 변경이 캐시에 반영되었습니다: %v", cached)
	}
	cached[1] = "also mutated"
	again, _ := cache.get([]string{"member"}, now)
	if again[1] != "posts:write" {
		t.Fatalf("반환한 slice 변경이 캐시에 반영되었습니다: %v", again)
	}
}

func TestPermissionCacheExpiresAfterTTL(t *testing.T) {
	cache := newPermissionCache()
	start := time.Now()
	cache.put([]string{"member"}, []string{"posts:read"}, start)
	if _, ok := cache.get([]string{"member"}, start.Add(permissionCacheTTL)); ok {
		t.Fatal("TTL이 지난 역할 권한이 그대로 반환되었습니다")
	}
}

func TestAPIKeyTouchWritesAtMostOncePerInterval(t *testing.T) {
	touch := newAPIKeyTouch()
	start := time.Now()
	if !touch.due("key_1", start) {
		t.Fatal("첫 사용은 기록되어야 합니다")
	}
	if touch.due("key_1", start.Add(apiKeyTouchInterval-time.Millisecond)) {
		t.Fatal("간격 이내에 다시 기록했습니다")
	}
	if !touch.due("key_1", start.Add(apiKeyTouchInterval)) {
		t.Fatal("간격이 지난 뒤 기록하지 않았습니다")
	}
	if !touch.due("key_2", start.Add(time.Second)) {
		t.Fatal("다른 키가 첫 사용부터 차단되었습니다")
	}
}
