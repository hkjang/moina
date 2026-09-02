package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hkjang/moina/backend/internal/model"
	"github.com/hkjang/moina/backend/internal/store"
)

// Two Server values over one database stand in for two instances behind a load
// balancer: the cache is only safe if a change on one reaches the other without
// waiting out the TTL.
func TestPostgreSQLSettingChangeReachesAnotherInstance(t *testing.T) {
	dsn := os.Getenv("MOINA_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("MOINA_TEST_POSTGRES_DSN is not set")
	}
	repository, err := store.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(repository.Close)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = repository.Pool().Exec(ctx, `DELETE FROM settings WHERE key=$1`, settingMedia)
	})

	writer, reader := New(repository, nil, "test"), New(repository, nil, "test")
	listening, stopListening := context.WithCancel(t.Context())
	defer stopListening()
	go func() { _ = reader.runSettingCacheWorker(listening) }()

	store := func(maxPerPost int) {
		t.Helper()
		payload, marshalErr := json.Marshal(mediaConfig{MaxUploadBytes: 10 << 20, MaxPerPost: maxPerPost, OrphanTTLHours: 24})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if _, putErr := repository.PutSetting(t.Context(), model.SettingRecord{Key: settingMedia, Payload: payload}, nil); putErr != nil {
			t.Fatal(putErr)
		}
	}
	read := func(server *Server) int {
		t.Helper()
		cfg := defaultMedia()
		if loadErr := server.loadSettingContext(t.Context(), settingMedia, &cfg); loadErr != nil {
			t.Fatal(loadErr)
		}
		return cfg.MaxPerPost
	}

	store(4)
	if got := read(reader); got != 4 {
		t.Fatalf("maxPerPost=%d, 4를 기대했습니다", got)
	}
	if got := read(writer); got != 4 {
		t.Fatalf("maxPerPost=%d, 4를 기대했습니다", got)
	}

	// The reader now holds a cached copy. Change the setting through the writer
	// the way adminPutSetting does, notification included.
	store(7)
	if got := read(reader); got != 4 {
		t.Fatalf("알림 전 reader가 이미 %d를 봤습니다. 캐시가 동작하지 않습니다", got)
	}
	if notifyErr := repository.NotifySettingChange(t.Context(), settingMedia); notifyErr != nil {
		t.Fatal(notifyErr)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		if got := read(reader); got == 7 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("설정 변경이 다른 인스턴스에 전파되지 않았습니다")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestPostgreSQLAPIKeyUsageIsRecordedButThrottled(t *testing.T) {
	dsn := os.Getenv("MOINA_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("MOINA_TEST_POSTGRES_DSN is not set")
	}
	repository, err := store.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(repository.Close)

	suffix := time.Now().UnixNano()
	userID := fmt.Sprintf("usr_touch_%d", suffix)
	keyID := fmt.Sprintf("key_touch_%d", suffix)
	if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO users(id,username,display_name,roles) VALUES($1,$1,$1,ARRAY['member']::text[])`, userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = repository.Pool().Exec(ctx, `DELETE FROM api_keys WHERE id=$1`, keyID)
		_, _ = repository.Pool().Exec(ctx, `DELETE FROM users WHERE id=$1`, userID)
	})
	if _, err := repository.CreateAPIKey(t.Context(), model.APIKey{
		ID: keyID, UserID: userID, Name: "touch", Prefix: "mk_touch", TokenHash: fmt.Sprintf("hash_%d", suffix), Permissions: []string{"posts:read"},
	}, userID); err != nil {
		t.Fatal(err)
	}

	lastUsed := func() *time.Time {
		t.Helper()
		var value *time.Time
		if scanErr := repository.Pool().QueryRow(t.Context(), `SELECT last_used_at FROM api_keys WHERE id=$1`, keyID).Scan(&value); scanErr != nil {
			t.Fatal(scanErr)
		}
		return value
	}
	if lastUsed() != nil {
		t.Fatal("새 키에 사용 기록이 있습니다")
	}

	server := New(repository, nil, "test")
	server.touchAPIKey(t.Context(), keyID)
	first := lastUsed()
	if first == nil {
		t.Fatal("첫 사용이 기록되지 않았습니다")
	}

	// A second request inside the interval must not write again.
	server.touchAPIKey(t.Context(), keyID)
	if second := lastUsed(); second == nil || !second.Equal(*first) {
		t.Fatalf("간격 이내에 다시 기록했습니다: %v -> %v", first, second)
	}
}
