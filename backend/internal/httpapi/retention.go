package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/hkjang/moina/backend/internal/store"
)

// retentionLock keeps one instance at a time sweeping. A concurrent sweep is
// not incorrect, only wasted work on rows another instance already deleted.
const retentionLock int64 = 1297042031

const (
	// A sweep deletes in bounded batches so an install that has never purged
	// does not take one long lock over a large table. Anything left over is
	// picked up by the next hourly pass.
	retentionBatch      = 5000
	retentionMaxBatches = 20
	retentionMaxDays    = 3650
)

// retentionConfig bounds the tables that grow with traffic rather than with the
// size of the service. Zero disables a sweep and keeps rows indefinitely.
type retentionConfig struct {
	AuditDays        int `json:"auditDays"`
	NotificationDays int `json:"notificationDays"`
	OutboxDays       int `json:"outboxDays"`
	AIUsageDays      int `json:"aiUsageDays"`
}

// defaultRetention prunes the operational churn and keeps the audit trail.
// Audit events are the compliance record an operator is most likely to be asked
// for after the fact, so an upgrade must never start deleting them on its own;
// an administrator opts in by setting auditDays.
func defaultRetention() retentionConfig {
	return retentionConfig{AuditDays: 0, NotificationDays: 90, OutboxDays: 14, AIUsageDays: 180}
}

func validateRetention(cfg retentionConfig) error {
	for _, days := range []int{cfg.AuditDays, cfg.NotificationDays, cfg.OutboxDays, cfg.AIUsageDays} {
		if days < 0 || days > retentionMaxDays {
			return errors.New("보존 기간은 0일(무기한)부터 3650일 사이여야 합니다")
		}
	}
	return nil
}

func (s *Server) retentionSettingsContext(ctx context.Context) (retentionConfig, error) {
	cfg := defaultRetention()
	if err := s.loadSettingContext(ctx, settingRetention, &cfg); err != nil && !store.IsNotFound(err) {
		return retentionConfig{}, err
	}
	return cfg, validateRetention(cfg)
}

// retentionSweep is one table's purge. The statement takes the cutoff as $1 and
// the batch size as $2 and returns nothing; the tag's row count drives batching.
type retentionSweep struct {
	name  string
	days  int
	query string
}

func retentionSweeps(cfg retentionConfig) []retentionSweep {
	return []retentionSweep{
		{"audit_events", cfg.AuditDays,
			`DELETE FROM audit_events WHERE id IN (SELECT id FROM audit_events WHERE created_at<$1 LIMIT $2)`},
		{"notifications", cfg.NotificationDays,
			`DELETE FROM notifications WHERE id IN (SELECT id FROM notifications WHERE created_at<$1 LIMIT $2)`},
		// Only delivered events are disposable. A dead letter stays until an
		// administrator retries or the delivery is written off, so the sweep
		// must not confuse "old" with "done".
		{"outbox_events", cfg.OutboxDays,
			`DELETE FROM outbox_events WHERE id IN (SELECT id FROM outbox_events WHERE delivered_at IS NOT NULL AND delivered_at<$1 LIMIT $2)`},
		{"ai_usage_events", cfg.AIUsageDays,
			`DELETE FROM ai_usage_events WHERE id IN (SELECT id FROM ai_usage_events WHERE created_at<$1 LIMIT $2)`},
	}
}

// purgeExpiredRecords removes expired sessions and then applies each configured
// retention window. Sessions are unconditional: an expired session already
// fails authentication, so the row is dead weight whatever the policy says.
func (s *Server) purgeExpiredRecords(ctx context.Context, now time.Time) error {
	locked, unlock, err := s.acquireRetentionLock(ctx)
	if err != nil || !locked {
		return err
	}
	defer unlock()

	if err := s.repo.DeleteExpiredSessions(ctx); err != nil {
		return fmt.Errorf("만료 세션 정리: %w", err)
	}
	cfg, err := s.retentionSettingsContext(ctx)
	if err != nil {
		return fmt.Errorf("보존 정책 조회: %w", err)
	}
	for _, sweep := range retentionSweeps(cfg) {
		if sweep.days <= 0 {
			continue
		}
		cutoff := now.UTC().AddDate(0, 0, -sweep.days)
		deleted, err := s.purgeInBatches(ctx, sweep.query, cutoff)
		if err != nil {
			return fmt.Errorf("%s 보존 정리: %w", sweep.name, err)
		}
		if deleted > 0 {
			slog.InfoContext(ctx, "보존 기간 초과 레코드 정리", "table", sweep.name, "deleted", deleted, "days", sweep.days)
		}
	}
	return nil
}

func (s *Server) purgeInBatches(ctx context.Context, query string, cutoff time.Time) (int64, error) {
	var deleted int64
	for batch := 0; batch < retentionMaxBatches; batch++ {
		tag, err := s.repo.Pool().Exec(ctx, query, cutoff, retentionBatch)
		if err != nil {
			return deleted, err
		}
		deleted += tag.RowsAffected()
		if tag.RowsAffected() < retentionBatch {
			return deleted, nil
		}
	}
	return deleted, nil
}

// acquireRetentionLock holds a session level advisory lock for the duration of
// the sweep. A transaction scoped lock would not survive the separate batched
// statements, which are deliberately not one transaction.
func (s *Server) acquireRetentionLock(ctx context.Context) (bool, func(), error) {
	connection, err := s.repo.Pool().Acquire(ctx)
	if err != nil {
		return false, nil, err
	}
	var locked bool
	if err := connection.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, retentionLock).Scan(&locked); err != nil {
		connection.Release()
		return false, nil, err
	}
	if !locked {
		connection.Release()
		return false, nil, nil
	}
	return true, func() {
		unlockContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, err := connection.Exec(unlockContext, `SELECT pg_advisory_unlock($1)`, retentionLock); err != nil {
			slog.WarnContext(ctx, "보존 정리 advisory lock 해제 실패", "error", err)
		}
		connection.Release()
	}, nil
}

// runRetentionWorker sweeps once at startup so an upgrade immediately drops the
// rows earlier releases had no way to remove, then hourly.
func (s *Server) runRetentionWorker(ctx context.Context) error {
	for {
		if err := s.purgeExpiredRecords(ctx, time.Now()); err != nil && ctx.Err() == nil {
			slog.WarnContext(ctx, "보존 기간 정리 실패", "error", err)
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
