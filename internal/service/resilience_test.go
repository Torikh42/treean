package service_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"golang.org/x/crypto/bcrypt"
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
	if len(limiter.keys) != 1 || !strings.HasPrefix(limiter.keys[0], "rl:login:acct:") || strings.Contains(limiter.keys[0], "203.0.113.10") {
		t.Fatalf("expected account bucket keyed only by email hash, got %v", limiter.keys)
	}

	failOpenSvc := service.NewAuthService(newMockUserRepo(), "jwt-secret").
		WithLoginRateLimiter(&stubRateLimiter{err: errors.New("redis timeout")}, 5)
	_, err = failOpenSvc.Login(capturedCtx, "missing@example.com", "secret123")
	de2, ok2 := errors.AsType[*domain.DomainError](err)
	if !ok2 || de2.Code != domain.ErrCodeInvalidCredentials {
		t.Fatalf("expected fail-open credential check (INVALID_CREDENTIALS), got err=%v", err)
	}
}

type memoryStatefulRateLimiter struct {
	mu       sync.Mutex
	counts   map[string]int
	resets   []string
	peekErr  error
	resetErr error
}

func newMemoryStatefulRateLimiter() *memoryStatefulRateLimiter {
	return &memoryStatefulRateLimiter{counts: make(map[string]int)}
}

func (m *memoryStatefulRateLimiter) Allow(_ context.Context, key string, limit int, window time.Duration) (bool, time.Duration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.counts[key]++
	if m.counts[key] > limit {
		return false, window, nil
	}
	return true, 0, nil
}

func (m *memoryStatefulRateLimiter) Peek(_ context.Context, key string, limit int) (bool, time.Duration, error) {
	if m.peekErr != nil {
		return false, 0, m.peekErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.counts[key] >= limit {
		return false, 15 * time.Minute, nil
	}
	return true, 0, nil
}

func (m *memoryStatefulRateLimiter) Reset(_ context.Context, key string) error {
	if m.resetErr != nil {
		return m.resetErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.counts, key)
	m.resets = append(m.resets, key)
	return nil
}

type countingUserRepo struct {
	*mockUserRepo
	findCalls int
}

func (c *countingUserRepo) FindByEmail(ctx context.Context, email string) (*domain.User, error) {
	c.findCalls++
	return c.mockUserRepo.FindByEmail(ctx, email)
}

func emailHashForTest(email string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	return hex.EncodeToString(sum[:8])
}

func captureClientIPContext(t *testing.T, remoteAddr string) context.Context {
	t.Helper()
	ctxCh := make(chan context.Context, 1)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/auth/login", nil)
	req.RemoteAddr = remoteAddr
	middleware.WithClientIP(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		ctxCh <- r.Context()
	})).ServeHTTP(httptest.NewRecorder(), req)
	return <-ctxCh
}

func TestAuthService_LoginRateLimitIsolatesDistinctEmailsOnSameIP(t *testing.T) {
	t.Parallel()

	repo := newMockUserRepo()
	hash, err := bcrypt.GenerateFromPassword([]byte("correct-pass"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	for _, email := range []string{"victim@event.local", "neighbor@event.local"} {
		if createErr := repo.Create(t.Context(), &domain.User{
			ID:           uuid.New(),
			Email:        email,
			Name:         "Test User",
			PasswordHash: string(hash),
			Role:         domain.RoleBuyer,
		}); createErr != nil {
			t.Fatalf("seed user %s: %v", email, createErr)
		}
	}

	limiter := newMemoryStatefulRateLimiter()
	svc := service.NewAuthService(repo, "jwt-secret").WithLoginRateLimiter(limiter, 2)
	sharedIPCtx := captureClientIPContext(t, "103.1.2.3:4567")

	// Burn victim@event.local account limit on shared CG-NAT IP.
	for range 2 {
		if _, loginErr := svc.Login(sharedIPCtx, "victim@event.local", "correct-pass"); loginErr != nil {
			t.Fatalf("victim initial login failed: %v", loginErr)
		}
	}
	_, victimErr := svc.Login(sharedIPCtx, "victim@event.local", "correct-pass")
	de, ok := errors.AsType[*domain.DomainError](victimErr)
	if !ok || de.Code != domain.ErrCodeRateLimited || de.HTTPStatus != http.StatusTooManyRequests {
		t.Fatalf("expected 429 RATE_LIMITED for victim@event.local, got %v", victimErr)
	}

	// Neighbor sharing the exact same IP 103.1.2.3 still succeeds cleanly.
	res, neighborErr := svc.Login(sharedIPCtx, "neighbor@event.local", "correct-pass")
	if neighborErr != nil || res == nil || res.AccessToken == "" {
		t.Fatalf("neighbor@event.local on same IP failed: res=%v err=%v", res, neighborErr)
	}
}

func mustLoginDomainErr(t *testing.T, ctx context.Context, svc *service.AuthService, email, pass, wantCode string) {
	t.Helper()
	_, err := svc.Login(ctx, email, pass)
	de, ok := errors.AsType[*domain.DomainError](err)
	if !ok || de.Code != wantCode {
		t.Fatalf("Login(%q) got err=%v, want code %s", email, err, wantCode)
	}
}

func mustLoginOK(t *testing.T, ctx context.Context, svc *service.AuthService, email, pass string) {
	t.Helper()
	res, err := svc.Login(ctx, email, pass)
	if err != nil || res == nil || res.AccessToken == "" {
		t.Fatalf("Login(%q) expected success, got res=%v err=%v", email, res, err)
	}
}

func TestAuthService_LoginFailedLockoutResetsOnSuccess(t *testing.T) {
	t.Parallel()

	baseRepo := newMockUserRepo()
	hash, err := bcrypt.GenerateFromPassword([]byte("right-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	const targetEmail = "lockout@event.local"
	if createErr := baseRepo.Create(t.Context(), &domain.User{
		ID: uuid.New(), Email: targetEmail, Name: "Lockout User", PasswordHash: string(hash), Role: domain.RoleBuyer,
	}); createErr != nil {
		t.Fatalf("seed user: %v", createErr)
	}

	repo := &countingUserRepo{mockUserRepo: baseRepo}
	limiter := newMemoryStatefulRateLimiter()
	svc := service.NewAuthService(repo, "jwt-secret").WithLoginRateLimiter(limiter, 20)
	ctx := captureClientIPContext(t, "103.1.2.3:9999")
	failKey := "rl:login:fail:103.1.2.3:" + emailHashForTest(targetEmail)

	for range 4 {
		mustLoginDomainErr(t, ctx, svc, targetEmail, "wrong-password", domain.ErrCodeInvalidCredentials)
	}
	if limiter.counts[failKey] != 4 {
		t.Fatalf("fail count = %d, want 4", limiter.counts[failKey])
	}
	mustLoginOK(t, ctx, svc, targetEmail, "right-password")
	if limiter.counts[failKey] != 0 || len(limiter.resets) != 1 || limiter.resets[0] != failKey {
		t.Fatalf("expected Reset(%q) on success, counts=%v resets=%v", failKey, limiter.counts, limiter.resets)
	}

	for range 5 {
		mustLoginDomainErr(t, ctx, svc, targetEmail, "wrong-password", domain.ErrCodeInvalidCredentials)
	}
	callsBeforeLockout := repo.findCalls
	mustLoginDomainErr(t, ctx, svc, targetEmail, "right-password", domain.ErrCodeRateLimited)
	if repo.findCalls != callsBeforeLockout {
		t.Fatalf("FindByEmail called %d times on locked account (before=%d)", repo.findCalls, callsBeforeLockout)
	}

	limiter.peekErr = errors.New("redis timeout")
	mustLoginOK(t, ctx, svc, targetEmail, "right-password")
}

func TestAuthService_LoginFailedLockoutIsolatedPerIPAndEmail_AndMissingUserTimingParity(t *testing.T) {
	t.Parallel()

	repo := newMockUserRepo()
	hash, err := bcrypt.GenerateFromPassword([]byte("correct-pass"), 10)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	const (
		targetEmail   = "organizer@event.local"
		neighborEmail = "neighbor@event.local"
		demoEmail     = "demo-nopass@event.local"
	)
	for _, u := range []*domain.User{
		{ID: uuid.New(), Email: targetEmail, Name: "Organizer", PasswordHash: string(hash), Role: domain.RoleOrganizer},
		{ID: uuid.New(), Email: neighborEmail, Name: "Neighbor", PasswordHash: string(hash), Role: domain.RoleBuyer},
		{ID: uuid.New(), Email: demoEmail, Name: "Demo NoPass", PasswordHash: "", Role: domain.RoleBuyer},
	} {
		if createErr := repo.Create(t.Context(), u); createErr != nil {
			t.Fatalf("seed user %s: %v", u.Email, createErr)
		}
	}

	limiter := newMemoryStatefulRateLimiter()
	svc := service.NewAuthService(repo, "jwt-secret").WithLoginRateLimiter(limiter, 50)
	attackerCtx := captureClientIPContext(t, "198.51.100.9:1111")
	legitOtherIPCtx := captureClientIPContext(t, "203.0.113.42:2222")

	for range 5 {
		mustLoginDomainErr(t, attackerCtx, svc, targetEmail, "wrong-password", domain.ErrCodeInvalidCredentials)
	}
	mustLoginDomainErr(t, attackerCtx, svc, targetEmail, "correct-pass", domain.ErrCodeRateLimited)
	mustLoginOK(t, legitOtherIPCtx, svc, targetEmail, "correct-pass")
	mustLoginOK(t, attackerCtx, svc, neighborEmail, "correct-pass")

	for _, tc := range []struct {
		name  string
		email string
	}{
		{name: "missing_email", email: "does-not-exist@event.local"},
		{name: "empty_password_hash", email: demoEmail},
	} {
		start := time.Now()
		mustLoginDomainErr(t, t.Context(), svc, tc.email, "any-password", domain.ErrCodeInvalidCredentials)
		if elapsed := time.Since(start); elapsed < 5*time.Millisecond {
			t.Fatalf("%s: expected cost-10 dummy bcrypt parity latency (>=5ms), got %v", tc.name, elapsed)
		}
		unknownFailKey := "rl:login:fail:unknown:" + emailHashForTest(tc.email)
		if limiter.counts[unknownFailKey] != 1 {
			t.Fatalf("%s: fail counter[%q] = %d, want 1", tc.name, unknownFailKey, limiter.counts[unknownFailKey])
		}
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
