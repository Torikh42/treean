package service_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/torikh42/treean/internal/domain"
	"github.com/torikh42/treean/internal/middleware"
	"github.com/torikh42/treean/internal/repository"
	"github.com/torikh42/treean/internal/service"
)

type stubRateLimiter struct {
	allow bool
	wait  time.Duration
	err   error
	keys  []string
}

func (s *stubRateLimiter) Allow(_ context.Context, key string, _ int, _ time.Duration) (bool, time.Duration, error) {
	s.keys = append(s.keys, key)
	return s.allow, s.wait, s.err
}

type stubEventCache struct {
	mu          sync.Mutex
	catalog     []domain.Event
	hasCatalog  bool
	details     map[uuid.UUID]*domain.Event
	invalidated []uuid.UUID
	invCalls    int
}

func (c *stubEventCache) GetEventsCatalog(context.Context) ([]domain.Event, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.hasCatalog {
		return nil, false
	}
	return c.catalog, true
}

func (c *stubEventCache) SetEventsCatalog(_ context.Context, events []domain.Event, _ time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.catalog = events
	c.hasCatalog = true
}

func (c *stubEventCache) GetEventDetail(_ context.Context, eventID uuid.UUID) (*domain.Event, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.details == nil {
		return nil, false
	}
	ev, ok := c.details[eventID]
	return ev, ok
}

func (c *stubEventCache) SetEventDetail(_ context.Context, ev *domain.Event, _ time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.details == nil {
		c.details = make(map[uuid.UUID]*domain.Event)
	}
	c.details[ev.ID] = ev
}

func (c *stubEventCache) InvalidateEvents(_ context.Context, eventIDs ...uuid.UUID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.invCalls++
	c.hasCatalog = false
	for _, id := range eventIDs {
		delete(c.details, id)
		c.invalidated = append(c.invalidated, id)
	}
}

type idempotentOrderMock struct {
	*mockOrderRepo
	lastKey      string
	replayResult *repository.CreateOrderResult
	touchedEvs   []uuid.UUID
	failEventID  uuid.UUID
}

func (m *idempotentOrderMock) CreateOrderIdempotent(
	_ context.Context,
	eventID, buyerID uuid.UUID,
	items []repository.CheckoutItemInput,
	key string,
	_ time.Time,
) (*repository.CreateOrderResult, error) {
	m.lastKey = key
	if m.replayResult != nil {
		return m.replayResult, nil
	}
	return &repository.CreateOrderResult{
		Order: &domain.Order{ID: uuid.New(), EventID: eventID, BuyerID: buyerID, TotalTickets: items[0].Quantity},
	}, nil
}

func (m *idempotentOrderMock) ExpireDueOrdersWithEvents(context.Context, *uuid.UUID, int, string, time.Time) (int, []uuid.UUID, error) {
	return len(m.touchedEvs), m.touchedEvs, nil
}

func (m *idempotentOrderMock) FailPaymentWithEvent(context.Context, uuid.UUID, string, time.Time) (domain.WebhookOutcome, uuid.UUID, error) {
	return domain.OutcomeProcessedFailed, m.failEventID, nil
}

func TestAuthService_LoginAccountRateLimitAndFailOpen(t *testing.T) {
	t.Parallel()

	ctxCh := make(chan context.Context, 1)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/auth/login", nil)
	req.RemoteAddr = "203.0.113.10:12345"
	middleware.WithClientIP(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		ctxCh <- r.Context()
	})).ServeHTTP(httptest.NewRecorder(), req)
	capturedCtx := <-ctxCh

	limiter := &stubRateLimiter{allow: false, wait: 15 * time.Second}
	blockedSvc := service.NewAuthService(newMockUserRepo(), "jwt-secret").
		WithLoginRateLimiter(limiter, 5)

	_, err := blockedSvc.Login(capturedCtx, "User@Example.com", "secret123")
	de, ok := errors.AsType[*domain.DomainError](err)
	if !ok || de.Code != domain.ErrCodeRateLimited || de.HTTPStatus != http.StatusTooManyRequests {
		t.Fatalf("expected 429 RATE_LIMITED, got err=%v", err)
	}
	if de.RetryAfterSeconds != 15 {
		t.Fatalf("RetryAfterSeconds = %d, want 15", de.RetryAfterSeconds)
	}
	if len(limiter.keys) != 1 || !strings.HasPrefix(limiter.keys[0], "rl:login:acct:203.0.113.10:") {
		t.Fatalf("expected account bucket keyed by client IP + email hash, got %v", limiter.keys)
	}

	failOpenSvc := service.NewAuthService(newMockUserRepo(), "jwt-secret").
		WithLoginRateLimiter(&stubRateLimiter{err: errors.New("redis timeout")}, 5)
	_, err = failOpenSvc.Login(capturedCtx, "missing@example.com", "secret123")
	de2, ok2 := errors.AsType[*domain.DomainError](err)
	if !ok2 || de2.Code != domain.ErrCodeInvalidCredentials {
		t.Fatalf("expected fail-open credential check (INVALID_CREDENTIALS), got err=%v", err)
	}
}

func TestOrderService_IdempotencyKeyValidationAndReplayCache(t *testing.T) {
	t.Parallel()

	buyer := domain.Actor{ID: uuid.New(), Role: domain.RoleBuyer}
	eventID := uuid.New()
	tierID := uuid.New()
	cache := &stubEventCache{}

	repo := &idempotentOrderMock{mockOrderRepo: &mockOrderRepo{}}
	svc := service.NewOrderService(repo, newMockEventRepo()).WithCache(cache)

	// Reject > 128 rune keys cleanly before repo is touched.
	tooLong := strings.Repeat("k", 129)
	_, err := svc.CreateOrderWithResult(t.Context(), buyer, service.CreateOrderInput{
		EventID:        eventID,
		IdempotencyKey: tooLong,
		Items:          []repository.CheckoutItemInput{{TicketTypeID: tierID, Quantity: 1}},
	})
	de, ok := errors.AsType[*domain.DomainError](err)
	if !ok || de.Code != domain.ErrCodeValidation {
		t.Fatalf("expected VALIDATION_ERROR for 129-char key, got err=%v", err)
	}

	// Valid creation trims key and invalidates event cache.
	res, err := svc.CreateOrderWithResult(t.Context(), buyer, service.CreateOrderInput{
		EventID:        eventID,
		IdempotencyKey: "  checkout-key-1  ",
		Items:          []repository.CheckoutItemInput{{TicketTypeID: tierID, Quantity: 1}},
	})
	if err != nil || res.IdempotentReplay {
		t.Fatalf("unexpected fresh create result=(%v, %v)", res, err)
	}
	if repo.lastKey != "checkout-key-1" || cache.invCalls != 1 {
		t.Fatalf("lastKey=%q invCalls=%d, want checkout-key-1 / 1", repo.lastKey, cache.invCalls)
	}

	// Idempotent replay skips cache invalidation.
	repo.replayResult = &repository.CreateOrderResult{
		Order:            res.Order,
		IdempotentReplay: true,
	}
	res2, err := svc.CreateOrderWithResult(t.Context(), buyer, service.CreateOrderInput{
		EventID:        eventID,
		IdempotencyKey: "checkout-key-1",
		Items:          []repository.CheckoutItemInput{{TicketTypeID: tierID, Quantity: 1}},
	})
	if err != nil || !res2.IdempotentReplay || cache.invCalls != 1 {
		t.Fatalf("expected replay without extra cache invalidation, res=(%v,%v) invCalls=%d", res2, err, cache.invCalls)
	}
}

func TestEventServiceAndPaymentService_CacheLifecycle(t *testing.T) {
	t.Parallel()

	evID := uuid.New()
	ev := domain.Event{
		ID:          evID,
		OrganizerID: uuid.New(),
		Title:       "Cached Concert",
		Venue:       "Hall A",
		EventDate:   time.Now().UTC().Add(24 * time.Hour),
		TicketTypes: []domain.TicketType{
			{ID: uuid.New(), EventID: evID, Name: "Reg", PriceIDR: 100000, TotalQuota: 10, RemainingQuota: 10},
			{ID: uuid.New(), EventID: evID, Name: "VIP", PriceIDR: 200000, TotalQuota: 5, RemainingQuota: 5},
		},
	}
	evRepo := newMockEventRepo()
	if err := evRepo.CreateWithTiers(t.Context(), &ev); err != nil {
		t.Fatalf("seed mock event: %v", err)
	}

	orderMock := &idempotentOrderMock{
		mockOrderRepo: &mockOrderRepo{},
		failEventID:   evID,
	}
	cache := &stubEventCache{}
	evSvc := service.NewEventService(evRepo, orderMock).WithCache(cache, 3*time.Second)

	// First ListEvents is cache miss -> populates cache.
	gotList, err := evSvc.ListEvents(t.Context())
	if err != nil || len(gotList) != 1 || !cache.hasCatalog {
		t.Fatalf("ListEvents miss got=(%d,%v) hasCatalog=%v", len(gotList), err, cache.hasCatalog)
	}

	// Corrupt underlying repo list check by checking cache hit returns populated catalog directly.
	evRepo.findErr = errors.New("db unavailable")
	if _, err := evSvc.GetEvent(t.Context(), evID); err == nil {
		t.Fatal("expected db error on GetEvent cache miss")
	}
	evRepo.findErr = nil
	if _, err := evSvc.GetEvent(t.Context(), evID); err != nil {
		t.Fatalf("expected GetEvent miss to populate cache: %v", err)
	}
	evRepo.findErr = errors.New("db down")
	if cachedEv, err := evSvc.GetEvent(t.Context(), evID); err != nil || cachedEv.ID != evID {
		t.Fatalf("expected GetEvent cache hit while db down, got=(%v,%v)", cachedEv, err)
	}

	// Payment failure webhook invalidates affected event detail & catalog caches.
	paySvc := service.NewPaymentService(orderMock, &mockWebhookRepo{}, evRepo, "sec").WithCache(cache)
	oid := uuid.New()
	sig := domain.ComputeSHA512Signature(oid.String(), "200", 100000, "sec")
	_, err = paySvc.ProcessWebhook(t.Context(), service.WebhookInput{
		TransactionID:     "tx-1",
		OrderID:           oid.String(),
		StatusCode:        "200",
		GrossAmount:       100000,
		TransactionStatus: "deny",
		SignatureKey:      sig,
	})
	if err != nil {
		t.Fatalf("ProcessWebhook deny failed: %v", err)
	}
	if cache.hasCatalog || len(cache.details) != 0 {
		t.Fatalf("expected event caches invalidated after failure webhook, hasCatalog=%v details=%d", cache.hasCatalog, len(cache.details))
	}
}
