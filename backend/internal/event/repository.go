package event

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const outboxChannel = "moina_outbox"

const claimSQL = `WITH candidates AS (
    SELECT id
    FROM outbox_events
    WHERE delivered_at IS NULL
      AND dead_lettered_at IS NULL
      AND available_at <= $1
      AND (locked_at IS NULL OR locked_at <= $2)
    ORDER BY available_at,created_at,id
    FOR UPDATE SKIP LOCKED
    LIMIT $3
)
UPDATE outbox_events AS event
SET locked_at=$1,locked_by=$4,attempts=event.attempts+1,last_attempt_at=$1
FROM candidates
WHERE event.id=candidates.id
RETURNING event.id,event.event_type,event.aggregate_id,event.payload,
	      COALESCE(event.idempotency_key,'legacy:'||event.id),event.attempts,event.max_attempts,event.available_at,event.created_at`

const markFailedSQL = `WITH updated AS (
    UPDATE outbox_events
    SET last_error=$3,available_at=$4::timestamptz,dead_lettered_at=CASE WHEN $5::boolean THEN $6::timestamptz ELSE NULL END,
        locked_at=NULL,locked_by=''
    WHERE id=$1 AND locked_by=$2 AND delivered_at IS NULL
    RETURNING id,attempts,last_attempt_at
)
INSERT INTO outbox_attempts(event_id,attempt,worker_id,error,failed_at,next_available_at,dead_lettered)
SELECT id,attempts,$2,$3,$6::timestamptz,$4::timestamptz,$5::boolean FROM updated
RETURNING event_id`

const listDeadLettersSQL = `SELECT id,event_type,aggregate_id,payload,COALESCE(idempotency_key,''),attempts,max_attempts,available_at,created_at,
       last_error,last_attempt_at,dead_lettered_at
FROM outbox_events
WHERE dead_lettered_at IS NOT NULL AND delivered_at IS NULL
ORDER BY dead_lettered_at DESC,id
LIMIT $1`

type Repository struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

type Stats struct {
	Pending    int64
	DeadLetter int64
	Lag        time.Duration
}

type DeadLetter struct {
	Event          Event      `json:"event"`
	LastError      string     `json:"lastError"`
	LastAttemptAt  *time.Time `json:"lastAttemptAt,omitempty"`
	DeadLetteredAt time.Time  `json:"deadLetteredAt"`
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool, now: func() time.Time { return time.Now().UTC() }}
}

func (r *Repository) Enqueue(ctx context.Context, input NewEvent) (EnqueueResult, error) {
	return Enqueue(ctx, r.pool, input)
}

func (r *Repository) Claim(ctx context.Context, workerID string, batch int, lease time.Duration) ([]Event, error) {
	workerID = strings.TrimSpace(workerID)
	if r == nil || r.pool == nil {
		return nil, errors.New("outbox repository has no pool")
	}
	if workerID == "" {
		return nil, errors.New("outbox worker ID is required")
	}
	if batch < 1 || batch > 1000 {
		return nil, errors.New("outbox claim batch must be between 1 and 1000")
	}
	if lease <= 0 {
		return nil, errors.New("outbox lease must be positive")
	}
	now := r.now()
	rows, err := r.pool.Query(ctx, claimSQL, now, now.Add(-lease), batch, workerID)
	if err != nil {
		return nil, fmt.Errorf("outbox claim: %w", err)
	}
	defer rows.Close()
	items := make([]Event, 0, batch)
	for rows.Next() {
		item, err := scanEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("outbox claim scan: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("outbox claim rows: %w", err)
	}
	return items, nil
}

func (r *Repository) MarkDelivered(ctx context.Context, eventID, workerID string) error {
	tag, err := r.pool.Exec(ctx, `UPDATE outbox_events
        SET delivered_at=$3,locked_at=NULL,locked_by='',last_error=''
        WHERE id=$1 AND locked_by=$2 AND delivered_at IS NULL AND dead_lettered_at IS NULL`,
		eventID, workerID, r.now())
	if err != nil {
		return fmt.Errorf("outbox mark delivered: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	return nil
}

func (r *Repository) MarkFailed(ctx context.Context, item Event, workerID string, cause error, base, maximum time.Duration) (bool, error) {
	message := "unknown handler failure"
	if cause != nil {
		message = truncateError(cause.Error(), 4000)
	}
	dead := item.Attempts >= item.MaxAttempts
	now := r.now()
	next := now
	if !dead {
		next = now.Add(Backoff(item.Attempts, base, maximum))
	}
	var updatedID string
	err := r.pool.QueryRow(ctx, markFailedSQL, item.ID, workerID, message, next, dead, now).Scan(&updatedID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, ErrLeaseLost
		}
		return false, fmt.Errorf("outbox mark failed: %w", err)
	}
	if updatedID != item.ID {
		return false, ErrLeaseLost
	}
	return dead, nil
}

func (r *Repository) ListDeadLetters(ctx context.Context, limit int) ([]DeadLetter, error) {
	if limit < 1 || limit > 1000 {
		return nil, errors.New("dead letter limit must be between 1 and 1000")
	}
	rows, err := r.pool.Query(ctx, listDeadLettersSQL, limit)
	if err != nil {
		return nil, fmt.Errorf("outbox dead letter list: %w", err)
	}
	defer rows.Close()
	items := make([]DeadLetter, 0, limit)
	for rows.Next() {
		var item DeadLetter
		var lastAttempt pgtype.Timestamptz
		if err := rows.Scan(
			&item.Event.ID, &item.Event.Type, &item.Event.AggregateID, &item.Event.Payload,
			&item.Event.IdempotencyKey, &item.Event.Attempts, &item.Event.MaxAttempts,
			&item.Event.AvailableAt, &item.Event.CreatedAt, &item.LastError,
			&lastAttempt, &item.DeadLetteredAt,
		); err != nil {
			return nil, fmt.Errorf("outbox dead letter scan: %w", err)
		}
		if lastAttempt.Valid {
			value := lastAttempt.Time
			item.LastAttemptAt = &value
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("outbox dead letter rows: %w", err)
	}
	return items, nil
}

// RetryDeadLetter makes a dead-lettered event immediately claimable again.
func (r *Repository) RetryDeadLetter(ctx context.Context, eventID string) error {
	tag, err := r.pool.Exec(ctx, `UPDATE outbox_events
        SET attempts=0,available_at=$2,last_attempt_at=NULL,last_error='',locked_at=NULL,locked_by='',dead_lettered_at=NULL
        WHERE id=$1 AND delivered_at IS NULL AND dead_lettered_at IS NOT NULL`, eventID, r.now())
	if err != nil {
		return fmt.Errorf("outbox dead letter retry: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

func (r *Repository) Stats(ctx context.Context) (Stats, error) {
	var stats Stats
	var lagSeconds float64
	err := r.pool.QueryRow(ctx, `SELECT
        count(*) FILTER (WHERE delivered_at IS NULL AND dead_lettered_at IS NULL),
        count(*) FILTER (WHERE dead_lettered_at IS NOT NULL),
        COALESCE(GREATEST(EXTRACT(EPOCH FROM (now()-min(created_at) FILTER (WHERE delivered_at IS NULL AND dead_lettered_at IS NULL))),0),0)
        FROM outbox_events`).Scan(&stats.Pending, &stats.DeadLetter, &lagSeconds)
	if err != nil {
		return Stats{}, fmt.Errorf("outbox stats: %w", err)
	}
	stats.Lag = time.Duration(lagSeconds * float64(time.Second))
	return stats, nil
}

// Listen reserves one pool connection and wakes workers after committed NOTIFY
// messages. Polling in Dispatcher remains the fallback when LISTEN is unavailable.
func (r *Repository) Listen(ctx context.Context, wake chan<- struct{}) error {
	connection, err := r.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("outbox listener acquire: %w", err)
	}
	defer connection.Release()
	if _, err := connection.Exec(ctx, `LISTEN `+outboxChannel); err != nil {
		return fmt.Errorf("outbox LISTEN: %w", err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = connection.Exec(cleanup, `UNLISTEN `+outboxChannel)
	}()
	for {
		if _, err := connection.Conn().WaitForNotification(ctx); err != nil {
			return err
		}
		select {
		case wake <- struct{}{}:
		default:
		}
	}
}

func Backoff(attempt int, base, maximum time.Duration) time.Duration {
	if base <= 0 {
		base = time.Second
	}
	if maximum <= 0 || maximum < base {
		maximum = 15 * time.Minute
		if maximum < base {
			maximum = base
		}
	}
	if attempt < 1 {
		attempt = 1
	}
	delay := base
	for step := 1; step < attempt; step++ {
		if delay >= maximum || delay > maximum/2 {
			return maximum
		}
		delay *= 2
	}
	if delay > maximum {
		return maximum
	}
	return delay
}

func truncateError(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
