package worker_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/torikh42/treean/internal/repository"
	"github.com/torikh42/treean/internal/worker"
)

var _ worker.ExpiryStore = (*repository.OrderRepo)(nil)

type sweepCall struct {
	eventID     *uuid.UUID
	limit       int
	actorType   string
	now         time.Time
	hasDeadline bool
}

type mockExpiryStore struct {
	mu     sync.Mutex
	calls  []sweepCall
	count  atomic.Int32
	result int
	err    error
	hook   func(ctx context.Context)
}

func (m *mockExpiryStore) ExpireDueOrders(
	ctx context.Context,
	eventID *uuid.UUID,
	limit int,
	actorType string,
	now time.Time,
) (int, error) {
	if m.hook != nil {
		m.hook(ctx)
	}

	var eventCopy *uuid.UUID
	if eventID != nil {
		id := *eventID
		eventCopy = &id
	}
	_, hasDeadline := ctx.Deadline()

	m.mu.Lock()
	m.calls = append(m.calls, sweepCall{
		eventID:     eventCopy,
		limit:       limit,
		actorType:   actorType,
		now:         now,
		hasDeadline: hasDeadline,
	})
	res := m.result
	err := m.err
	m.mu.Unlock()

	m.count.Add(1)
	return res, err
}

func (m *mockExpiryStore) snapshotCalls() []sweepCall {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]sweepCall, len(m.calls))
	copy(out, m.calls)
	return out
}

type logRecord struct {
	Level        string `json:"level"`
	Msg          string `json:"msg"`
	ExpiredCount int    `json:"expired_count"`
	Error        string `json:"error"`
}

func decodeLogRecord(t *testing.T, raw []byte) logRecord {
	t.Helper()

	var rec logRecord
	if err := json.Unmarshal(bytes.TrimSpace(raw), &rec); err != nil {
		t.Fatalf("failed to decode log JSON %q: %v", string(raw), err)
	}
	return rec
}

func assertSweepCallArgs(t *testing.T, call sweepCall, wantLimit int) {
	t.Helper()

	if call.eventID != nil {
		t.Fatalf("eventID = %v, want nil", call.eventID)
	}
	if call.limit != wantLimit {
		t.Fatalf("limit = %d, want %d", call.limit, wantLimit)
	}
	if call.actorType != "scheduler" {
		t.Fatalf("actorType = %q, want %q", call.actorType, "scheduler")
	}
	if call.now.IsZero() || call.now.Location() != time.UTC {
		t.Fatalf("now = %v (loc=%v), want non-zero UTC", call.now, call.now.Location())
	}
	if !call.hasDeadline {
		t.Fatal("expected bounded sweep context with deadline")
	}
}

func assertStartStopsOnCancel(t *testing.T, sweeper *worker.ExpirySweeper) {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		sweeper.Start(ctx)
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("sweeper failed to exit after context cancellation")
	}
}

func TestExpirySweeper_StopsGracefullyOnCancel(t *testing.T) {
	t.Parallel()

	ticked := make(chan struct{}, 1)
	store := &mockExpiryStore{
		result: 1,
		hook: func(context.Context) {
			select {
			case ticked <- struct{}{}:
			default:
			}
		},
	}
	logger := slog.New(slog.DiscardHandler)
	sweeper := worker.NewExpirySweeper(store, logger, 10*time.Millisecond, 50)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		sweeper.Start(ctx)
		close(done)
	}()

	select {
	case <-ticked:
	case <-time.After(500 * time.Millisecond):
		cancel()
		t.Fatal("sweeper did not tick within 500ms")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("sweeper failed to exit after context cancellation")
	}

	calls := store.snapshotCalls()
	if len(calls) == 0 {
		t.Fatal("expected at least one ExpireDueOrders call")
	}
	assertSweepCallArgs(t, calls[0], 50)
}

func TestExpirySweeper_RunOnceLogsResultsAndErrors(t *testing.T) {
	t.Parallel()

	t.Run("logs expired_count when expired orders exceed zero", func(t *testing.T) {
		t.Parallel()

		var buf bytes.Buffer
		store := &mockExpiryStore{result: 3}
		logger := slog.New(slog.NewJSONHandler(&buf, nil))
		sweeper := worker.NewExpirySweeper(store, logger, time.Second, 25)

		sweeper.RunOnce(t.Context())

		calls := store.snapshotCalls()
		if len(calls) != 1 {
			t.Fatalf("call count = %d, want 1", len(calls))
		}
		assertSweepCallArgs(t, calls[0], 25)

		rec := decodeLogRecord(t, buf.Bytes())
		if rec.Level != "INFO" || rec.Msg != "expired unpaid orders and restored quota" || rec.ExpiredCount != 3 {
			t.Fatalf("unexpected log record: %+v", rec)
		}
	})

	t.Run("does not log info when zero orders expired", func(t *testing.T) {
		t.Parallel()

		var buf bytes.Buffer
		store := &mockExpiryStore{result: 0}
		logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
		sweeper := worker.NewExpirySweeper(store, logger, time.Second, 50)

		sweeper.RunOnce(t.Context())

		if buf.Len() != 0 {
			t.Fatalf("expected no log output when zero expired, got %s", buf.String())
		}
	})

	t.Run("logs error on store failure without panicking", func(t *testing.T) {
		t.Parallel()

		var buf bytes.Buffer
		store := &mockExpiryStore{err: errors.New("db connection lost")}
		logger := slog.New(slog.NewJSONHandler(&buf, nil))
		sweeper := worker.NewExpirySweeper(store, logger, time.Second, 50)

		sweeper.RunOnce(t.Context())

		rec := decodeLogRecord(t, buf.Bytes())
		if rec.Level != "ERROR" || rec.Msg != "expiry sweep failed" || rec.Error != "db connection lost" {
			t.Fatalf("unexpected error log record: %+v", rec)
		}
	})

	t.Run("avoids error log spam on cancellation", func(t *testing.T) {
		t.Parallel()

		var buf bytes.Buffer
		ctx, cancel := context.WithCancel(t.Context())
		store := &mockExpiryStore{
			err: context.Canceled,
			hook: func(context.Context) {
				cancel()
			},
		}
		logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
		sweeper := worker.NewExpirySweeper(store, logger, time.Second, 50)

		sweeper.RunOnce(ctx)

		raw := buf.String()
		if strings.Contains(raw, `"level":"ERROR"`) {
			t.Fatalf("expected no ERROR log on cancellation, got %s", raw)
		}
		if raw != "" {
			rec := decodeLogRecord(t, buf.Bytes())
			if rec.Level != "DEBUG" {
				t.Fatalf("expected DEBUG level on cancellation, got %+v", rec)
			}
		}
	})
}

func TestExpirySweeper_DefensiveGuardsAndDefaults(t *testing.T) {
	t.Parallel()

	t.Run("defaults non-positive interval batchSize and nil logger", func(t *testing.T) {
		t.Parallel()

		cases := []struct {
			name      string
			interval  time.Duration
			batchSize int
		}{
			{name: "zero interval and batch size", interval: 0, batchSize: 0},
			{name: "negative interval and batch size", interval: -time.Second, batchSize: -10},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				store := &mockExpiryStore{result: 2}
				sweeper := worker.NewExpirySweeper(store, nil, tc.interval, tc.batchSize)

				sweeper.RunOnce(t.Context())
				assertStartStopsOnCancel(t, sweeper)

				calls := store.snapshotCalls()
				if len(calls) != 1 {
					t.Fatalf("call count = %d, want 1", len(calls))
				}
				assertSweepCallArgs(t, calls[0], 50)

				errStore := &mockExpiryStore{err: errors.New("boom")}
				worker.NewExpirySweeper(errStore, nil, tc.interval, tc.batchSize).RunOnce(t.Context())
				if errStore.count.Load() != 1 {
					t.Fatalf("errStore call count = %d, want 1", errStore.count.Load())
				}
			})
		}
	})

	t.Run("nil receiver is safe", func(t *testing.T) {
		t.Parallel()

		var nilSweeper *worker.ExpirySweeper
		nilSweeper.Start(t.Context())
		nilSweeper.RunOnce(t.Context())
	})

	t.Run("zero value struct is safe", func(t *testing.T) {
		t.Parallel()

		zeroSweeper := &worker.ExpirySweeper{}
		zeroSweeper.Start(t.Context())
		zeroSweeper.RunOnce(t.Context())
	})

	t.Run("nil store is safe", func(t *testing.T) {
		t.Parallel()

		logger := slog.New(slog.DiscardHandler)
		sweeper := worker.NewExpirySweeper(nil, logger, 10*time.Millisecond, 10)
		sweeper.Start(t.Context())
		sweeper.RunOnce(t.Context())
	})

	t.Run("nil context never invokes store", func(t *testing.T) {
		t.Parallel()

		var nilCtx context.Context
		store := &mockExpiryStore{}
		sweeper := worker.NewExpirySweeper(store, nil, 10*time.Millisecond, 10)

		sweeper.Start(nilCtx)
		sweeper.RunOnce(nilCtx)

		if store.count.Load() != 0 {
			t.Fatalf("store calls = %d, want 0", store.count.Load())
		}
	})

	t.Run("pre-cancelled context never invokes store", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		store := &mockExpiryStore{}
		sweeper := worker.NewExpirySweeper(store, nil, 10*time.Millisecond, 10)

		sweeper.Start(ctx)
		sweeper.RunOnce(ctx)

		if store.count.Load() != 0 {
			t.Fatalf("store calls = %d, want 0", store.count.Load())
		}
	})
}

type eventAwareExpiryMock struct {
	*mockExpiryStore
	touchedEvents []uuid.UUID
}

func (m *eventAwareExpiryMock) ExpireDueOrdersWithEvents(
	ctx context.Context,
	eventID *uuid.UUID,
	limit int,
	actorType string,
	now time.Time,
) (int, []uuid.UUID, error) {
	count, err := m.ExpireDueOrders(ctx, eventID, limit, actorType, now)
	return count, m.touchedEvents, err
}

type spyEventCacheInvalidator struct {
	mu    sync.Mutex
	calls [][]uuid.UUID
}

func (s *spyEventCacheInvalidator) InvalidateEvents(_ context.Context, eventIDs ...uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]uuid.UUID, len(eventIDs))
	copy(cp, eventIDs)
	s.calls = append(s.calls, cp)
}

func TestExpirySweeper_InvalidatesEventCacheOnExpiredOrders(t *testing.T) {
	t.Parallel()

	evA := uuid.New()
	evB := uuid.New()
	cache := &spyEventCacheInvalidator{}

	storeZero := &eventAwareExpiryMock{mockExpiryStore: &mockExpiryStore{result: 0}}
	worker.NewExpirySweeper(storeZero, nil, time.Second, 10).WithCache(cache).RunOnce(t.Context())
	if len(cache.calls) != 0 {
		t.Fatalf("expected 0 invalidations when expired=0, got %v", cache.calls)
	}

	storeHit := &eventAwareExpiryMock{
		mockExpiryStore: &mockExpiryStore{result: 2},
		touchedEvents:   []uuid.UUID{evA, evB},
	}
	worker.NewExpirySweeper(storeHit, nil, time.Second, 10).WithCache(cache).RunOnce(t.Context())
	if len(cache.calls) != 1 || len(cache.calls[0]) != 2 || cache.calls[0][0] != evA || cache.calls[0][1] != evB {
		t.Fatalf("unexpected invalidated event IDs: %+v", cache.calls)
	}
}

type multiBatchExpiryMock struct {
	mu      sync.Mutex
	batches []struct {
		expired int
		events  []uuid.UUID
	}
	idx int
}

func (m *multiBatchExpiryMock) ExpireDueOrders(context.Context, *uuid.UUID, int, string, time.Time) (int, error) {
	return 0, nil
}

func (m *multiBatchExpiryMock) ExpireDueOrdersWithEvents(_ context.Context, _ *uuid.UUID, _ int, _ string, _ time.Time) (int, []uuid.UUID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.idx >= len(m.batches) {
		return 0, nil, nil
	}
	b := m.batches[m.idx]
	m.idx++
	return b.expired, b.events, nil
}

func TestExpirySweeper_DrainsMultipleFullBatchesInSingleTick(t *testing.T) {
	t.Parallel()

	evA := uuid.New()
	evB := uuid.New()
	evC := uuid.New()
	store := &multiBatchExpiryMock{
		batches: []struct {
			expired int
			events  []uuid.UUID
		}{
			{expired: 10, events: []uuid.UUID{evA, evB}},
			{expired: 10, events: []uuid.UUID{evB, evC}},
			{expired: 4, events: []uuid.UUID{evA}},
		},
	}
	cache := &spyEventCacheInvalidator{}
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	worker.NewExpirySweeper(store, logger, time.Second, 10).WithCache(cache).RunOnce(t.Context())

	if store.idx != 3 {
		t.Fatalf("batch calls = %d, want 3", store.idx)
	}
	if len(cache.calls) != 1 || len(cache.calls[0]) != 3 {
		t.Fatalf("expected 1 deduplicated InvalidateEvents call with 3 events, got %+v", cache.calls)
	}
	rec := decodeLogRecord(t, buf.Bytes())
	if rec.ExpiredCount != 24 {
		t.Fatalf("logged expired_count = %d, want 24", rec.ExpiredCount)
	}
}

func TestExpirySweeper_StartSweepsImmediatelyOnStartup(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	ran := make(chan struct{}, 1)
	store := &mockExpiryStore{
		hook: func(context.Context) {
			select {
			case ran <- struct{}{}:
			default:
			}
			cancel()
		},
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.NewExpirySweeper(store, nil, time.Hour, 10).Start(ctx)
	}()

	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("expected Start to execute an immediate initial sweep before first 1h tick")
	}
	<-done
}
