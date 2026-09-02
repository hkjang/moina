package event

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

const notificationChannel = "moina_notifications"

type Execer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

type NotificationSignal struct {
	NotificationID string `json:"notificationId"`
	UserID         string `json:"userId"`
}

// PublishNotificationSignal should run in the same pgx.Tx that inserts the
// notification (normally with Event.ID as its primary key). PostgreSQL delivers
// pg_notify only after commit, so other instances never fan out rolled-back rows.
func PublishNotificationSignal(ctx context.Context, tx Execer, signal NotificationSignal) error {
	if tx == nil {
		return errors.New("notification signal transaction is required")
	}
	signal.NotificationID = strings.TrimSpace(signal.NotificationID)
	signal.UserID = strings.TrimSpace(signal.UserID)
	if signal.NotificationID == "" || signal.UserID == "" || len(signal.NotificationID) > 256 || len(signal.UserID) > 256 {
		return errors.New("notification ID and user ID are required")
	}
	payload, err := json.Marshal(signal)
	if err != nil {
		return fmt.Errorf("notification signal marshal: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_notify('`+notificationChannel+`',$1)`, string(payload)); err != nil {
		return fmt.Errorf("notification signal publish: %w", err)
	}
	return nil
}

// ListenNotificationSignals reserves one pool connection and emits committed
// cross-instance fanout hints. Consumers still load the durable notification row.
func (r *Repository) ListenNotificationSignals(ctx context.Context, signals chan<- NotificationSignal) error {
	if r == nil || r.pool == nil || signals == nil {
		return errors.New("notification listener repository and output channel are required")
	}
	connection, err := r.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("notification listener acquire: %w", err)
	}
	defer connection.Release()
	if _, err := connection.Exec(ctx, `LISTEN `+notificationChannel); err != nil {
		return fmt.Errorf("notification LISTEN: %w", err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = connection.Exec(cleanup, `UNLISTEN `+notificationChannel)
	}()
	for {
		notification, err := connection.Conn().WaitForNotification(ctx)
		if err != nil {
			return err
		}
		var signal NotificationSignal
		if err := json.Unmarshal([]byte(notification.Payload), &signal); err != nil || signal.NotificationID == "" || signal.UserID == "" {
			continue
		}
		select {
		case signals <- signal:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
