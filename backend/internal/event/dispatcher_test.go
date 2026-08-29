package event

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

type queueStub struct {
	mu        sync.Mutex
	items     []Event
	delivered []string
	failed    []string
}

func (queue *queueStub) Claim(_ context.Context, _ string, batch int, _ time.Duration) ([]Event, error) {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if len(queue.items) == 0 {
		return nil, nil
	}
	count := min(batch, len(queue.items))
	items := append([]Event(nil), queue.items[:count]...)
	queue.items = queue.items[count:]
	return items, nil
}
func (queue *queueStub) MarkDelivered(_ context.Context, id, _ string) error {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	queue.delivered = append(queue.delivered, id)
	return nil
}
func (queue *queueStub) MarkFailed(_ context.Context, item Event, _ string, _ error, _, _ time.Duration) (bool, error) {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	queue.failed = append(queue.failed, item.ID)
	return item.Attempts >= item.MaxAttempts, nil
}
func (*queueStub) RetryDeadLetter(context.Context, string) error { return nil }
func (*queueStub) Stats(context.Context) (Stats, error)          { return Stats{}, nil }
func (*queueStub) Listen(ctx context.Context, _ chan<- struct{}) error {
	<-ctx.Done()
	return ctx.Err()
}

type observerStub struct {
	mu       sync.Mutex
	failures int
}

func (*observerStub) SetOutboxLag(time.Duration) {}
func (observer *observerStub) IncOutboxFailures() {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	observer.failures++
}

func TestDispatcherDeliversAndDeadLetters(t *testing.T) {
	queue := &queueStub{items: []Event{
		{ID: "ok", Type: "ok", Attempts: 1, MaxAttempts: 3},
		{ID: "bad", Type: "bad", Attempts: 3, MaxAttempts: 3},
	}}
	observer := &observerStub{}
	config := DefaultDispatcherConfig()
	config.WorkerCount = 2
	config.PollInterval = 5 * time.Millisecond
	config.HandlerTimeout = time.Second
	dispatcher, err := NewDispatcher(queue, HandlerFunc(func(_ context.Context, item Event) error {
		if item.Type == "bad" {
			return errors.New("handler failed")
		}
		return nil
	}), observer, slog.New(slog.NewTextHandler(io.Discard, nil)), config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := dispatcher.Run(ctx); err != nil {
		t.Fatal(err)
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if len(queue.delivered) != 1 || queue.delivered[0] != "ok" {
		t.Fatalf("delivered = %#v", queue.delivered)
	}
	if len(queue.failed) != 1 || queue.failed[0] != "bad" {
		t.Fatalf("failed = %#v", queue.failed)
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if observer.failures != 1 {
		t.Fatalf("failures = %d", observer.failures)
	}
}
