package media

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

type OrphanStore interface {
	DeleteOrphans(context.Context, time.Time, int) (CleanupResult, error)
}

type Cleaner struct {
	Store    OrphanStore
	TTL      time.Duration
	Interval time.Duration
	Batch    int
	Logger   *slog.Logger
}

func (cleaner *Cleaner) Validate() error {
	if cleaner == nil || cleaner.Store == nil {
		return errors.New("media orphan store is required")
	}
	if cleaner.TTL <= 0 || cleaner.Interval <= 0 {
		return errors.New("media orphan TTL and interval must be positive")
	}
	if cleaner.Batch < 1 || cleaner.Batch > 10000 {
		return errors.New("media orphan batch must be between 1 and 10000")
	}
	return nil
}

func (cleaner *Cleaner) RunOnce(ctx context.Context, now time.Time) (CleanupResult, error) {
	if err := cleaner.Validate(); err != nil {
		return CleanupResult{}, err
	}
	return cleaner.Store.DeleteOrphans(ctx, now.UTC().Add(-cleaner.TTL), cleaner.Batch)
}

// Run performs one cleanup at startup and then on each interval. SKIP LOCKED in
// the PostgreSQL adapter makes concurrent service instances safe.
func (cleaner *Cleaner) Run(ctx context.Context) error {
	if err := cleaner.Validate(); err != nil {
		return err
	}
	logger := cleaner.Logger
	if logger == nil {
		logger = slog.Default()
	}
	for {
		result, err := cleaner.RunOnce(ctx, time.Now())
		if err != nil && ctx.Err() == nil {
			logger.ErrorContext(ctx, "미디어 orphan 정리 실패", "error", err)
		} else if result.Deleted > 0 {
			logger.InfoContext(ctx, "미디어 orphan 정리 완료", "deleted", result.Deleted, "deleted_bytes", result.DeletedBytes)
		}
		timer := time.NewTimer(cleaner.Interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}
