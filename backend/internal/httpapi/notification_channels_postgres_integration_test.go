package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/hkjang/moina/backend/internal/event"
	"github.com/hkjang/moina/backend/internal/model"
	"github.com/hkjang/moina/backend/internal/secure"
	"github.com/hkjang/moina/backend/internal/store"
)

func TestPostgreSQLNotificationChannelsRemainIndependent(t *testing.T) {
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
	userID := fmt.Sprintf("usr_channel_%d", suffix)
	if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO users(id,username,display_name,roles) VALUES($1,$1,$1,ARRAY['member']::text[])`, userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = repository.Pool().Exec(ctx, `DELETE FROM users WHERE id=$1`, userID)
	})

	preferences := defaultPreferencesDocument()
	preferences.Notifications.InApp.Signals = false
	preferences.Notifications.Toast.Enabled = true
	payload, err := json.Marshal(preferences)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.PutPreference(t.Context(), userID, payload); err != nil {
		t.Fatal(err)
	}
	server := New(repository, nil, "test")

	create := func(id, kind string) {
		t.Helper()
		raw, marshalErr := json.Marshal(notificationEventPayload{UserID: userID, Type: kind, Payload: json.RawMessage(`{}`)})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if handleErr := server.handleOutboxEvent(t.Context(), event.Event{ID: id, Type: notificationCreateEvent, AggregateID: userID, Payload: raw, CreatedAt: time.Now().UTC()}); handleErr != nil {
			t.Fatal(handleErr)
		}
	}

	reactionID := fmt.Sprintf("ntf_channel_reaction_%d", suffix)
	operationalID := fmt.Sprintf("ntf_channel_security_%d", suffix)
	create(reactionID, "reaction")
	create(operationalID, "security")

	var reactionInApp, operationalInApp bool
	var reactionReadAt *time.Time
	if err := repository.Pool().QueryRow(t.Context(), `SELECT in_app,read_at FROM notifications WHERE id=$1`, reactionID).Scan(&reactionInApp, &reactionReadAt); err != nil {
		t.Fatal(err)
	}
	if reactionInApp || reactionReadAt == nil {
		t.Fatalf("disabled in-app reaction stored as visible/unread: in_app=%t read_at=%v", reactionInApp, reactionReadAt)
	}
	if err := repository.Pool().QueryRow(t.Context(), `SELECT in_app FROM notifications WHERE id=$1`, operationalID).Scan(&operationalInApp); err != nil {
		t.Fatal(err)
	}
	if !operationalInApp {
		t.Fatal("mandatory operational notification was hidden")
	}
}

func TestPostgreSQLDigestPreferenceTransitionIsAtomic(t *testing.T) {
	dsn := os.Getenv("MOINA_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("MOINA_TEST_POSTGRES_DSN is not set")
	}
	repository, err := store.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(repository.Close)
	secrets, err := secure.New(bytes.Repeat([]byte{37}, 32))
	if err != nil {
		t.Fatal(err)
	}
	suffix := time.Now().UnixNano()
	userID := fmt.Sprintf("usr_digest_transition_%d", suffix)
	token := fmt.Sprintf("digest-transition-token-%d", suffix)
	csrf := fmt.Sprintf("digest-transition-csrf-%d", suffix)
	notificationID := fmt.Sprintf("ntf_digest_transition_%d", suffix)
	if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO users(id,username,display_name,roles) VALUES($1,$1,$1,ARRAY['member']::text[])`, userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = repository.Pool().Exec(ctx, `DELETE FROM audit_events WHERE actor_id=$1`, userID)
		_, _ = repository.Pool().Exec(ctx, `DELETE FROM users WHERE id=$1`, userID)
	})
	initial, err := json.Marshal(defaultPreferencesDocument())
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.PutPreference(t.Context(), userID, initial); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO notifications(id,user_id,type,payload) VALUES($1,$2,'follow','{}')`, notificationID, userID); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateSession(t.Context(), model.Session{
		ID: "session_digest_transition_" + fmt.Sprint(suffix), UserID: userID,
		TokenHash: secrets.HashToken(token), CSRFHash: secrets.HashToken(csrf),
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	handler := New(repository, secrets, "v0.1.9-test").Handler()
	request := httptest.NewRequest(http.MethodPut, "/api/v1/profile/preferences", bytes.NewBufferString(`{"notifications":{"digest":{"mode":"hourly"}}}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", csrf)
	request.AddCookie(&http.Cookie{Name: SessionCookie, Value: token})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("digest preference update = %d: %s", response.Code, response.Body.String())
	}
	var signature string
	if err := repository.Pool().QueryRow(t.Context(), `SELECT config_signature FROM notification_digest_state WHERE user_id=$1`, userID).Scan(&signature); err != nil {
		t.Fatal(err)
	}
	if signature != "hourly" {
		t.Fatalf("digest config signature = %q", signature)
	}
	var markedAt *time.Time
	if err := repository.Pool().QueryRow(t.Context(), `SELECT digested_at FROM notifications WHERE id=$1`, notificationID).Scan(&markedAt); err != nil {
		t.Fatal(err)
	}
	if markedAt == nil {
		t.Fatal("preference transition committed without baselining existing notifications")
	}
}
