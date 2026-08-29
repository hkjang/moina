// Package event implements the transactional outbox used to move durable domain
// events to asynchronous handlers. Delivery is at-least-once; handlers therefore
// must use the event ID or idempotency key when mutating downstream systems.
package event

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const DefaultMaxAttempts = 8

var (
	ErrInvalidEvent = errors.New("invalid outbox event")
	ErrNotFound     = errors.New("outbox event not found")
	ErrLeaseLost    = errors.New("outbox event lease lost")
)

type Event struct {
	ID             string          `json:"id"`
	Type           string          `json:"type"`
	AggregateID    string          `json:"aggregateId"`
	Payload        json.RawMessage `json:"payload"`
	IdempotencyKey string          `json:"idempotencyKey"`
	Attempts       int             `json:"attempts"`
	MaxAttempts    int             `json:"maxAttempts"`
	AvailableAt    time.Time       `json:"availableAt"`
	CreatedAt      time.Time       `json:"createdAt"`
}

type NewEvent struct {
	ID             string
	Type           string
	AggregateID    string
	Payload        json.RawMessage
	IdempotencyKey string
	MaxAttempts    int
	AvailableAt    time.Time
}

type EnqueueResult struct {
	Event    Event
	Inserted bool
}

// Querier is deliberately satisfied by pgx.Tx and pgxpool.Pool. Passing the
// business transaction guarantees the state change and event commit atomically.
type Querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func Enqueue(ctx context.Context, db Querier, input NewEvent) (EnqueueResult, error) {
	if db == nil {
		return EnqueueResult{}, fmt.Errorf("%w: nil database", ErrInvalidEvent)
	}
	now := time.Now().UTC()
	input.ID = strings.TrimSpace(input.ID)
	input.Type = strings.TrimSpace(input.Type)
	input.AggregateID = strings.TrimSpace(input.AggregateID)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if input.ID == "" {
		input.ID = newID()
	}
	if input.MaxAttempts == 0 {
		input.MaxAttempts = DefaultMaxAttempts
	}
	if input.AvailableAt.IsZero() {
		input.AvailableAt = now
	} else {
		input.AvailableAt = input.AvailableAt.UTC()
	}
	if len(input.Payload) == 0 {
		input.Payload = json.RawMessage(`{}`)
	}
	if err := validate(input); err != nil {
		return EnqueueResult{}, err
	}

	const insert = `INSERT INTO outbox_events
        (id,event_type,aggregate_id,payload,idempotency_key,max_attempts,available_at,created_at)
        VALUES($1,$2,$3,$4,$5,$6,$7,$8)
        ON CONFLICT (idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING
        RETURNING id,event_type,aggregate_id,payload,idempotency_key,attempts,max_attempts,available_at,created_at`
	created, err := scanEvent(db.QueryRow(ctx, insert,
		input.ID, input.Type, input.AggregateID, input.Payload, input.IdempotencyKey,
		input.MaxAttempts, input.AvailableAt, now,
	))
	if err == nil {
		return EnqueueResult{Event: created, Inserted: true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return EnqueueResult{}, fmt.Errorf("outbox enqueue: %w", err)
	}

	const existing = `SELECT id,event_type,aggregate_id,payload,idempotency_key,attempts,max_attempts,available_at,created_at
        FROM outbox_events WHERE idempotency_key=$1`
	created, err = scanEvent(db.QueryRow(ctx, existing, input.IdempotencyKey))
	if err != nil {
		return EnqueueResult{}, fmt.Errorf("outbox idempotency lookup: %w", err)
	}
	return EnqueueResult{Event: created, Inserted: false}, nil
}

func validate(input NewEvent) error {
	if input.ID == "" || len(input.ID) > 160 {
		return fmt.Errorf("%w: id is required and must be at most 160 bytes", ErrInvalidEvent)
	}
	if input.Type == "" || len(input.Type) > 160 {
		return fmt.Errorf("%w: type is required and must be at most 160 bytes", ErrInvalidEvent)
	}
	if input.AggregateID == "" || len(input.AggregateID) > 256 {
		return fmt.Errorf("%w: aggregate ID is required and must be at most 256 bytes", ErrInvalidEvent)
	}
	if input.IdempotencyKey == "" || len(input.IdempotencyKey) > 512 {
		return fmt.Errorf("%w: idempotency key is required and must be at most 512 bytes", ErrInvalidEvent)
	}
	if input.MaxAttempts < 1 || input.MaxAttempts > 100 {
		return fmt.Errorf("%w: max attempts must be between 1 and 100", ErrInvalidEvent)
	}
	if !json.Valid(input.Payload) {
		return fmt.Errorf("%w: payload must be valid JSON", ErrInvalidEvent)
	}
	return nil
}

func scanEvent(row pgx.Row) (Event, error) {
	var item Event
	err := row.Scan(
		&item.ID, &item.Type, &item.AggregateID, &item.Payload, &item.IdempotencyKey,
		&item.Attempts, &item.MaxAttempts, &item.AvailableAt, &item.CreatedAt,
	)
	return item, err
}

func newID() string {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		panic(fmt.Sprintf("crypto/rand unavailable: %v", err))
	}
	return "evt_" + hex.EncodeToString(random[:])
}
