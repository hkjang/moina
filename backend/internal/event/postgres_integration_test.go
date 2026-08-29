package event

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	storepkg "github.com/hkjang/moina/backend/internal/store"
)

func TestPostgreSQLOutboxLifecycle(t *testing.T) {
	dsn := os.Getenv("MOINA_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("MOINA_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	database, err := storepkg.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repository := NewRepository(database.Pool())
	now := time.Unix(2, 0).UTC()
	repository.now = func() time.Time { return now }
	key := "integration:" + newID()
	result, err := repository.Enqueue(ctx, NewEvent{
		Type: "notification.create", AggregateID: "user_integration", IdempotencyKey: key,
		Payload: json.RawMessage(`{"userId":"user_integration"}`), MaxAttempts: 2, AvailableAt: time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = database.Pool().Exec(context.Background(), `DELETE FROM outbox_events WHERE id=$1`, result.Event.ID)
	}()
	duplicate, err := repository.Enqueue(ctx, NewEvent{
		Type: "notification.create", AggregateID: "user_integration", IdempotencyKey: key,
		Payload: json.RawMessage(`{}`), MaxAttempts: 2,
	})
	if err != nil || duplicate.Inserted || duplicate.Event.ID != result.Event.ID {
		t.Fatalf("duplicate=%#v err=%v", duplicate, err)
	}

	claimed, err := repository.Claim(ctx, "worker-a", 1, time.Minute)
	if err != nil || len(claimed) != 1 || claimed[0].ID != result.Event.ID {
		t.Fatalf("first claim=%#v err=%v", claimed, err)
	}
	second, err := repository.Claim(ctx, "worker-b", 1, time.Minute)
	if err != nil || len(second) != 0 {
		t.Fatalf("locked event was claimed twice: %#v err=%v", second, err)
	}
	dead, err := repository.MarkFailed(ctx, claimed[0], "worker-a", errors.New("first failure"), time.Second, time.Minute)
	if err != nil || dead {
		t.Fatalf("first failure dead=%v err=%v", dead, err)
	}
	now = now.Add(2 * time.Second)
	claimed, err = repository.Claim(ctx, "worker-b", 1, time.Minute)
	if err != nil || len(claimed) != 1 || claimed[0].Attempts != 2 {
		t.Fatalf("retry claim=%#v err=%v", claimed, err)
	}
	dead, err = repository.MarkFailed(ctx, claimed[0], "worker-b", errors.New("second failure"), time.Second, time.Minute)
	if err != nil || !dead {
		t.Fatalf("second failure dead=%v err=%v", dead, err)
	}
	deadLetters, err := repository.ListDeadLetters(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range deadLetters {
		if item.Event.ID == result.Event.ID {
			found = item.LastError == "second failure" && item.LastAttemptAt != nil && !item.DeadLetteredAt.IsZero()
		}
	}
	if !found {
		t.Fatalf("dead letter detail missing: %#v", deadLetters)
	}
	var attempts int
	if err := database.Pool().QueryRow(ctx, `SELECT count(*) FROM outbox_attempts WHERE event_id=$1`, result.Event.ID).Scan(&attempts); err != nil || attempts != 2 {
		t.Fatalf("attempt history=%d err=%v", attempts, err)
	}
	if err := repository.RetryDeadLetter(ctx, result.Event.ID); err != nil {
		t.Fatal(err)
	}
	claimed, err = repository.Claim(ctx, "worker-a", 1, time.Minute)
	if err != nil || len(claimed) != 1 || claimed[0].Attempts != 1 {
		t.Fatalf("manual retry claim=%#v err=%v", claimed, err)
	}
	if err := repository.MarkDelivered(ctx, result.Event.ID, "worker-a"); err != nil {
		t.Fatal(err)
	}

	listenCtx, stopListening := context.WithCancel(ctx)
	wake := make(chan struct{}, 1)
	listenResult := make(chan error, 1)
	go func() { listenResult <- repository.Listen(listenCtx, wake) }()
	time.Sleep(100 * time.Millisecond)
	wakeupEvent, err := repository.Enqueue(ctx, NewEvent{
		Type: "notification.create", AggregateID: "user_wakeup", IdempotencyKey: "integration:wakeup:" + newID(),
		Payload: json.RawMessage(`{}`), AvailableAt: now,
	})
	if err != nil {
		stopListening()
		t.Fatal(err)
	}
	defer func() {
		_, _ = database.Pool().Exec(context.Background(), `DELETE FROM outbox_events WHERE id=$1`, wakeupEvent.Event.ID)
	}()
	select {
	case <-wake:
	case <-time.After(3 * time.Second):
		stopListening()
		t.Fatal("LISTEN/NOTIFY did not wake the outbox listener")
	}
	stopListening()
	select {
	case <-listenResult:
	case <-time.After(3 * time.Second):
		t.Fatal("outbox listener did not stop after context cancellation")
	}
}
