package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hkjang/moina/backend/internal/store"
)

func TestPostgreSQLNotificationDigestCreatesOneDurableSummary(t *testing.T) {
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
	userID := fmt.Sprintf("usr_digest_%d", suffix)
	if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO users(id,username,display_name,roles) VALUES($1,$1,$1,ARRAY['member']::text[])`, userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = repository.Pool().Exec(ctx, `DELETE FROM users WHERE id=$1`, userID)
	})
	preferences := defaultPreferencesDocument()
	preferences.Notifications.Digest.Mode = "hourly"
	payload, err := json.Marshal(preferences)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.PutPreference(t.Context(), userID, payload); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	last := now.Add(-2 * time.Hour)
	if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO notification_digest_state(user_id,last_sent_at,config_signature) VALUES($1,$2,'hourly')`, userID, last); err != nil {
		t.Fatal(err)
	}
	for index, kind := range []string{"follow", "reaction", "reply"} {
		deliveredAt := last.Add(time.Duration(index+1) * time.Minute)
		if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO notifications(id,user_id,type,payload,created_at,delivered_at) VALUES($1,$2,$3,'{}',$4,$5)`, fmt.Sprintf("ntf_digest_%d_%d", suffix, index), userID, kind, deliveredAt, deliveredAt); err != nil {
			t.Fatal(err)
		}
	}
	// A delayed outbox delivery keeps its original occurrence time for the UI,
	// but must be counted by the delivery window so it cannot fall permanently
	// behind an already advanced digest cursor.
	if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO notifications(id,user_id,type,payload,created_at,delivered_at) VALUES($1,$2,'mention','{}',$3,$4)`, fmt.Sprintf("ntf_digest_delayed_%d", suffix), userID, last.Add(-time.Hour), last.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	server := New(repository, nil, "test")
	delayedTx, err := repository.Pool().Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	delayedID := fmt.Sprintf("ntf_digest_uncommitted_%d", suffix)
	if _, err := delayedTx.Exec(t.Context(), `INSERT INTO notifications(id,user_id,type,payload,created_at) VALUES($1,$2,'follow','{}',$3)`, delayedID, userID, last.Add(-time.Hour)); err != nil {
		_ = delayedTx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := server.generateNotificationDigests(t.Context(), now); err != nil {
		_ = delayedTx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := delayedTx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	var eventPayload json.RawMessage
	if err := repository.Pool().QueryRow(t.Context(), `SELECT payload FROM outbox_events WHERE aggregate_id=$1 AND event_type='notification.create' AND idempotency_key LIKE 'notification:digest:%'`, userID).Scan(&eventPayload); err != nil {
		t.Fatal(err)
	}
	var event notificationEventPayload
	if err := json.Unmarshal(eventPayload, &event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "digest" {
		t.Fatalf("digest event type = %q", event.Type)
	}
	var summary struct {
		Count       int            `json:"count"`
		UnreadCount int            `json:"unreadCount"`
		ByType      map[string]int `json:"byType"`
	}
	if err := json.Unmarshal(event.Payload, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Count != 4 || summary.UnreadCount != 4 || summary.ByType["reaction"] != 1 || summary.ByType["mention"] != 1 {
		t.Fatalf("digest summary = %+v", summary)
	}
	if err := server.generateNotificationDigests(t.Context(), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	var events int
	if err := repository.Pool().QueryRow(t.Context(), `SELECT count(*) FROM outbox_events WHERE aggregate_id=$1 AND event_type='notification.create' AND idempotency_key LIKE 'notification:digest:%'`, userID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("digest was duplicated: %d events", events)
	}
	if err := server.generateNotificationDigests(t.Context(), now.Add(61*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := repository.Pool().QueryRow(t.Context(), `SELECT count(*) FROM outbox_events WHERE aggregate_id=$1 AND event_type='notification.create' AND idempotency_key LIKE 'notification:digest:%'`, userID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 2 {
		t.Fatalf("concurrently committed notification was lost: digest events=%d", events)
	}
	var markedAt *time.Time
	if err := repository.Pool().QueryRow(t.Context(), `SELECT digested_at FROM notifications WHERE id=$1`, delayedID).Scan(&markedAt); err != nil {
		t.Fatal(err)
	}
	if markedAt == nil {
		t.Fatal("concurrently committed notification was not marked by the next digest")
	}

	// Turning a schedule off and back on is a new subscription boundary. Events
	// accumulated while it was off must be baselined instead of replayed.
	preferences.Notifications.Digest.Mode = "off"
	payload, err = json.Marshal(preferences)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.PutPreference(t.Context(), userID, payload); err != nil {
		t.Fatal(err)
	}
	if err := server.generateNotificationDigests(t.Context(), now.Add(62*time.Minute)); err != nil {
		t.Fatal(err)
	}
	offID := fmt.Sprintf("ntf_digest_off_period_%d", suffix)
	if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO notifications(id,user_id,type,payload) VALUES($1,$2,'follow','{}')`, offID, userID); err != nil {
		t.Fatal(err)
	}
	preferences.Notifications.Digest.Mode = "hourly"
	payload, err = json.Marshal(preferences)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.PutPreference(t.Context(), userID, payload); err != nil {
		t.Fatal(err)
	}
	if err := server.generateNotificationDigests(t.Context(), now.Add(63*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := repository.Pool().QueryRow(t.Context(), `SELECT count(*) FROM outbox_events WHERE aggregate_id=$1 AND event_type='notification.create' AND idempotency_key LIKE 'notification:digest:%'`, userID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 2 {
		t.Fatalf("off-period notification was replayed after re-enabling digest: events=%d", events)
	}
	if err := repository.Pool().QueryRow(t.Context(), `SELECT digested_at FROM notifications WHERE id=$1`, offID).Scan(&markedAt); err != nil {
		t.Fatal(err)
	}
	if markedAt == nil {
		t.Fatal("off-period notification was not baselined on subscription transition")
	}
}

func TestPostgreSQLNotificationDigestIsolatesCorruptUserPreferences(t *testing.T) {
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
	corruptUserID := fmt.Sprintf("usr_digest_a_corrupt_%d", suffix)
	validUserID := fmt.Sprintf("usr_digest_z_valid_%d", suffix)
	for _, userID := range []string{corruptUserID, validUserID} {
		if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO users(id,username,display_name,roles) VALUES($1,$1,$1,ARRAY['member']::text[])`, userID); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = repository.Pool().Exec(ctx, `DELETE FROM users WHERE id=ANY($1::text[])`, []string{corruptUserID, validUserID})
	})
	corruptPayload := json.RawMessage(`{"notifications":{"digest":{"mode":"hourly","time":"08:00"},"unknown":true}}`)
	if err := repository.PutPreference(t.Context(), corruptUserID, corruptPayload); err != nil {
		t.Fatal(err)
	}
	validPreferences := defaultPreferencesDocument()
	validPreferences.Notifications.Digest.Mode = "hourly"
	validPayload, err := json.Marshal(validPreferences)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.PutPreference(t.Context(), validUserID, validPayload); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	last := now.Add(-2 * time.Hour)
	for _, userID := range []string{corruptUserID, validUserID} {
		if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO notification_digest_state(user_id,last_sent_at,config_signature) VALUES($1,$2,'hourly')`, userID, last); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO notifications(id,user_id,type,payload) VALUES($1,$2,'follow','{}')`, "ntf_"+userID, userID); err != nil {
			t.Fatal(err)
		}
	}
	server := New(repository, nil, "test")
	if err := server.generateNotificationDigests(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	var validEvents int
	if err := repository.Pool().QueryRow(t.Context(), `SELECT count(*) FROM outbox_events WHERE aggregate_id=$1 AND event_type='notification.create' AND idempotency_key LIKE 'notification:digest:%'`, validUserID).Scan(&validEvents); err != nil {
		t.Fatal(err)
	}
	if validEvents != 1 {
		t.Fatalf("valid user was starved by corrupt preferences: events=%d", validEvents)
	}
	var corruptMarkedAt *time.Time
	if err := repository.Pool().QueryRow(t.Context(), `SELECT digested_at FROM notifications WHERE id=$1`, "ntf_"+corruptUserID).Scan(&corruptMarkedAt); err != nil {
		t.Fatal(err)
	}
	if corruptMarkedAt == nil {
		t.Fatal("corrupt user's pending notifications were not quarantined")
	}
}

func TestPostgreSQLNotificationDigestCandidatesMatchLegacyDefaults(t *testing.T) {
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
	offUserID := fmt.Sprintf("usr_digest_legacy_off_%d", suffix)
	dailyUserID := fmt.Sprintf("usr_digest_legacy_daily_%d", suffix)
	for _, userID := range []string{offUserID, dailyUserID} {
		if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO users(id,username,display_name,roles) VALUES($1,$1,$1,ARRAY['member']::text[])`, userID); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = repository.Pool().Exec(ctx, `DELETE FROM users WHERE id=ANY($1::text[])`, []string{offUserID, dailyUserID})
	})
	if err := repository.PutPreference(t.Context(), offUserID, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := repository.PutPreference(t.Context(), dailyUserID, json.RawMessage(`{"notifications":{"digest":{"mode":"daily"}}}`)); err != nil {
		t.Fatal(err)
	}
	fixedNow := time.Date(2026, 8, 30, 7, 0, 0, 0, time.UTC)
	if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO notification_digest_state(user_id,last_sent_at,config_signature) VALUES
		($1,$3,'off'),($2,$3,'daily@08:00')`, offUserID, dailyUserID, fixedNow); err != nil {
		t.Fatal(err)
	}
	rows, err := repository.Pool().Query(t.Context(), notificationDigestCandidatesSQL, fixedNow, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var userID string
		if err := rows.Scan(&userID); err != nil {
			t.Fatal(err)
		}
		if userID == offUserID || userID == dailyUserID {
			t.Fatalf("legacy default preference was needlessly reselected: %s", userID)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgreSQLNotificationDigestSerializesPreferenceTransitions(t *testing.T) {
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
	userID := fmt.Sprintf("usr_digest_serial_%d", suffix)
	if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO users(id,username,display_name,roles) VALUES($1,$1,$1,ARRAY['member']::text[])`, userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = repository.Pool().Exec(ctx, `DELETE FROM users WHERE id=$1`, userID)
	})
	preferences := defaultPreferencesDocument()
	preferences.Notifications.Digest.Mode = "hourly"
	hourlyPayload, err := json.Marshal(preferences)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.PutPreference(t.Context(), userID, hourlyPayload); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO notification_digest_state(user_id,last_sent_at,config_signature) VALUES($1,$2,'hourly')`, userID, now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}

	// Hold the preference lock while applying the same state -> notification
	// order used by PUT. The worker first sees the old due candidate and must
	// then wait, reread the committed preference, and leave an event inserted
	// after the transition baseline untouched.
	transition, err := repository.Pool().Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer transition.Rollback(context.Background())
	var lockedPayload json.RawMessage
	if err := transition.QueryRow(t.Context(), `SELECT payload FROM user_preferences WHERE user_id=$1 FOR UPDATE`, userID).Scan(&lockedPayload); err != nil {
		t.Fatal(err)
	}
	preferences.Notifications.Digest.Mode = "off"
	offPayload, err := json.Marshal(preferences)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transition.Exec(t.Context(), `UPDATE user_preferences SET payload=$2,updated_at=now() WHERE user_id=$1`, userID, offPayload); err != nil {
		t.Fatal(err)
	}
	if _, err := transition.Exec(t.Context(), `UPDATE notification_digest_state SET last_sent_at=$2,config_signature='off',updated_at=now() WHERE user_id=$1`, userID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := transition.Exec(t.Context(), `UPDATE notifications SET digested_at=$2 WHERE user_id=$1 AND type<>'digest' AND digested_at IS NULL`, userID, now); err != nil {
		t.Fatal(err)
	}
	newNotificationID := fmt.Sprintf("ntf_digest_after_baseline_%d", suffix)
	if _, err := transition.Exec(t.Context(), `INSERT INTO notifications(id,user_id,type,payload) VALUES($1,$2,'follow','{}')`, newNotificationID, userID); err != nil {
		t.Fatal(err)
	}

	server := New(repository, nil, "test")
	workerDone := make(chan error, 1)
	go func() {
		workerDone <- server.generateNotificationDigests(context.Background(), now.Add(time.Minute))
	}()
	select {
	case workerErr := <-workerDone:
		t.Fatalf("worker bypassed the held preference lock: %v", workerErr)
	case <-time.After(100 * time.Millisecond):
	}
	if err := transition.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case workerErr := <-workerDone:
		if workerErr != nil {
			t.Fatal(workerErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("digest worker deadlocked with preference transition")
	}
	var markedAt *time.Time
	if err := repository.Pool().QueryRow(t.Context(), `SELECT digested_at FROM notifications WHERE id=$1`, newNotificationID).Scan(&markedAt); err != nil {
		t.Fatal(err)
	}
	if markedAt != nil {
		t.Fatalf("notification committed after transition baseline was consumed: %v", markedAt)
	}
	var signature string
	if err := repository.Pool().QueryRow(t.Context(), `SELECT config_signature FROM notification_digest_state WHERE user_id=$1`, userID).Scan(&signature); err != nil {
		t.Fatal(err)
	}
	if signature != "off" {
		t.Fatalf("worker restored stale digest signature %q", signature)
	}
}
