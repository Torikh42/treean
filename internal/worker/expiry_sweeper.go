// Package worker executes bounded background tasks tied to application context lifecycles.
package worker

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

const (
	defaultSweepInterval  = 5 * time.Second
	defaultSweepBatchSize = 50
	defaultSweepTimeout   = 10 * time.Second
	schedulerActorType    = "scheduler"
)

// ExpiryStore exposes batch expiration required by ExpirySweeper.
type ExpiryStore interface {
	ExpireDueOrders(ctx context.Context, eventID *uuid.UUID, limit int, actorType string, now time.Time) (int, error)
}

// EventAwareExpiryStore extends ExpiryStore with touched event IDs for cache invalidation.
type EventAwareExpiryStore interface {
	ExpireDueOrdersWithEvents(ctx context.Context, eventID *uuid.UUID, limit int, actorType string, now time.Time) (int, []uuid.UUID, error)
}

// EventCacheInvalidator clears stale public event caches after background expiry sweeps.
type EventCacheInvalidator interface {
	InvalidateEvents(ctx context.Context, eventIDs ...uuid.UUID)
}

// ExpirySweeper periodically releases overdue WAITING_PAYMENT quota holds without blocking HTTP traffic.
type ExpirySweeper struct {
	store     ExpiryStore
	cache     EventCacheInvalidator
	logger    *slog.Logger
	interval  time.Duration
	batchSize int
}

// WithCache attaches an optional EventCacheInvalidator cleared when sweeps transition overdue orders.
func (w *ExpirySweeper) WithCache(cache EventCacheInvalidator) *ExpirySweeper {
	if w != nil {
		w.cache = cache
	}
	return w
}

// NewExpirySweeper constructs an ExpirySweeper with safe defaults for interval, batch size, and logger.
func NewExpirySweeper(store ExpiryStore, logger *slog.Logger, interval time.Duration, batchSize int) *ExpirySweeper {
	if interval <= 0 {
		interval = defaultSweepInterval
	}
	if batchSize <= 0 {
		batchSize = defaultSweepBatchSize
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &ExpirySweeper{
		store:     store,
		logger:    logger,
		interval:  interval,
		batchSize: batchSize,
	}
}

// Start runs the sweep ticker until ctx is cancelled, preventing goroutine leaks.
func (w *ExpirySweeper) Start(ctx context.Context) {
	if !w.canRun(ctx) {
		return
	}

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if ctx.Err() != nil {
				return
			}
			w.RunOnce(ctx)
		}
	}
}

// RunOnce executes one bounded sweep cycle with a timeout context.
func (w *ExpirySweeper) RunOnce(ctx context.Context) {
	if !w.canRun(ctx) {
		return
	}

	sweepCtx, cancel := context.WithTimeout(ctx, defaultSweepTimeout)
	defer cancel()

	nowUTC := time.Now().UTC()
	var (
		expired       int
		touchedEvents []uuid.UUID
		err           error
	)
	if aware, ok := w.store.(EventAwareExpiryStore); ok {
		expired, touchedEvents, err = aware.ExpireDueOrdersWithEvents(sweepCtx, nil, w.batchSize, schedulerActorType, nowUTC)
	} else {
		expired, err = w.store.ExpireDueOrders(sweepCtx, nil, w.batchSize, schedulerActorType, nowUTC)
	}
	if err != nil {
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			w.logger.DebugContext(sweepCtx, "expiry sweep cancelled", slog.String("error", err.Error()))
			return
		}
		w.logger.ErrorContext(sweepCtx, "expiry sweep failed", slog.String("error", err.Error()))
		return
	}
	if expired > 0 {
		w.logger.InfoContext(sweepCtx, "expired unpaid orders and restored quota", slog.Int("expired_count", expired))
		if w.cache != nil {
			w.cache.InvalidateEvents(sweepCtx, touchedEvents...)
		}
	}
}

func (w *ExpirySweeper) canRun(ctx context.Context) bool {
	return w != nil && w.store != nil && ctx != nil && ctx.Err() == nil
}
