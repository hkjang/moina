package event

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

type signalTxStub struct {
	sql  string
	args []any
}

func (tx *signalTxStub) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tx.sql, tx.args = sql, args
	return pgconn.NewCommandTag("SELECT 1"), nil
}

func TestPublishNotificationSignalIsTransactionCallable(t *testing.T) {
	tx := &signalTxStub{}
	err := PublishNotificationSignal(context.Background(), tx, NotificationSignal{
		NotificationID: "evt_notification_1", UserID: "user_1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tx.sql, "pg_notify('moina_notifications'") || len(tx.args) != 1 {
		t.Fatalf("unexpected publish: sql=%q args=%#v", tx.sql, tx.args)
	}
	var signal NotificationSignal
	if err := json.Unmarshal([]byte(tx.args[0].(string)), &signal); err != nil {
		t.Fatal(err)
	}
	if signal.NotificationID != "evt_notification_1" || signal.UserID != "user_1" {
		t.Fatalf("signal = %#v", signal)
	}
}
