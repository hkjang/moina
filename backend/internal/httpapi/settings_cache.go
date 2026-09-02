package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/hkjang/moina/backend/internal/store"
)

// settingCacheTTL bounds how long a stale copy can survive when the change
// notification is missed, for example while the listener is reconnecting.
// Normal propagation is immediate through pg_notify.
const settingCacheTTL = 30 * time.Second

// settingEntry caches one decrypted setting payload. missing records that the
// row does not exist, so a default-only configuration stops re-querying too.
type settingEntry struct {
	payload  json.RawMessage
	missing  bool
	loadedAt time.Time
}

type settingCache struct {
	mu      sync.RWMutex
	entries map[string]settingEntry
}

func newSettingCache() *settingCache {
	return &settingCache{entries: make(map[string]settingEntry)}
}

func (c *settingCache) get(key string, now time.Time) (settingEntry, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.entries[key]
	if !ok || now.Sub(entry.loadedAt) >= settingCacheTTL {
		return settingEntry{}, false
	}
	return entry, true
}

func (c *settingCache) put(key string, entry settingEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = entry
}

func (c *settingCache) invalidate(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, key)
}

// invalidateAll runs after a listener reconnect, when this instance cannot know
// which keys changed while it was disconnected.
func (c *settingCache) invalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	clear(c.entries)
}

// cachedSettingPayload returns the decrypted payload for key. Settings are read
// on hot paths - every API key request checks whether API access is enabled,
// every Moin write checks the media limits - while they change only when an
// administrator saves them, so the read is served from memory.
func (s *Server) cachedSettingPayload(ctx context.Context, key string) (json.RawMessage, error) {
	now := time.Now()
	if entry, ok := s.settings.get(key, now); ok {
		if entry.missing {
			return nil, store.ErrNotFound
		}
		return entry.payload, nil
	}
	record, err := s.repo.GetSetting(ctx, key)
	if store.IsNotFound(err) {
		s.settings.put(key, settingEntry{missing: true, loadedAt: now})
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	payload := record.Payload
	if record.Sensitive {
		payload, err = s.secrets.Decrypt(payload, "setting:"+key)
		if err != nil {
			return nil, err
		}
	}
	s.settings.put(key, settingEntry{payload: payload, loadedAt: now})
	return payload, nil
}

// runSettingCacheWorker keeps this instance's cached settings in step with the
// administrator changes other instances make.
func (s *Server) runSettingCacheWorker(ctx context.Context) error {
	keys := make(chan string, 64)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case key := <-keys:
				if key == rolePermissionsSignal {
					s.permissions.invalidateAll()
					continue
				}
				s.settings.invalidate(key)
			}
		}
	}()
	for ctx.Err() == nil {
		// A reconnect means notifications were missed, so start from empty
		// rather than trusting entries that may already be stale.
		s.settings.invalidateAll()
		s.permissions.invalidateAll()
		err := s.repo.ListenSettingChanges(ctx, keys)
		if ctx.Err() != nil {
			return nil
		}
		slog.WarnContext(ctx, "설정 변경 LISTEN 연결 복구 대기", "error", err)
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
	return nil
}
