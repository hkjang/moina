package event

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type rowStub struct {
	values []any
	err    error
}

func (row rowStub) Scan(dest ...any) error {
	if row.err != nil {
		return row.err
	}
	if len(dest) != len(row.values) {
		return errors.New("scan arity mismatch")
	}
	for index, value := range row.values {
		switch pointer := dest[index].(type) {
		case *string:
			*pointer = value.(string)
		case *int:
			*pointer = value.(int)
		case *time.Time:
			*pointer = value.(time.Time)
		case *json.RawMessage:
			*pointer = append((*pointer)[:0], value.(json.RawMessage)...)
		default:
			return errors.New("unsupported scan type")
		}
	}
	return nil
}

type enqueueDBStub struct {
	rows  []pgx.Row
	calls []string
}

func (db *enqueueDBStub) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	db.calls = append(db.calls, sql)
	row := db.rows[0]
	db.rows = db.rows[1:]
	return row
}

func eventRow(id, key string) rowStub {
	now := time.Now().UTC()
	return rowStub{values: []any{id, "post.created", "moin_1", json.RawMessage(`{"ok":true}`), key, 0, 8, now, now}}
}

func TestEnqueueInTransaction(t *testing.T) {
	db := &enqueueDBStub{rows: []pgx.Row{eventRow("evt_existing", "post:moin_1")}}
	result, err := Enqueue(context.Background(), db, NewEvent{
		Type: "post.created", AggregateID: "moin_1", IdempotencyKey: "post:moin_1",
		Payload: json.RawMessage(`{"ok":true}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Inserted || result.Event.ID != "evt_existing" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if len(db.calls) != 1 || !strings.Contains(db.calls[0], "ON CONFLICT (idempotency_key)") {
		t.Fatalf("enqueue did not use idempotent insert: %#v", db.calls)
	}
}

func TestEnqueueReturnsExistingIdempotentEvent(t *testing.T) {
	db := &enqueueDBStub{rows: []pgx.Row{
		rowStub{err: pgx.ErrNoRows},
		eventRow("evt_first", "post:moin_1"),
	}}
	result, err := Enqueue(context.Background(), db, NewEvent{
		Type: "post.created", AggregateID: "moin_1", IdempotencyKey: "post:moin_1",
		Payload: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Inserted || result.Event.ID != "evt_first" || len(db.calls) != 2 {
		t.Fatalf("unexpected conflict result: %#v calls=%d", result, len(db.calls))
	}
}

func TestEnqueueValidation(t *testing.T) {
	db := &enqueueDBStub{}
	_, err := Enqueue(context.Background(), db, NewEvent{
		Type: "post.created", AggregateID: "moin_1", IdempotencyKey: "post:moin_1",
		Payload: json.RawMessage(`{"broken"`),
	})
	if !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("expected invalid event, got %v", err)
	}
}

func TestBackoffIsExponentialAndCapped(t *testing.T) {
	base, cap := time.Second, 10*time.Second
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 10 * time.Second, 10 * time.Second}
	for index, expected := range want {
		if got := Backoff(index+1, base, cap); got != expected {
			t.Errorf("attempt %d: got %s want %s", index+1, got, expected)
		}
	}
}

func TestClaimSQLUsesSkipLocked(t *testing.T) {
	for _, fragment := range []string{"FOR UPDATE SKIP LOCKED", "available_at <= $1", "locked_at <= $2", "attempts=event.attempts+1"} {
		if !strings.Contains(claimSQL, fragment) {
			t.Errorf("claim SQL missing %q", fragment)
		}
	}
}

func TestFailureAndDeadLetterQueriesAreAuditable(t *testing.T) {
	for _, fragment := range []string{"INSERT INTO outbox_attempts", "dead_lettered", "RETURNING event_id"} {
		if !strings.Contains(markFailedSQL, fragment) {
			t.Errorf("failure SQL missing %q", fragment)
		}
	}
	for _, fragment := range []string{"last_error", "last_attempt_at", "dead_lettered_at", "ORDER BY dead_lettered_at DESC"} {
		if !strings.Contains(listDeadLettersSQL, fragment) {
			t.Errorf("dead letter SQL missing %q", fragment)
		}
	}
}
