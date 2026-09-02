package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/hkjang/moina/backend/internal/event"
	mediastore "github.com/hkjang/moina/backend/internal/media"
	"github.com/hkjang/moina/backend/internal/model"
	"github.com/hkjang/moina/backend/internal/secure"
	"github.com/hkjang/moina/backend/internal/store"
	"github.com/hkjang/moina/backend/internal/visibility"
	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/errgroup"
)

const (
	notificationCreateEvent = "notification.create"
	notificationEmailEvent  = "notification.email"
)

var errNoIndependentApprover = errors.New("no independent approver")

type notificationEventPayload struct {
	UserID   string          `json:"userId"`
	ActorID  string          `json:"actorId,omitempty"`
	Type     string          `json:"type"`
	TargetID string          `json:"targetId,omitempty"`
	Payload  json.RawMessage `json:"payload"`
}

type notificationEmailEventPayload struct {
	NotificationID string `json:"notificationId"`
	UserID         string `json:"userId"`
}

// enqueueNotification must receive the same transaction that mutates the
// business entity. Callers choose a key scoped to the business operation while
// the generated outbox event ID becomes the notification's idempotency key.
func (s *Server) enqueueNotification(ctx context.Context, tx event.Querier, userID, actorID, kind, targetID string, payload any, idempotencyKey string) error {
	userID = strings.TrimSpace(userID)
	actorID = strings.TrimSpace(actorID)
	if userID == "" || userID == actorID {
		return nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("notification payload marshal: %w", err)
	}
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage(`{}`)
	}
	eventPayload, err := json.Marshal(notificationEventPayload{
		UserID: userID, ActorID: actorID, Type: strings.TrimSpace(kind),
		TargetID: strings.TrimSpace(targetID), Payload: raw,
	})
	if err != nil {
		return fmt.Errorf("notification event marshal: %w", err)
	}
	_, err = event.Enqueue(ctx, tx, event.NewEvent{
		Type: notificationCreateEvent, AggregateID: userID,
		Payload: eventPayload, IdempotencyKey: strings.TrimSpace(idempotencyKey),
	})
	return err
}

func (s *Server) enqueueMentionNotifications(ctx context.Context, tx pgx.Tx, actorID, postID, content string) error {
	usernames := extractMentions(content)
	if len(usernames) == 0 {
		return nil
	}
	// A mention only notifies someone who could have read the Moin anyway, so
	// the same visibility rule applies with the mentioned user as the viewer.
	query := `SELECT mentioned.id
		FROM users mentioned JOIN posts post ON post.id=$2
		WHERE mentioned.active AND lower(mentioned.username)=ANY($1)
		AND ` + visibility.NotBlockedBetween("mentioned.id", "$3") +
		` AND ` + visibility.Moin("post", "mentioned.id")
	rows, err := tx.Query(ctx, query, usernames, postID, actorID)
	if err != nil {
		return err
	}
	userIDs := make([]string, 0, len(usernames))
	for rows.Next() {
		var userID string
		if err := rows.Scan(&userID); err != nil {
			rows.Close()
			return err
		}
		userIDs = append(userIDs, userID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, userID := range userIDs {
		if err := s.enqueueNotification(ctx, tx, userID, actorID, "mention", postID,
			map[string]string{"postId": postID}, fmt.Sprintf("notification:post:%s:mention:%s", postID, userID)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) enqueueApproverNotifications(ctx context.Context, tx pgx.Tx, actorID, approvalID, postID string, roles []string) error {
	rows, err := tx.Query(ctx, `SELECT u.id
		FROM users u
		WHERE u.active AND u.id<>$2 AND u.roles && $1::text[]
		AND EXISTS (
			SELECT 1 FROM role_permissions rp
			WHERE rp.role_name=ANY(u.roles)
			AND rp.permission IN ('*','approvals:*','approvals:review')
		)`, roles, actorID)
	if err != nil {
		return err
	}
	userIDs := make([]string, 0)
	for rows.Next() {
		var userID string
		if err := rows.Scan(&userID); err != nil {
			rows.Close()
			return err
		}
		userIDs = append(userIDs, userID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(userIDs) == 0 {
		return errNoIndependentApprover
	}
	for _, userID := range userIDs {
		if err := s.enqueueNotification(ctx, tx, userID, actorID, "approval_requested", approvalID,
			map[string]string{"postId": postID, "approvalId": approvalID},
			fmt.Sprintf("notification:approval:%s:requested:%s", approvalID, userID)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) approvalNotificationEligible(ctx context.Context, userID, approvalID string, cfg model.WorkflowConfig) (bool, error) {
	if !cfg.Enabled || len(cfg.ApproverRoles) == 0 || strings.TrimSpace(userID) == "" || strings.TrimSpace(approvalID) == "" {
		return false, nil
	}
	var eligible bool
	err := s.repo.Pool().QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM users u
		JOIN approval_requests approval ON approval.id=$2
		WHERE u.id=$1 AND u.active AND approval.status='pending' AND approval.requester_id<>u.id
		AND u.roles && $3::text[]
		AND EXISTS (
			SELECT 1 FROM role_permissions rp
			WHERE rp.role_name=ANY(u.roles)
			AND rp.permission IN ('*','approvals:*','approvals:review')
		)
	)`, userID, approvalID, cfg.ApproverRoles).Scan(&eligible)
	return eligible, err
}

// RunBackground owns the PostgreSQL outbox workers, cross-instance notification
// fanout and orphan media cleanup. Cancelling ctx is a normal graceful shutdown.
func (s *Server) RunBackground(ctx context.Context) error {
	if s.repo == nil || s.outbox == nil || s.media == nil {
		return errors.New("background services require PostgreSQL")
	}
	config := event.DefaultDispatcherConfig()
	config.WorkerPrefix = secure.NewID("worker")
	dispatcher, err := event.NewDispatcher(s.outbox, event.HandlerFunc(s.handleOutboxEvent), s.metrics, slog.Default(), config)
	if err != nil {
		return err
	}
	signals := make(chan event.NotificationSignal, 256)
	group, groupContext := errgroup.WithContext(ctx)
	group.Go(func() error { return s.listenNotificationSignals(groupContext, signals) })
	group.Go(func() error { return s.publishNotificationSignals(groupContext, signals) })
	group.Go(func() error { return dispatcher.Run(groupContext) })
	group.Go(func() error { return s.cleanupOrphanMedia(groupContext) })
	group.Go(func() error { return s.runNotificationDigestWorker(groupContext) })
	group.Go(func() error { return s.runRetentionWorker(groupContext) })
	group.Go(func() error { return s.runSettingCacheWorker(groupContext) })
	return group.Wait()
}

func (s *Server) handleOutboxEvent(ctx context.Context, item event.Event) error {
	if item.Type == notificationEmailEvent {
		return s.handleNotificationEmailEvent(ctx, item)
	}
	if item.Type != notificationCreateEvent {
		return fmt.Errorf("지원하지 않는 outbox 이벤트: %s", item.Type)
	}
	var payload notificationEventPayload
	if err := json.Unmarshal(item.Payload, &payload); err != nil {
		return fmt.Errorf("notification event decode: %w", err)
	}
	payload.UserID = strings.TrimSpace(payload.UserID)
	payload.ActorID = strings.TrimSpace(payload.ActorID)
	payload.Type = strings.TrimSpace(payload.Type)
	if payload.UserID == "" || payload.Type == "" || !json.Valid(payload.Payload) {
		return errors.New("notification event fields are invalid")
	}
	if payload.Type == "approval_requested" {
		cfg, configErr := s.workflowConfigContext(ctx)
		if configErr != nil {
			return fmt.Errorf("approval notification policy: %w", configErr)
		}
		eligible, eligibilityErr := s.approvalNotificationEligible(ctx, payload.UserID, payload.TargetID, cfg)
		if eligibilityErr != nil {
			return fmt.Errorf("approval notification eligibility: %w", eligibilityErr)
		}
		if !eligible {
			return nil
		}
	}
	preferences, err := s.loadPreferencesDocument(ctx, payload.UserID)
	if err != nil {
		return fmt.Errorf("notification preferences: %w", err)
	}
	inApp := notificationInAppEnabled(preferences.Notifications, payload.Type)
	tx, err := s.repo.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	createdAt := item.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	tag, err := tx.Exec(ctx, `INSERT INTO notifications(id,user_id,actor_id,type,target_id,payload,in_app,read_at,created_at)
		SELECT $1,target.id,(SELECT id FROM users WHERE id=$3),$4,$5,$6,$7,
			CASE WHEN $7 THEN NULL ELSE $8::timestamptz END,$8
		FROM users target WHERE target.id=$2
		ON CONFLICT(id) DO NOTHING`, item.ID, payload.UserID, payload.ActorID, payload.Type, payload.TargetID, payload.Payload, inApp, createdAt)
	if err != nil {
		return fmt.Errorf("notification insert: %w", err)
	}
	if tag.RowsAffected() > 0 {
		if notificationEmailEnabled(preferences.Notifications, payload.Type) {
			emailPayload, marshalErr := json.Marshal(notificationEmailEventPayload{NotificationID: item.ID, UserID: payload.UserID})
			if marshalErr != nil {
				return fmt.Errorf("notification email event marshal: %w", marshalErr)
			}
			if _, enqueueErr := event.Enqueue(ctx, tx, event.NewEvent{
				Type: notificationEmailEvent, AggregateID: payload.UserID, Payload: emailPayload,
				IdempotencyKey: "notification:email:" + item.ID,
			}); enqueueErr != nil {
				return fmt.Errorf("notification email enqueue: %w", enqueueErr)
			}
		}
		if err := event.PublishNotificationSignal(ctx, tx, event.NotificationSignal{NotificationID: item.ID, UserID: payload.UserID}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Server) handleNotificationEmailEvent(ctx context.Context, item event.Event) error {
	var payload notificationEmailEventPayload
	if err := json.Unmarshal(item.Payload, &payload); err != nil {
		return fmt.Errorf("notification email event decode: %w", err)
	}
	payload.NotificationID = strings.TrimSpace(payload.NotificationID)
	payload.UserID = strings.TrimSpace(payload.UserID)
	if payload.NotificationID == "" || payload.UserID == "" {
		return errors.New("notification email event fields are invalid")
	}
	cfg, err := s.smtpConfigContext(ctx)
	if err != nil {
		return fmt.Errorf("SMTP settings: %w", err)
	}
	if !cfg.Enabled {
		return nil
	}
	preferences, err := s.loadPreferencesDocument(ctx, payload.UserID)
	if err != nil {
		return fmt.Errorf("notification email preferences: %w", err)
	}
	var notification model.Notification
	var recipient string
	var emailedAt *time.Time
	err = s.repo.Pool().QueryRow(ctx, `SELECT n.id,n.user_id,COALESCE(n.actor_id,''),n.type,n.target_id,n.payload,n.in_app,n.read_at,n.created_at,n.emailed_at,u.email
		FROM notifications n JOIN users u ON u.id=n.user_id
		WHERE n.id=$1 AND n.user_id=$2 AND u.active`, payload.NotificationID, payload.UserID).Scan(
		&notification.ID, &notification.UserID, &notification.ActorID, &notification.Type,
		&notification.TargetID, &notification.Payload, &notification.InApp, &notification.ReadAt,
		&notification.CreatedAt, &emailedAt, &recipient,
	)
	if errors.Is(err, pgx.ErrNoRows) || emailedAt != nil {
		return nil
	}
	if err != nil {
		return fmt.Errorf("notification email load: %w", err)
	}
	if _, recipientConfigured := bareEmailAddress(recipient); !recipientConfigured {
		return nil
	}
	if !notificationEmailEnabled(preferences.Notifications, notification.Type) {
		return nil
	}
	s.decorateNotification(ctx, &notification)
	general := defaultGeneral()
	if err := s.loadSettingContext(ctx, settingGeneral, &general); err != nil && !store.IsNotFound(err) {
		return fmt.Errorf("notification email service settings: %w", err)
	}
	normalizeGeneral(&general)
	message := notificationEmailMessage(general.ServiceName, general.PublicBaseURL, recipient, notification)
	if err := deliverSMTP(ctx, cfg, message); err != nil {
		return fmt.Errorf("notification email delivery: %w", err)
	}
	if _, err := s.repo.Pool().Exec(ctx, `UPDATE notifications SET emailed_at=now() WHERE id=$1 AND user_id=$2 AND emailed_at IS NULL`, notification.ID, notification.UserID); err != nil {
		return fmt.Errorf("notification email marker: %w", err)
	}
	return nil
}

func (s *Server) listenNotificationSignals(ctx context.Context, signals chan<- event.NotificationSignal) error {
	for ctx.Err() == nil {
		err := s.outbox.ListenNotificationSignals(ctx, signals)
		if ctx.Err() != nil {
			return nil
		}
		slog.WarnContext(ctx, "notification LISTEN 연결 복구 대기", "error", err)
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
	return nil
}

func (s *Server) publishNotificationSignals(ctx context.Context, signals <-chan event.NotificationSignal) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case signal := <-signals:
			var notification model.Notification
			err := s.repo.Pool().QueryRow(ctx, `SELECT id,user_id,COALESCE(actor_id,''),type,target_id,payload,in_app,read_at,created_at
				FROM notifications WHERE id=$1 AND user_id=$2`, signal.NotificationID, signal.UserID).Scan(
				&notification.ID, &notification.UserID, &notification.ActorID, &notification.Type,
				&notification.TargetID, &notification.Payload, &notification.InApp, &notification.ReadAt, &notification.CreatedAt,
			)
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				slog.WarnContext(ctx, "notification fanout 조회 실패", "notification_id", signal.NotificationID, "error", err)
				continue
			}
			if notification.Type == "approval_requested" {
				cfg, configErr := s.workflowConfigContext(ctx)
				eligible := false
				if configErr == nil {
					eligible, configErr = s.approvalNotificationEligible(ctx, notification.UserID, notification.TargetID, cfg)
				}
				if configErr != nil || !eligible {
					_, hideErr := s.repo.Pool().Exec(ctx, `UPDATE notifications SET in_app=false,read_at=COALESCE(read_at,now()),digested_at=COALESCE(digested_at,now()) WHERE id=$1 AND user_id=$2`, notification.ID, notification.UserID)
					if configErr != nil || hideErr != nil {
						slog.WarnContext(ctx, "승인 알림 최종 권한 검증 실패", "notification_id", notification.ID, "eligibility_error", configErr, "hide_error", hideErr)
					}
					continue
				}
			}
			s.decorateNotification(ctx, &notification)
			preferences, preferenceErr := s.loadPreferencesDocument(ctx, signal.UserID)
			if preferenceErr != nil {
				slog.WarnContext(ctx, "알림 채널 설정 조회 실패", "user_id", signal.UserID, "error", preferenceErr)
				preferences = defaultPreferencesDocument()
			}
			location := time.UTC
			general := defaultGeneral()
			if err := s.loadSettingContext(ctx, settingGeneral, &general); err == nil || store.IsNotFound(err) {
				if loaded, locationErr := time.LoadLocation(general.DefaultTimezone); locationErr == nil {
					location = loaded
				}
			}
			quiet := notificationQuietAt(preferences.Notifications.QuietHours, time.Now().In(location))
			batched := notificationBatched(preferences.Notifications, notification.Type)
			notification.Toast = preferences.Notifications.Toast.Enabled && !quiet && !batched
			notification.Desktop = preferences.Notifications.Desktop.Enabled && !quiet && !batched
			s.hub.publish(signal.UserID, notification)
		}
	}
}

func (s *Server) cleanupOrphanMedia(ctx context.Context) error {
	for {
		cfg := defaultMedia()
		if err := s.loadSettingContext(ctx, settingMedia, &cfg); err != nil && !store.IsNotFound(err) {
			slog.WarnContext(ctx, "미디어 TTL 설정 조회 실패", "error", err)
			cfg = defaultMedia()
		}
		cleaner := mediastore.Cleaner{
			Store: s.media, TTL: time.Duration(cfg.OrphanTTLHours) * time.Hour,
			Interval: time.Hour, Batch: 500, Logger: slog.Default(),
		}
		var result mediastore.CleanupResult
		var err error
		for batch := 0; batch < 20; batch++ {
			current, cleanupErr := cleaner.RunOnce(ctx, time.Now())
			result.Deleted += current.Deleted
			result.DeletedBytes += current.DeletedBytes
			if cleanupErr != nil {
				err = cleanupErr
				break
			}
			if current.Deleted < int64(cleaner.Batch) {
				break
			}
		}
		if err != nil && ctx.Err() == nil {
			slog.ErrorContext(ctx, "미사용 미디어 정리 실패", "error", err)
		} else if result.Deleted > 0 {
			slog.InfoContext(ctx, "미사용 미디어 정리 완료", "deleted", result.Deleted, "deleted_bytes", result.DeletedBytes)
		}
		if _, snapshotErr := s.repo.Pool().Exec(ctx, `DELETE FROM feed_snapshots WHERE expires_at<=now()`); snapshotErr != nil && ctx.Err() == nil {
			slog.WarnContext(ctx, "만료 Flow 스냅샷 정리 실패", "error", snapshotErr)
		}
		if _, rateErr := s.repo.Pool().Exec(ctx, `DELETE FROM rate_limit_buckets WHERE expires_at<=now()-interval '1 hour'`); rateErr != nil && ctx.Err() == nil {
			slog.WarnContext(ctx, "만료 요청 한도 Bucket 정리 실패", "error", rateErr)
		}
		timer := time.NewTimer(time.Hour)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func (s *Server) adminListOutbox(w http.ResponseWriter, r *http.Request) {
	status := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("status")))
	if status != "" && status != "dead_letter" {
		writeError(w, http.StatusBadRequest, "invalid_status", "현재는 실패 보관 이벤트만 조회할 수 있습니다")
		return
	}
	limit, _ := pagination(r)
	items, err := s.outbox.ListDeadLetters(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "실패 이벤트를 불러올 수 없습니다")
		return
	}
	views := make([]map[string]any, 0, len(items))
	for _, item := range items {
		views = append(views, map[string]any{
			"id": item.Event.ID, "type": item.Event.Type, "aggregateId": item.Event.AggregateID,
			"payload": item.Event.Payload, "attempts": item.Event.Attempts,
			"maxAttempts": item.Event.MaxAttempts, "createdAt": item.Event.CreatedAt,
			"lastError": item.LastError, "lastAttemptAt": item.LastAttemptAt,
			"deadLetteredAt": item.DeadLetteredAt,
		})
	}
	writeData(w, http.StatusOK, map[string]any{"items": views, "status": "dead_letter", "limit": limit})
}

func (s *Server) adminRetryOutbox(w http.ResponseWriter, r *http.Request) {
	eventID := strings.TrimSpace(chi.URLParam(r, "eventID"))
	if eventID == "" || len(eventID) > 160 {
		writeError(w, http.StatusBadRequest, "invalid_event_id", "이벤트 ID가 올바르지 않습니다")
		return
	}
	if err := s.outbox.RetryDeadLetter(r.Context(), eventID); err != nil {
		if errors.Is(err, event.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "재처리할 실패 이벤트를 찾을 수 없습니다")
			return
		}
		writeError(w, http.StatusInternalServerError, "storage_error", "실패 이벤트를 재처리할 수 없습니다")
		return
	}
	s.audit(r, "outbox.retry", "outbox_event", eventID, true, nil)
	writeData(w, http.StatusAccepted, map[string]any{"id": eventID, "status": "pending"})
}
