package httpapi

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hkjang/moina/backend/internal/store"
)

func TestPostgreSQLRateLimitIsSharedAcrossInstances(t *testing.T) {
	dsn := os.Getenv("MOINA_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("MOINA_TEST_POSTGRES_DSN is not set")
	}
	repository, err := store.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(repository.Close)
	key := fmt.Sprintf("api-key|shared-%d", time.Now().UnixNano())
	digest := sha256.Sum256([]byte(key))
	keyHash := fmt.Sprintf("%x", digest[:])
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = repository.Pool().Exec(ctx, `DELETE FROM rate_limit_buckets WHERE key_hash=$1`, keyHash)
	})

	first := New(repository, nil, "test")
	second := New(repository, nil, "test")
	for index, server := range []*Server{first, second} {
		allowed, allowErr := server.allow(t.Context(), key, 2, time.Minute)
		if allowErr != nil || !allowed {
			t.Fatalf("instance request %d allowed=%t err=%v", index+1, allowed, allowErr)
		}
	}
	allowed, allowErr := first.allow(t.Context(), key, 2, time.Minute)
	if allowErr != nil || allowed {
		t.Fatalf("shared third request allowed=%t err=%v", allowed, allowErr)
	}
}
