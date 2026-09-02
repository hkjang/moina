package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hkjang/moina/backend/internal/model"
	"github.com/hkjang/moina/backend/internal/store"
)

func TestPostgreSQLRetentionPurgesOnlyExpiredRecords(t *testing.T) {
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
	userID := fmt.Sprintf("usr_retention_%d", suffix)
	if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO users(id,username,display_name,roles) VALUES($1,$1,$1,ARRAY['member']::text[])`, userID); err != nil {
		t.Fatal(err)
	}
	id := func(prefix string) string { return fmt.Sprintf("%s_retention_%d", prefix, suffix) }
	oldID, freshID := id("old"), id("fresh")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, table := range []string{"audit_events", "notifications", "outbox_events", "ai_usage_events"} {
			_, _ = repository.Pool().Exec(ctx, `DELETE FROM `+table+` WHERE id=ANY($1::text[])`, []string{oldID, freshID})
		}
		_, _ = repository.Pool().Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, userID)
		_, _ = repository.Pool().Exec(ctx, `DELETE FROM settings WHERE key=$1`, settingRetention)
		_, _ = repository.Pool().Exec(ctx, `DELETE FROM users WHERE id=$1`, userID)
	})

	now := time.Now().UTC()
	stale, recent := now.AddDate(0, 0, -120), now.AddDate(0, 0, -1)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, execErr := repository.Pool().Exec(t.Context(), query, args...); execErr != nil {
			t.Fatal(execErr)
		}
	}
	for _, row := range []struct {
		id string
		at time.Time
	}{{oldID, stale}, {freshID, recent}} {
		exec(`INSERT INTO audit_events(id,actor_id,action,success,created_at) VALUES($1,$2,'test.retention',true,$3)`, row.id, userID, row.at)
		exec(`INSERT INTO notifications(id,user_id,type,created_at) VALUES($1,$2,'system',$3)`, row.id, userID, row.at)
		exec(`INSERT INTO ai_usage_events(id,user_id,model,api_style,max_tokens,success,created_at) VALUES($1,$2,'m','responses',1,true,$3)`, row.id, userID, row.at)
	}
	// One delivered event long past its window and one dead letter of the same
	// age: only the delivered one may go, because the dead letter is still
	// waiting for an administrator to retry it.
	exec(`INSERT INTO outbox_events(id,event_type,aggregate_id,idempotency_key,created_at,delivered_at) VALUES($1,'test.retention',$2,$1,$3,$3)`, oldID, userID, stale)
	exec(`INSERT INTO outbox_events(id,event_type,aggregate_id,idempotency_key,created_at,delivered_at,dead_lettered_at) VALUES($1,'test.retention',$2,$1,$3,NULL,$3)`, freshID, userID, stale)

	expiredSession := model.Session{ID: id("ses_expired"), UserID: userID, TokenHash: id("tok_expired"), CSRFHash: "csrf", ExpiresAt: now.Add(-time.Hour)}
	liveSession := model.Session{ID: id("ses_live"), UserID: userID, TokenHash: id("tok_live"), CSRFHash: "csrf", ExpiresAt: now.Add(time.Hour)}
	for _, session := range []model.Session{expiredSession, liveSession} {
		if err := repository.CreateSession(t.Context(), session); err != nil {
			t.Fatal(err)
		}
	}

	policy, err := json.Marshal(retentionConfig{AuditDays: 30, NotificationDays: 30, OutboxDays: 30, AIUsageDays: 30})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.PutSetting(t.Context(), model.SettingRecord{Key: settingRetention, Payload: policy}, nil); err != nil {
		t.Fatal(err)
	}

	server := New(repository, nil, "test")
	if err := server.purgeExpiredRecords(t.Context(), now); err != nil {
		t.Fatal(err)
	}

	exists := func(query string, args ...any) bool {
		t.Helper()
		var found bool
		if scanErr := repository.Pool().QueryRow(t.Context(), query, args...).Scan(&found); scanErr != nil {
			t.Fatal(scanErr)
		}
		return found
	}
	for _, table := range []string{"audit_events", "notifications", "ai_usage_events"} {
		if exists(`SELECT EXISTS(SELECT 1 FROM `+table+` WHERE id=$1)`, oldID) {
			t.Fatalf("%s의 보존 기간 초과 행이 남았습니다", table)
		}
		if !exists(`SELECT EXISTS(SELECT 1 FROM `+table+` WHERE id=$1)`, freshID) {
			t.Fatalf("%s의 보존 기간 내 행이 삭제되었습니다", table)
		}
	}
	if exists(`SELECT EXISTS(SELECT 1 FROM outbox_events WHERE id=$1)`, oldID) {
		t.Fatal("전달 완료된 오래된 Outbox 이벤트가 남았습니다")
	}
	if !exists(`SELECT EXISTS(SELECT 1 FROM outbox_events WHERE id=$1)`, freshID) {
		t.Fatal("미전달 Outbox 이벤트가 보존 정리에 삭제되었습니다. 관리자 재처리 대상이 사라집니다")
	}
	if exists(`SELECT EXISTS(SELECT 1 FROM sessions WHERE id=$1)`, expiredSession.ID) {
		t.Fatal("만료 세션이 남았습니다")
	}
	if !exists(`SELECT EXISTS(SELECT 1 FROM sessions WHERE id=$1)`, liveSession.ID) {
		t.Fatal("유효한 세션이 삭제되었습니다")
	}
}

func TestPostgreSQLRetentionKeepsEverythingWhenDisabled(t *testing.T) {
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
	userID := fmt.Sprintf("usr_retention_off_%d", suffix)
	auditID := fmt.Sprintf("aud_retention_off_%d", suffix)
	if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO users(id,username,display_name,roles) VALUES($1,$1,$1,ARRAY['member']::text[])`, userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = repository.Pool().Exec(ctx, `DELETE FROM audit_events WHERE id=$1`, auditID)
		_, _ = repository.Pool().Exec(ctx, `DELETE FROM settings WHERE key=$1`, settingRetention)
		_, _ = repository.Pool().Exec(ctx, `DELETE FROM users WHERE id=$1`, userID)
	})
	// The default policy leaves auditDays at zero, so a decade old audit row
	// must survive a sweep that no administrator has opted into.
	if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO audit_events(id,actor_id,action,success,created_at) VALUES($1,$2,'test.retention',true,$3)`, auditID, userID, time.Now().UTC().AddDate(-10, 0, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(t.Context(), `DELETE FROM settings WHERE key=$1`, settingRetention); err != nil {
		t.Fatal(err)
	}

	server := New(repository, nil, "test")
	if err := server.purgeExpiredRecords(t.Context(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var found bool
	if err := repository.Pool().QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM audit_events WHERE id=$1)`, auditID).Scan(&found); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("보존 정책이 설정되지 않았는데 감사 로그가 삭제되었습니다")
	}
}
