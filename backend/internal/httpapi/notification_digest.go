package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/hkjang/moina/backend/internal/store"
	"github.com/jackc/pgx/v5"
)

const notificationDigestLock int64 = 1297042013

type notificationDigestCandidate struct {
	UserID string
}

const notificationDigestCandidatesSQL = `WITH candidates AS (
		SELECT up.user_id,
			COALESCE(up.payload#>>'{notifications,digest,mode}', 'off') AS digest_mode,
			COALESCE(up.payload#>>'{notifications,digest,time}', '08:00') AS digest_time,
			state.last_sent_at,
			state.config_signature AS stored_signature,
			CASE COALESCE(up.payload#>>'{notifications,digest,mode}', 'off')
				WHEN 'off' THEN 'off'
				WHEN 'hourly' THEN 'hourly'
				WHEN 'daily' THEN 'daily@' || COALESCE(up.payload#>>'{notifications,digest,time}', '08:00')
				ELSE 'invalid'
			END AS config_signature
		FROM user_preferences up
		JOIN users u ON u.id=up.user_id AND u.active
		LEFT JOIN notification_digest_state state ON state.user_id=up.user_id
	)
	SELECT user_id FROM candidates
	WHERE stored_signature IS DISTINCT FROM config_signature
		OR (digest_mode='hourly' AND last_sent_at<=$1::timestamptz-interval '1 hour')
		OR (
			digest_mode='daily'
			AND digest_time ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'
			AND $1::timestamptz >= (((($1::timestamptz AT TIME ZONE $2)::date + digest_time::time) AT TIME ZONE $2))
			AND last_sent_at < (((($1::timestamptz AT TIME ZONE $2)::date + digest_time::time) AT TIME ZONE $2))
		)
	ORDER BY last_sent_at NULLS FIRST,user_id LIMIT 1000`

func (s *Server) runNotificationDigestWorker(ctx context.Context) error {
	for {
		if err := s.generateNotificationDigests(ctx, time.Now().UTC()); err != nil && ctx.Err() == nil {
			slog.WarnContext(ctx, "알림 Digest 생성 실패", "error", err)
		}
		timer := time.NewTimer(time.Minute)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func (s *Server) generateNotificationDigests(ctx context.Context, now time.Time) error {
	general := defaultGeneral()
	if err := s.loadSettingContext(ctx, settingGeneral, &general); err != nil && !store.IsNotFound(err) {
		return err
	}
	location, err := time.LoadLocation(general.DefaultTimezone)
	if err != nil {
		location = time.UTC
		general.DefaultTimezone = "UTC"
	}
	tx, err := s.repo.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var locked bool
	if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, notificationDigestLock).Scan(&locked); err != nil || !locked {
		return err
	}
	rows, err := tx.Query(ctx, notificationDigestCandidatesSQL, now, general.DefaultTimezone)
	if err != nil {
		return err
	}
	candidates := make([]notificationDigestCandidate, 0)
	for rows.Next() {
		var candidate notificationDigestCandidate
		if err := rows.Scan(&candidate.UserID); err != nil {
			rows.Close()
			return err
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, candidate := range candidates {
		userTx, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		err = s.generateNotificationDigestForUser(ctx, userTx, candidate.UserID, now, location)
		if err != nil {
			_ = userTx.Rollback(ctx)
			return err
		}
		if err := userTx.Commit(ctx); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Server) generateNotificationDigestForUser(ctx context.Context, tx pgx.Tx, userID string, now time.Time, location *time.Location) error {
	var payload json.RawMessage
	// Keep the same lock order as preference updates: preferences, schedule
	// state, then notification rows. This also ensures invalid preferences are
	// quarantined against the exact version that was read.
	if err := tx.QueryRow(ctx, `SELECT payload FROM user_preferences WHERE user_id=$1 FOR UPDATE`, userID).Scan(&payload); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	patch, err := decodePreferencesPatch(payload)
	if err != nil {
		slog.WarnContext(ctx, "잘못된 알림 Digest 설정 격리", "userId", userID, "error", err)
		return quarantineNotificationDigestUser(ctx, tx, userID, looseDigestConfigSignature(payload), now)
	}
	preferences, err := applyPreferencesPatch(defaultPreferencesDocument(), patch)
	if err != nil {
		slog.WarnContext(ctx, "잘못된 알림 Digest 설정 격리", "userId", userID, "error", err)
		return quarantineNotificationDigestUser(ctx, tx, userID, looseDigestConfigSignature(payload), now)
	}
	signature := digestConfigSignature(preferences.Notifications.Digest)
	var last time.Time
	var storedSignature string
	err = tx.QueryRow(ctx, `SELECT last_sent_at,config_signature FROM notification_digest_state WHERE user_id=$1 FOR UPDATE`, userID).Scan(&last, &storedSignature)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && storedSignature != signature) {
		if _, err := tx.Exec(ctx, `INSERT INTO notification_digest_state(user_id,last_sent_at,config_signature) VALUES($1,$2,$3)
			ON CONFLICT(user_id) DO UPDATE SET last_sent_at=EXCLUDED.last_sent_at,config_signature=EXCLUDED.config_signature,updated_at=now()`, userID, now, signature); err != nil {
			return err
		}
		// A new or changed subscription starts at the current boundary. It
		// must not replay notifications accumulated while the channel was off
		// or while a different schedule was active.
		_, err = tx.Exec(ctx, `UPDATE notifications SET digested_at=$2 WHERE user_id=$1 AND type<>'digest' AND digested_at IS NULL`, userID, now)
		return err
	}
	if err != nil {
		return err
	}
	due, windowID := digestDue(preferences.Notifications.Digest, last, now, location)
	if !due {
		return nil
	}
	var total, unread int
	byType := map[string]int{}
	notificationIDs := make([]string, 0)
	counts, err := tx.Query(ctx, `SELECT type,array_agg(id ORDER BY delivered_at,id),count(*)::integer,count(*) FILTER (WHERE in_app AND read_at IS NULL)::integer
		FROM notifications WHERE user_id=$1 AND digested_at IS NULL AND type<>'digest'
		GROUP BY type`, userID)
	if err != nil {
		return err
	}
	for counts.Next() {
		var kind string
		var ids []string
		var count, unreadCount int
		if err := counts.Scan(&kind, &ids, &count, &unreadCount); err != nil {
			counts.Close()
			return err
		}
		byType[kind] = count
		notificationIDs = append(notificationIDs, ids...)
		total += count
		unread += unreadCount
	}
	if err := counts.Err(); err != nil {
		counts.Close()
		return err
	}
	counts.Close()
	if total > 0 {
		body := fmt.Sprintf("새 알림 %d개를 한 번에 정리했습니다.", total)
		if unread > 0 {
			body = fmt.Sprintf("읽지 않은 알림 %d개를 포함해 새 알림 %d개를 정리했습니다.", unread, total)
		}
		if err := s.enqueueNotification(ctx, tx, userID, "", "digest", "", map[string]any{
			"body": body, "count": total, "unreadCount": unread, "byType": byType,
		}, fmt.Sprintf("notification:digest:%s:%s", userID, windowID)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE notifications SET digested_at=$3 WHERE user_id=$1 AND id=ANY($2::text[]) AND digested_at IS NULL`, userID, notificationIDs, now); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE notification_digest_state SET last_sent_at=$2,config_signature=$3,updated_at=now() WHERE user_id=$1`, userID, now, signature)
	return err
}

func quarantineNotificationDigestUser(ctx context.Context, tx pgx.Tx, userID, signature string, now time.Time) error {
	if signature == "" {
		signature = "invalid"
	}
	// The caller still holds the preference row lock. A concurrent valid PUT
	// therefore cannot be overwritten or have its newly-created backlog marked
	// by a stale quarantine decision.
	if _, err := tx.Exec(ctx, `INSERT INTO notification_digest_state(user_id,last_sent_at,config_signature) VALUES($1,$2,$3)
		ON CONFLICT(user_id) DO UPDATE SET last_sent_at=EXCLUDED.last_sent_at,config_signature=EXCLUDED.config_signature,updated_at=now()`, userID, now, signature); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE notifications SET digested_at=$2 WHERE user_id=$1 AND type<>'digest' AND digested_at IS NULL`, userID, now)
	return err
}

func looseDigestConfigSignature(payload json.RawMessage) string {
	var value struct {
		Notifications struct {
			Digest notificationDigestPreferences `json:"digest"`
		} `json:"notifications"`
	}
	if err := json.Unmarshal(payload, &value); err != nil {
		return "invalid"
	}
	switch value.Notifications.Digest.Mode {
	case "", "off":
		return "off"
	case "hourly":
		return "hourly"
	case "daily":
		at := value.Notifications.Digest.Time
		if at == "" {
			at = "08:00"
		}
		return "daily@" + at
	default:
		return "invalid"
	}
}

func digestConfigSignature(preferences notificationDigestPreferences) string {
	switch preferences.Mode {
	case "hourly":
		return "hourly"
	case "daily":
		return "daily@" + preferences.Time
	default:
		return "off"
	}
}

func digestDue(preferences notificationDigestPreferences, last, now time.Time, location *time.Location) (bool, string) {
	if preferences.Mode == "hourly" {
		dueAt := last.Add(time.Hour)
		return !now.Before(dueAt), fmt.Sprintf("hour-%d", dueAt.Unix())
	}
	if preferences.Mode != "daily" || !validClockTime(preferences.Time) {
		return false, ""
	}
	localNow := now.In(location)
	hour := int(preferences.Time[0]-'0')*10 + int(preferences.Time[1]-'0')
	minute := int(preferences.Time[3]-'0')*10 + int(preferences.Time[4]-'0')
	dueAt := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), hour, minute, 0, 0, location)
	return !localNow.Before(dueAt) && last.Before(dueAt), dueAt.Format("2006-01-02")
}

func notificationBatched(value notificationPreferences, kind string) bool {
	if value.Digest.Mode == "off" || kind == "digest" || kind == "mention" || kind == "approval_requested" || kind == "approval_approved" || kind == "approval_rejected" || kind == "security" {
		return false
	}
	return true
}
