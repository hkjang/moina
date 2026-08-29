package event

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

type Handler interface {
	Handle(context.Context, Event) error
}

type HandlerFunc func(context.Context, Event) error

func (fn HandlerFunc) Handle(ctx context.Context, item Event) error { return fn(ctx, item) }

type Queue interface {
	Claim(context.Context, string, int, time.Duration) ([]Event, error)
	MarkDelivered(context.Context, string, string) error
	MarkFailed(context.Context, Event, string, error, time.Duration, time.Duration) (bool, error)
	RetryDeadLetter(context.Context, string) error
	Stats(context.Context) (Stats, error)
	Listen(context.Context, chan<- struct{}) error
}

type Observer interface {
	SetOutboxLag(time.Duration)
	IncOutboxFailures()
}

type DispatcherConfig struct {
	WorkerCount    int
	ClaimBatch     int
	Lease          time.Duration
	PollInterval   time.Duration
	BaseBackoff    time.Duration
	MaxBackoff     time.Duration
	HandlerTimeout time.Duration
	WorkerPrefix   string
}

func DefaultDispatcherConfig() DispatcherConfig {
	return DispatcherConfig{
		WorkerCount:    4,
		ClaimBatch:     1,
		Lease:          2 * time.Minute,
		PollInterval:   5 * time.Second,
		BaseBackoff:    time.Second,
		MaxBackoff:     15 * time.Minute,
		HandlerTimeout: 90 * time.Second,
		WorkerPrefix:   "moina",
	}
}

type Dispatcher struct {
	queue    Queue
	handler  Handler
	observer Observer
	logger   *slog.Logger
	config   DispatcherConfig
}

func NewDispatcher(queue Queue, handler Handler, observer Observer, logger *slog.Logger, config DispatcherConfig) (*Dispatcher, error) {
	if queue == nil || handler == nil {
		return nil, errors.New("outbox queue and handler are required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if err := validateDispatcherConfig(config); err != nil {
		return nil, err
	}
	return &Dispatcher{queue: queue, handler: handler, observer: observer, logger: logger, config: config}, nil
}

func validateDispatcherConfig(config DispatcherConfig) error {
	if config.WorkerCount < 1 || config.WorkerCount > 128 {
		return errors.New("outbox worker count must be between 1 and 128")
	}
	if config.ClaimBatch < 1 || config.ClaimBatch > 1000 {
		return errors.New("outbox claim batch must be between 1 and 1000")
	}
	if config.Lease <= 0 || config.PollInterval <= 0 || config.BaseBackoff <= 0 || config.MaxBackoff < config.BaseBackoff || config.HandlerTimeout <= 0 {
		return errors.New("outbox durations must be positive and max backoff must not be below base backoff")
	}
	if config.HandlerTimeout > time.Duration((1<<63-1)/int64(config.ClaimBatch)) || config.Lease < config.HandlerTimeout*time.Duration(config.ClaimBatch) {
		return errors.New("outbox lease must cover the sequential handler timeout for a claimed batch")
	}
	if config.WorkerPrefix == "" {
		return errors.New("outbox worker prefix is required")
	}
	return nil
}

// Run starts a single LISTEN loop and the configured number of SKIP LOCKED
// workers. Context cancellation is a normal shutdown and returns nil.
func (d *Dispatcher) Run(ctx context.Context) error {
	wake := make(chan struct{}, 1)
	var wait sync.WaitGroup
	wait.Add(1)
	go func() {
		defer wait.Done()
		d.listenLoop(ctx, wake)
	}()
	for index := 0; index < d.config.WorkerCount; index++ {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			d.work(ctx, fmt.Sprintf("%s-%02d", d.config.WorkerPrefix, worker+1), wake)
		}(index)
	}
	<-ctx.Done()
	wait.Wait()
	return nil
}

func (d *Dispatcher) listenLoop(ctx context.Context, wake chan<- struct{}) {
	for ctx.Err() == nil {
		err := d.queue.Listen(ctx, wake)
		if ctx.Err() != nil {
			return
		}
		d.logger.WarnContext(ctx, "outbox LISTEN 연결 복구 대기", "error", err)
		timer := time.NewTimer(d.config.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (d *Dispatcher) work(ctx context.Context, workerID string, wake <-chan struct{}) {
	for ctx.Err() == nil {
		items, err := d.queue.Claim(ctx, workerID, d.config.ClaimBatch, d.config.Lease)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			d.logger.ErrorContext(ctx, "outbox claim 실패", "worker_id", workerID, "error", err)
			d.wait(ctx, wake)
			continue
		}
		if len(items) == 0 {
			d.observe(ctx)
			d.wait(ctx, wake)
			continue
		}
		for _, item := range items {
			if ctx.Err() != nil {
				return
			}
			handleCtx, cancel := context.WithTimeout(ctx, d.config.HandlerTimeout)
			handleErr := d.handler.Handle(handleCtx, item)
			cancel()
			if ctx.Err() != nil {
				return
			}
			if handleErr == nil {
				if err := d.queue.MarkDelivered(ctx, item.ID, workerID); err != nil && ctx.Err() == nil {
					d.logger.ErrorContext(ctx, "outbox 완료 기록 실패", "event_id", item.ID, "worker_id", workerID, "error", err)
				}
				continue
			}
			if d.observer != nil {
				d.observer.IncOutboxFailures()
			}
			dead, err := d.queue.MarkFailed(ctx, item, workerID, handleErr, d.config.BaseBackoff, d.config.MaxBackoff)
			if err != nil {
				d.logger.ErrorContext(ctx, "outbox 실패 기록 실패", "event_id", item.ID, "worker_id", workerID, "error", err)
				continue
			}
			d.logger.WarnContext(ctx, "outbox handler 실패", "event_id", item.ID, "event_type", item.Type, "attempts", item.Attempts, "dead_letter", dead, "error", handleErr)
		}
		d.observe(ctx)
	}
}

func (d *Dispatcher) wait(ctx context.Context, wake <-chan struct{}) {
	timer := time.NewTimer(d.config.PollInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-wake:
	case <-timer.C:
	}
}

func (d *Dispatcher) observe(ctx context.Context) {
	if d.observer == nil {
		return
	}
	stats, err := d.queue.Stats(ctx)
	if err != nil {
		if ctx.Err() == nil {
			d.logger.DebugContext(ctx, "outbox 지표 조회 실패", "error", err)
		}
		return
	}
	d.observer.SetOutboxLag(stats.Lag)
}

var _ Queue = (*Repository)(nil)
