package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/torikh42/treean/internal/domain"
	"github.com/torikh42/treean/internal/repository"
	"github.com/torikh42/treean/internal/service"
	"golang.org/x/crypto/bcrypt"
)

var (
	_ service.UserRepository    = (*repository.UserRepo)(nil)
	_ service.EventRepository   = (*repository.EventRepo)(nil)
	_ service.OrderRepository   = (*repository.OrderRepo)(nil)
	_ service.WebhookRepository = (*repository.WebhookRepo)(nil)
)

type mockUserRepo struct {
	mu           sync.Mutex
	usersByEmail map[string]*domain.User
	usersByID    map[uuid.UUID]*domain.User
	demoCalls    int
	createErr    error
	findEmailErr error
	findIDErr    error
	ensureErr    error
	createdUser  *domain.User
}

func newMockUserRepo() *mockUserRepo {
	return &mockUserRepo{
		usersByEmail: make(map[string]*domain.User),
		usersByID:    make(map[uuid.UUID]*domain.User),
	}
}

func (m *mockUserRepo) Create(_ context.Context, u *domain.User) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.createErr != nil {
		return m.createErr
	}
	clone := *u
	m.createdUser = &clone
	m.usersByEmail[u.Email] = &clone
	m.usersByID[u.ID] = &clone
	return nil
}

func (m *mockUserRepo) FindByEmail(_ context.Context, email string) (*domain.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.findEmailErr != nil {
		return nil, m.findEmailErr
	}
	u, ok := m.usersByEmail[email]
	if !ok {
		return nil, domain.NewDomainError(http.StatusUnauthorized, domain.ErrCodeInvalidCredentials, "invalid email or password")
	}
	clone := *u
	return &clone, nil
}

func (m *mockUserRepo) FindByID(_ context.Context, id uuid.UUID) (*domain.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.findIDErr != nil {
		return nil, m.findIDErr
	}
	u, ok := m.usersByID[id]
	if !ok {
		return nil, domain.NewDomainError(http.StatusUnauthorized, domain.ErrCodeUnauthorized, "user not found")
	}
	clone := *u
	return &clone, nil
}

func (m *mockUserRepo) EnsureDemoUser(_ context.Context, _ uuid.UUID, _ domain.Role) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.demoCalls++
	return m.ensureErr
}

type mockEventRepo struct {
	mu           sync.Mutex
	events       map[uuid.UUID]*domain.Event
	tiers        map[uuid.UUID]*domain.TicketType
	createErr    error
	listErr      error
	findErr      error
	findOverride func(uuid.UUID) (*domain.Event, error)
	created      *domain.Event
}

func newMockEventRepo() *mockEventRepo {
	return &mockEventRepo{
		events: make(map[uuid.UUID]*domain.Event),
		tiers:  make(map[uuid.UUID]*domain.TicketType),
	}
}

func (m *mockEventRepo) CreateWithTiers(_ context.Context, ev *domain.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.createErr != nil {
		return m.createErr
	}
	clone := *ev
	clone.TicketTypes = slices.Clone(ev.TicketTypes)
	for i := range clone.TicketTypes {
		tt := clone.TicketTypes[i]
		m.tiers[tt.ID] = &tt
	}
	m.events[ev.ID] = &clone
	m.created = &clone
	return nil
}

func (m *mockEventRepo) ListAll(_ context.Context) ([]domain.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.listErr != nil {
		return nil, m.listErr
	}
	out := make([]domain.Event, 0, len(m.events))
	for _, ev := range m.events {
		out = append(out, m.snapshotEventLocked(ev))
	}
	return out, nil
}

func (m *mockEventRepo) FindByID(_ context.Context, id uuid.UUID) (*domain.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.findErr != nil {
		return nil, m.findErr
	}
	if m.findOverride != nil {
		return m.findOverride(id)
	}
	ev, ok := m.events[id]
	if !ok {
		return nil, domain.NewDomainError(http.StatusNotFound, domain.ErrCodeEventNotFound, "event not found")
	}
	snap := m.snapshotEventLocked(ev)
	return &snap, nil
}

func (m *mockEventRepo) snapshotEventLocked(ev *domain.Event) domain.Event {
	snap := *ev
	snap.TicketTypes = make([]domain.TicketType, len(ev.TicketTypes))
	for i, tt := range ev.TicketTypes {
		if live, ok := m.tiers[tt.ID]; ok {
			snap.TicketTypes[i] = *live
		} else {
			snap.TicketTypes[i] = tt
		}
	}
	return snap
}

type expireCall struct {
	eventID   *uuid.UUID
	limit     int
	actorType string
}

type mockOrderRepo struct {
	mu              sync.Mutex
	eventRepo       *mockEventRepo
	lastItems       []repository.CheckoutItemInput
	lastCancelOrder uuid.UUID
	lastCancelBuyer uuid.UUID
	lastFailReason  string
	expireCalls     []expireCall
	expireErr       error
	createErr       error
	cancelErr       error
	confirmRes      *repository.PaymentConfirmResult
	confirmErr      error
	failOutcome     domain.WebhookOutcome
	failErr         error
	foundOrder      *domain.Order
	findErr         error
	buyerOrders     []domain.Order
	listErr         error
}

func (m *mockOrderRepo) CreateOrder(_ context.Context, eventID, buyerID uuid.UUID, items []repository.CheckoutItemInput, now time.Time) (*domain.Order, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastItems = slices.Clone(items)
	if m.createErr != nil {
		return nil, m.createErr
	}
	if m.eventRepo != nil {
		return m.reserveFromMockEventLocked(eventID, buyerID, items, now)
	}
	if m.foundOrder != nil {
		return m.foundOrder, nil
	}
	return &domain.Order{ID: uuid.New(), EventID: eventID, BuyerID: buyerID, Status: domain.OrderStatusWaitingPayment}, nil
}

func (m *mockOrderRepo) reserveFromMockEventLocked(eventID, buyerID uuid.UUID, items []repository.CheckoutItemInput, now time.Time) (*domain.Order, error) {
	m.eventRepo.mu.Lock()
	defer m.eventRepo.mu.Unlock()

	var totalQty int32
	for _, it := range items {
		tt, ok := m.eventRepo.tiers[it.TicketTypeID]
		if !ok {
			return nil, domain.NewDomainError(http.StatusNotFound, domain.ErrCodeTicketTypeNotFound, "missing tier")
		}
		if tt.RemainingQuota < it.Quantity {
			return nil, domain.NewQuotaExhaustedError("quota exhausted")
		}
		totalQty += it.Quantity
	}
	for _, it := range items {
		m.eventRepo.tiers[it.TicketTypeID].RemainingQuota -= it.Quantity
	}
	return &domain.Order{
		ID:           uuid.New(),
		EventID:      eventID,
		BuyerID:      buyerID,
		TotalTickets: totalQty,
		Status:       domain.OrderStatusWaitingPayment,
		ExpiresAt:    now.Add(domain.OrderHoldDuration),
	}, nil
}

func (m *mockOrderRepo) CancelOrder(_ context.Context, orderID, buyerID uuid.UUID, _ time.Time) (*domain.Order, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastCancelOrder = orderID
	m.lastCancelBuyer = buyerID
	if m.cancelErr != nil {
		return nil, m.cancelErr
	}
	return m.foundOrder, nil
}

func (m *mockOrderRepo) ConfirmPayment(_ context.Context, _ uuid.UUID, _ int64, _ time.Time) (*repository.PaymentConfirmResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.confirmRes, m.confirmErr
}

func (m *mockOrderRepo) FailPayment(_ context.Context, _ uuid.UUID, reason string, _ time.Time) (domain.WebhookOutcome, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastFailReason = reason
	if m.failErr != nil {
		return "", m.failErr
	}
	if m.failOutcome == "" {
		return domain.OutcomeProcessedFailed, nil
	}
	return m.failOutcome, nil
}

func (m *mockOrderRepo) ExpireDueOrders(_ context.Context, eventID *uuid.UUID, limit int, actorType string, _ time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var cp *uuid.UUID
	if eventID != nil {
		v := *eventID
		cp = &v
	}
	m.expireCalls = append(m.expireCalls, expireCall{eventID: cp, limit: limit, actorType: actorType})
	return 0, m.expireErr
}

func (m *mockOrderRepo) FindByID(_ context.Context, _ uuid.UUID) (*domain.Order, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.findErr != nil {
		return nil, m.findErr
	}
	if m.foundOrder == nil {
		return nil, domain.NewDomainError(http.StatusNotFound, domain.ErrCodeOrderNotFound, "order not found")
	}
	return m.foundOrder, nil
}

func (m *mockOrderRepo) ListByBuyer(_ context.Context, _ uuid.UUID) ([]domain.Order, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.buyerOrders, nil
}

type mockWebhookRepo struct {
	mu     sync.Mutex
	logged []*domain.PaymentWebhook
	logErr error
}

func (m *mockWebhookRepo) LogDelivery(_ context.Context, w *domain.PaymentWebhook) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *w
	m.logged = append(m.logged, &cp)
	return m.logErr
}

func assertDomainErr(t *testing.T, err error, wantStatus int, wantCode string) {
	t.Helper()
	de, ok := errors.AsType[*domain.DomainError](err)
	if !ok {
		t.Fatalf("expected *domain.DomainError, got %T (%v)", err, err)
	}
	if de.HTTPStatus != wantStatus || de.Code != wantCode {
		t.Fatalf("got (%d, %q), want (%d, %q)", de.HTTPStatus, de.Code, wantStatus, wantCode)
	}
}

func assertNonNilError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected non-nil error")
	}
}

func TestAuthService_NilGuards(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	validIn := service.RegisterInput{
		Email: "user@event.local", Name: "Alice", Password: "password1", Role: domain.RoleBuyer,
	}

	var nilSvc *service.AuthService
	assertNonNilError(t, func() error { _, err := nilSvc.Register(ctx, validIn); return err }())
	assertNonNilError(t, func() error { _, err := nilSvc.Login(ctx, "a@b.com", "password1"); return err }())
	assertNonNilError(t, func() error { _, err := nilSvc.ParseJWT("tok"); return err }())
	assertNonNilError(t, nilSvc.EnsureDemoUser(ctx, uuid.New(), domain.RoleBuyer))

	noRepo := service.NewAuthService(nil, "secret")
	assertNonNilError(t, func() error { _, err := noRepo.Register(ctx, validIn); return err }())
	assertNonNilError(t, func() error { _, err := noRepo.Login(ctx, "a@b.com", "password1"); return err }())
	assertNonNilError(t, noRepo.EnsureDemoUser(ctx, uuid.New(), domain.RoleBuyer))
}

func TestAuthService_SecretAndContextGuards(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	validIn := service.RegisterInput{
		Email: "user@event.local", Name: "Alice", Password: "password1", Role: domain.RoleBuyer,
	}
	repo := newMockUserRepo()
	for _, secret := range []string{"", "   "} {
		noSecret := service.NewAuthService(repo, secret)
		assertNonNilError(t, func() error { _, err := noSecret.Register(ctx, validIn); return err }())
		assertNonNilError(t, func() error { _, err := noSecret.Login(ctx, "a@b.com", "password1"); return err }())
		_, err := noSecret.ParseJWT("tok")
		assertDomainErr(t, err, http.StatusUnauthorized, domain.ErrCodeUnauthorized)
	}

	validSvc := service.NewAuthService(repo, "secret")
	var nilCtx context.Context
	assertNonNilError(t, func() error { _, err := validSvc.Register(nilCtx, validIn); return err }())
	assertNonNilError(t, func() error { _, err := validSvc.Login(nilCtx, "a@b.com", "password1"); return err }())
	assertNonNilError(t, validSvc.EnsureDemoUser(nilCtx, uuid.New(), domain.RoleBuyer))
}

func TestAuthService_RegisterValidation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   service.RegisterInput
	}{
		{
			name: "invalid role",
			in:   service.RegisterInput{Email: "u@event.local", Name: "Alice", Password: "password1", Role: domain.Role("ADMIN")},
		},
		{
			name: "empty email",
			in:   service.RegisterInput{Email: "   ", Name: "Alice", Password: "password1", Role: domain.RoleBuyer},
		},
		{
			name: "email with display name rejected",
			in:   service.RegisterInput{Email: "Alice <alice@event.local>", Name: "Alice", Password: "password1", Role: domain.RoleBuyer},
		},
		{
			name: "angle bracket email rejected",
			in:   service.RegisterInput{Email: "<alice@event.local>", Name: "Alice", Password: "password1", Role: domain.RoleBuyer},
		},
		{
			name: "malformed email rejected",
			in:   service.RegisterInput{Email: "not-an-email", Name: "Alice", Password: "password1", Role: domain.RoleBuyer},
		},
		{
			name: "name too short after trim",
			in:   service.RegisterInput{Email: "a@event.local", Name: " A ", Password: "password1", Role: domain.RoleBuyer},
		},
		{
			name: "name too long",
			in:   service.RegisterInput{Email: "a@event.local", Name: strings.Repeat("a", 101), Password: "password1", Role: domain.RoleBuyer},
		},
		{
			name: "password under 6 chars",
			in:   service.RegisterInput{Email: "a@event.local", Name: "Alice", Password: "12345", Role: domain.RoleBuyer},
		},
		{
			name: "password all spaces",
			in:   service.RegisterInput{Email: "a@event.local", Name: "Alice", Password: "      ", Role: domain.RoleBuyer},
		},
		{
			name: "password over 72 ASCII bytes",
			in:   service.RegisterInput{Email: "a@event.local", Name: "Alice", Password: strings.Repeat("p", 73), Role: domain.RoleBuyer},
		},
		{
			name: "password over 72 UTF-8 bytes even with 25 chars",
			in:   service.RegisterInput{Email: "a@event.local", Name: "Alice", Password: strings.Repeat("\u20ac", 25), Role: domain.RoleBuyer},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := service.NewAuthService(newMockUserRepo(), "test-secret")
			_, err := svc.Register(t.Context(), tc.in)
			assertDomainErr(t, err, http.StatusUnprocessableEntity, domain.ErrCodeValidation)
		})
	}
}

func TestAuthService_RegisterSuccessAndDuplicate(t *testing.T) {
	t.Parallel()

	repo := newMockUserRepo()
	svc := service.NewAuthService(repo, "test-jwt-secret")

	res, err := svc.Register(t.Context(), service.RegisterInput{
		Email:    "  Alice@Event.LOCAL ",
		Name:     "  Alice Builder  ",
		Password: strings.Repeat("s", 72),
		Role:     domain.RoleOrganizer,
	})
	if err != nil {
		t.Fatalf("unexpected Register error: %v", err)
	}
	if res.User.Email != "alice@event.local" || res.User.Name != "Alice Builder" {
		t.Fatalf("unexpected normalized user: %+v", res.User)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(res.User.PasswordHash), []byte(strings.Repeat("s", 72))); err != nil {
		t.Fatalf("password hash did not verify: %v", err)
	}
	actor, err := svc.ParseJWT(res.AccessToken)
	if err != nil {
		t.Fatalf("ParseJWT failed on issued token: %v", err)
	}
	if actor.ID != res.User.ID || actor.Email != "alice@event.local" || actor.Role != domain.RoleOrganizer {
		t.Fatalf("unexpected parsed actor: %+v", actor)
	}

	repo.createErr = domain.NewDomainError(http.StatusConflict, domain.ErrCodeEmailAlreadyExists, "duplicate email")
	_, dupErr := svc.Register(t.Context(), service.RegisterInput{
		Email: "alice@event.local", Name: "Alice", Password: "password1", Role: domain.RoleBuyer,
	})
	assertDomainErr(t, dupErr, http.StatusConflict, domain.ErrCodeEmailAlreadyExists)
}

func TestAuthService_Login(t *testing.T) {
	t.Parallel()

	repo := newMockUserRepo()
	svc := service.NewAuthService(repo, "login-secret")

	hash, err := bcrypt.GenerateFromPassword([]byte("correct-pass"), 10)
	if err != nil {
		t.Fatalf("bcrypt hash failed: %v", err)
	}
	activeID := uuid.New()
	repo.usersByEmail["buyer@event.local"] = &domain.User{
		ID: activeID, Email: "buyer@event.local", Name: "Buyer", PasswordHash: string(hash), Role: domain.RoleBuyer,
	}
	repo.usersByEmail["demo@event.local"] = &domain.User{
		ID: uuid.New(), Email: "demo@event.local", Name: "Demo", PasswordHash: "", Role: domain.RoleBuyer,
	}

	badCases := []struct {
		name     string
		email    string
		password string
	}{
		{name: "blank email", email: "   ", password: "correct-pass"},
		{name: "blank password", email: "buyer@event.local", password: "   "},
		{name: "unknown user", email: "missing@event.local", password: "correct-pass"},
		{name: "demo user empty password hash", email: "demo@event.local", password: "any-password"},
		{name: "wrong password", email: "buyer@event.local", password: "wrong-password"},
	}
	for _, tc := range badCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, loginErr := svc.Login(t.Context(), tc.email, tc.password)
			assertDomainErr(t, loginErr, http.StatusUnauthorized, domain.ErrCodeInvalidCredentials)
		})
	}

	res, err := svc.Login(t.Context(), "  BUYER@Event.Local ", "correct-pass")
	if err != nil {
		t.Fatalf("valid Login failed: %v", err)
	}
	if res.User.ID != activeID || res.AccessToken == "" {
		t.Fatalf("unexpected Login result: %+v", res)
	}
}

func TestAuthService_ParseJWTRejectsInvalidTokens(t *testing.T) {
	t.Parallel()

	secret := "jwt-parse-secret"
	svc := service.NewAuthService(newMockUserRepo(), secret)

	signWith := func(method jwt.SigningMethod, key any, claims jwt.MapClaims) string {
		tok := jwt.NewWithClaims(method, claims)
		s, err := tok.SignedString(key)
		if err != nil {
			t.Fatalf("sign token failed: %v", err)
		}
		return s
	}

	now := time.Now().UTC()
	cases := []struct {
		name  string
		token string
	}{
		{name: "empty token", token: "   "},
		{name: "malformed token", token: "not.a.jwt"},
		{name: "wrong secret", token: signWith(jwt.SigningMethodHS256, []byte("other-secret"), jwt.MapClaims{"sub": uuid.NewString(), "role": "BUYER", "exp": now.Add(time.Hour).Unix()})},
		{name: "missing exp claim", token: signWith(jwt.SigningMethodHS256, []byte(secret), jwt.MapClaims{"sub": uuid.NewString(), "role": "BUYER"})},
		{name: "expired token", token: signWith(jwt.SigningMethodHS256, []byte(secret), jwt.MapClaims{"sub": uuid.NewString(), "role": "BUYER", "exp": now.Add(-time.Minute).Unix()})},
		{name: "disallowed HS512 method", token: signWith(jwt.SigningMethodHS512, []byte(secret), jwt.MapClaims{"sub": uuid.NewString(), "role": "BUYER", "exp": now.Add(time.Hour).Unix()})},
		{name: "nil UUID subject", token: signWith(jwt.SigningMethodHS256, []byte(secret), jwt.MapClaims{"sub": uuid.Nil.String(), "role": "BUYER", "exp": now.Add(time.Hour).Unix()})},
		{name: "non-UUID subject", token: signWith(jwt.SigningMethodHS256, []byte(secret), jwt.MapClaims{"sub": "bad-uuid", "role": "BUYER", "exp": now.Add(time.Hour).Unix()})},
		{name: "invalid role", token: signWith(jwt.SigningMethodHS256, []byte(secret), jwt.MapClaims{"sub": uuid.NewString(), "role": "SUPERADMIN", "exp": now.Add(time.Hour).Unix()})},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := svc.ParseJWT(tc.token)
			assertDomainErr(t, err, http.StatusUnauthorized, domain.ErrCodeUnauthorized)
		})
	}
}

func TestAuthService_EnsureDemoUser(t *testing.T) {
	t.Parallel()

	repo := newMockUserRepo()
	svc := service.NewAuthService(repo, "sec")

	assertDomainErr(t, svc.EnsureDemoUser(t.Context(), uuid.Nil, domain.RoleBuyer), http.StatusUnprocessableEntity, domain.ErrCodeValidation)
	assertDomainErr(t, svc.EnsureDemoUser(t.Context(), uuid.New(), domain.Role("GUEST")), http.StatusUnprocessableEntity, domain.ErrCodeValidation)

	if err := svc.EnsureDemoUser(t.Context(), uuid.New(), domain.RoleOrganizer); err != nil {
		t.Fatalf("unexpected EnsureDemoUser error: %v", err)
	}
	if repo.demoCalls != 1 {
		t.Fatalf("demoCalls = %d, want 1", repo.demoCalls)
	}
}

func TestEventService_GuardsAndRoleChecks(t *testing.T) {
	t.Parallel()

	var nilSvc *service.EventService
	validIn := service.CreateEventInput{
		Title: "Go Conf", Venue: "Hall", EventDate: time.Now().Add(time.Hour),
		TicketTypes: []service.CreateTierInput{{Name: "Reg", PriceIDR: 100, TotalQuota: 10}, {Name: "VIP", PriceIDR: 200, TotalQuota: 10}},
	}
	org := domain.Actor{ID: uuid.New(), Role: domain.RoleOrganizer}

	assertNonNilError(t, func() error { _, err := nilSvc.CreateEvent(t.Context(), org, validIn); return err }())
	assertNonNilError(t, func() error { _, err := nilSvc.ListEvents(t.Context()); return err }())
	assertNonNilError(t, func() error { _, err := nilSvc.GetEvent(t.Context(), uuid.New()); return err }())

	svc := service.NewEventService(newMockEventRepo(), &mockOrderRepo{})
	var nilCtx context.Context
	assertNonNilError(t, func() error { _, err := svc.CreateEvent(nilCtx, org, validIn); return err }())
	assertNonNilError(t, func() error { _, err := svc.ListEvents(nilCtx); return err }())
	assertNonNilError(t, func() error { _, err := svc.GetEvent(nilCtx, uuid.New()); return err }())

	_, err := svc.CreateEvent(t.Context(), domain.Actor{ID: uuid.Nil, Role: domain.RoleOrganizer}, validIn)
	assertDomainErr(t, err, http.StatusUnauthorized, domain.ErrCodeUnauthorized)

	_, err = svc.CreateEvent(t.Context(), domain.Actor{ID: uuid.New(), Role: domain.RoleBuyer}, validIn)
	assertDomainErr(t, err, http.StatusForbidden, domain.ErrCodeForbidden)
}

func TestEventService_CreateValidation(t *testing.T) {
	t.Parallel()

	org := domain.Actor{ID: uuid.New(), Role: domain.RoleOrganizer}
	future := time.Now().Add(24 * time.Hour)
	twoTiers := []service.CreateTierInput{
		{Name: "Reguler", PriceIDR: 100000, TotalQuota: 50},
		{Name: "VIP", PriceIDR: 200000, TotalQuota: 20},
	}

	elevenTiers := make([]service.CreateTierInput, 11)
	for i := range elevenTiers {
		elevenTiers[i] = service.CreateTierInput{Name: "Tier-" + string(rune('A'+i)), PriceIDR: 1000, TotalQuota: 10}
	}

	cases := []struct {
		name string
		in   service.CreateEventInput
	}{
		{name: "title too short", in: service.CreateEventInput{Title: " ab ", Venue: "Hall", EventDate: future, TicketTypes: twoTiers}},
		{name: "venue too short", in: service.CreateEventInput{Title: "Summit", Venue: " x ", EventDate: future, TicketTypes: twoTiers}},
		{name: "zero event date", in: service.CreateEventInput{Title: "Summit", Venue: "Hall", TicketTypes: twoTiers}},
		{name: "fewer than 2 tiers", in: service.CreateEventInput{Title: "Summit", Venue: "Hall", EventDate: future, TicketTypes: twoTiers[:1]}},
		{name: "more than 10 tiers", in: service.CreateEventInput{Title: "Summit", Venue: "Hall", EventDate: future, TicketTypes: elevenTiers}},
		{name: "case-insensitive duplicate tier name", in: service.CreateEventInput{
			Title: "Summit", Venue: "Hall", EventDate: future,
			TicketTypes: []service.CreateTierInput{{Name: "VIP", PriceIDR: 100, TotalQuota: 10}, {Name: " vip ", PriceIDR: 200, TotalQuota: 10}},
		}},
		{name: "negative tier price", in: service.CreateEventInput{
			Title: "Summit", Venue: "Hall", EventDate: future,
			TicketTypes: []service.CreateTierInput{{Name: "Reg", PriceIDR: -1, TotalQuota: 10}, {Name: "VIP", PriceIDR: 200, TotalQuota: 10}},
		}},
		{name: "zero tier quota", in: service.CreateEventInput{
			Title: "Summit", Venue: "Hall", EventDate: future,
			TicketTypes: []service.CreateTierInput{{Name: "Reg", PriceIDR: 100, TotalQuota: 0}, {Name: "VIP", PriceIDR: 200, TotalQuota: 10}},
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := service.NewEventService(newMockEventRepo(), &mockOrderRepo{})
			_, err := svc.CreateEvent(t.Context(), org, tc.in)
			assertDomainErr(t, err, http.StatusUnprocessableEntity, domain.ErrCodeValidation)
		})
	}
}

func TestEventService_CreateSuccess(t *testing.T) {
	t.Parallel()

	evRepo := newMockEventRepo()
	svc := service.NewEventService(evRepo, &mockOrderRepo{})
	org := domain.Actor{ID: uuid.New(), Role: domain.RoleOrganizer}
	wib := time.FixedZone("WIB", 7*3600)
	date := time.Date(2026, 12, 1, 19, 0, 0, 0, wib)

	ev, err := svc.CreateEvent(t.Context(), org, service.CreateEventInput{
		Title: "  Go Conf  ", Description: "  Annual  ", Venue: "  Jakarta  ", EventDate: date,
		TicketTypes: []service.CreateTierInput{
			{Name: "  Reguler ", PriceIDR: 150000, TotalQuota: 80},
			{Name: " VIP ", PriceIDR: 350000, TotalQuota: 20},
		},
	})
	if err != nil {
		t.Fatalf("CreateEvent failed: %v", err)
	}
	if ev.Title != "Go Conf" || ev.Venue != "Jakarta" || ev.EventDate.Location() != time.UTC {
		t.Fatalf("unexpected normalized event: %+v", ev)
	}
	if ev.TicketTypes[0].RemainingQuota != 80 || ev.TicketTypes[1].RemainingQuota != 20 {
		t.Fatalf("unexpected remaining quotas: %+v", ev.TicketTypes)
	}
}

func TestEventService_ListAndGetSuccess(t *testing.T) {
	t.Parallel()

	evRepo := newMockEventRepo()
	ev := &domain.Event{
		ID: uuid.New(), Title: "Go Conf", Venue: "Jakarta",
		TicketTypes: []domain.TicketType{{ID: uuid.New(), Name: "Reguler", PriceIDR: 100, TotalQuota: 10, RemainingQuota: 10}},
	}
	evRepo.events[ev.ID] = ev
	ordRepo := &mockOrderRepo{}
	svc := service.NewEventService(evRepo, ordRepo)

	_, err := svc.GetEvent(t.Context(), uuid.Nil)
	assertDomainErr(t, err, http.StatusUnprocessableEntity, domain.ErrCodeValidation)

	list, err := svc.ListEvents(t.Context())
	if err != nil || len(list) != 1 {
		t.Fatalf("ListEvents got (%d, %v), want len=1", len(list), err)
	}
	gotEv, err := svc.GetEvent(t.Context(), ev.ID)
	if err != nil || gotEv.ID != ev.ID {
		t.Fatalf("GetEvent got (%v, %v), want id=%s", gotEv, err, ev.ID)
	}
	if len(ordRepo.expireCalls) != 2 || ordRepo.expireCalls[0].actorType != "lazy-check" || ordRepo.expireCalls[0].limit != 50 {
		t.Fatalf("unexpected ExpireDueOrders calls: %+v", ordRepo.expireCalls)
	}
	if ordRepo.expireCalls[0].eventID != nil || ordRepo.expireCalls[1].eventID == nil {
		t.Fatalf("unexpected eventID scopes on ExpireDueOrders: %+v", ordRepo.expireCalls)
	}
	if *ordRepo.expireCalls[1].eventID != ev.ID {
		t.Fatalf("GetEvent ExpireDueOrders eventID = %s, want %s", *ordRepo.expireCalls[1].eventID, ev.ID)
	}
}

func TestOrderService_GuardsAndValidation(t *testing.T) {
	t.Parallel()

	var nilSvc *service.OrderService
	buyer := domain.Actor{ID: uuid.New(), Role: domain.RoleBuyer}
	evID := uuid.New()
	tierID := uuid.New()
	validIn := service.CreateOrderInput{
		EventID: evID, Items: []repository.CheckoutItemInput{{TicketTypeID: tierID, Quantity: 1}},
	}

	assertNonNilError(t, func() error { _, err := nilSvc.CreateOrder(t.Context(), buyer, validIn); return err }())
	assertNonNilError(t, func() error { _, err := nilSvc.CancelOrder(t.Context(), buyer, uuid.New()); return err }())
	assertNonNilError(t, func() error { _, err := nilSvc.ListOrders(t.Context(), buyer); return err }())
	assertNonNilError(t, func() error { _, err := nilSvc.GetOrder(t.Context(), buyer, uuid.New()); return err }())

	svc := service.NewOrderService(&mockOrderRepo{}, newMockEventRepo())
	var nilCtx context.Context
	assertNonNilError(t, func() error { _, err := svc.CreateOrder(nilCtx, buyer, validIn); return err }())

	_, err := svc.CreateOrder(t.Context(), domain.Actor{ID: uuid.Nil, Role: domain.RoleBuyer}, validIn)
	assertDomainErr(t, err, http.StatusUnauthorized, domain.ErrCodeUnauthorized)
	_, err = svc.CreateOrder(t.Context(), domain.Actor{ID: uuid.New(), Role: ""}, validIn)
	assertDomainErr(t, err, http.StatusUnauthorized, domain.ErrCodeUnauthorized)
	_, err = svc.CreateOrder(t.Context(), domain.Actor{ID: uuid.New(), Role: domain.Role("INVALID")}, validIn)
	assertDomainErr(t, err, http.StatusForbidden, domain.ErrCodeForbidden)

	_, err = svc.CreateOrder(t.Context(), buyer, service.CreateOrderInput{EventID: uuid.Nil, Items: validIn.Items})
	assertDomainErr(t, err, http.StatusUnprocessableEntity, domain.ErrCodeValidation)
	_, err = svc.CancelOrder(t.Context(), buyer, uuid.Nil)
	assertDomainErr(t, err, http.StatusUnprocessableEntity, domain.ErrCodeValidation)
	_, err = svc.GetOrder(t.Context(), buyer, uuid.Nil)
	assertDomainErr(t, err, http.StatusUnprocessableEntity, domain.ErrCodeValidation)
}

func TestOrderService_CreateQuantityAndOrdering(t *testing.T) {
	t.Parallel()

	evID := uuid.New()
	tierLow := uuid.MustParse("7fffffff-0000-0000-0000-000000000000")
	tierHigh := uuid.MustParse("80000000-0000-0000-0000-000000000000")
	buyer := domain.Actor{ID: uuid.New(), Role: domain.RoleBuyer}

	badCases := []struct {
		name     string
		items    []repository.CheckoutItemInput
		wantCode string
	}{
		{name: "empty items", items: nil, wantCode: domain.ErrCodeInvalidQuantity},
		{name: "nil ticket_type_id", items: []repository.CheckoutItemInput{{TicketTypeID: uuid.Nil, Quantity: 1}}, wantCode: domain.ErrCodeValidation},
		{name: "zero qty", items: []repository.CheckoutItemInput{{TicketTypeID: tierLow, Quantity: 0}}, wantCode: domain.ErrCodeInvalidQuantity},
		{name: "negative qty", items: []repository.CheckoutItemInput{{TicketTypeID: tierLow, Quantity: -1}}, wantCode: domain.ErrCodeInvalidQuantity},
		{name: "single item > 4", items: []repository.CheckoutItemInput{{TicketTypeID: tierLow, Quantity: 5}}, wantCode: domain.ErrCodeInvalidQuantity},
		{name: "maxint32 overflow guarded", items: []repository.CheckoutItemInput{{TicketTypeID: tierLow, Quantity: math.MaxInt32}, {TicketTypeID: tierHigh, Quantity: 2}}, wantCode: domain.ErrCodeInvalidQuantity},
		{name: "combined > 4", items: []repository.CheckoutItemInput{{TicketTypeID: tierLow, Quantity: 3}, {TicketTypeID: tierHigh, Quantity: 2}}, wantCode: domain.ErrCodeInvalidQuantity},
	}

	for _, tc := range badCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := service.NewOrderService(&mockOrderRepo{}, newMockEventRepo())
			_, err := svc.CreateOrder(t.Context(), buyer, service.CreateOrderInput{EventID: evID, Items: tc.items})
			assertDomainErr(t, err, http.StatusUnprocessableEntity, tc.wantCode)
		})
	}

	ordRepo := &mockOrderRepo{}
	svc := service.NewOrderService(ordRepo, newMockEventRepo())
	_, err := svc.CreateOrder(t.Context(), buyer, service.CreateOrderInput{
		EventID: evID,
		Items: []repository.CheckoutItemInput{
			{TicketTypeID: tierHigh, Quantity: 1},
			{TicketTypeID: tierLow, Quantity: 2},
			{TicketTypeID: tierHigh, Quantity: 1},
		},
	})
	if err != nil {
		t.Fatalf("CreateOrder failed: %v", err)
	}
	want := []repository.CheckoutItemInput{{TicketTypeID: tierLow, Quantity: 2}, {TicketTypeID: tierHigh, Quantity: 2}}
	if !slices.Equal(ordRepo.lastItems, want) {
		t.Fatalf("deduped/sorted items = %+v, want %+v", ordRepo.lastItems, want)
	}
}

func TestOrderService_CancelListAndGetOwnership(t *testing.T) {
	t.Parallel()

	ownerID := uuid.New()
	otherBuyerID := uuid.New()
	orgOwnerID := uuid.New()
	eventID := uuid.New()
	orderID := uuid.New()
	evRepo := newMockEventRepo()
	evRepo.events[eventID] = &domain.Event{ID: eventID, OrganizerID: orgOwnerID}
	ordRepo := &mockOrderRepo{
		foundOrder:  &domain.Order{ID: orderID, EventID: eventID, BuyerID: ownerID, Status: domain.OrderStatusWaitingPayment},
		buyerOrders: []domain.Order{{ID: orderID, EventID: eventID, BuyerID: ownerID}},
	}
	svc := service.NewOrderService(ordRepo, evRepo)

	_, err := svc.CancelOrder(t.Context(), domain.Actor{ID: ownerID, Role: domain.RoleBuyer}, orderID)
	if err != nil || ordRepo.lastCancelOrder != orderID || ordRepo.lastCancelBuyer != ownerID {
		t.Fatalf("CancelOrder got err=%v order=%s buyer=%s", err, ordRepo.lastCancelOrder, ordRepo.lastCancelBuyer)
	}

	orders, err := svc.ListOrders(t.Context(), domain.Actor{ID: ownerID, Role: domain.RoleBuyer})
	if err != nil || len(orders) != 1 {
		t.Fatalf("ListOrders got (%d, %v)", len(orders), err)
	}
	if len(ordRepo.expireCalls) != 1 || ordRepo.expireCalls[0].actorType != "lazy-check" {
		t.Fatalf("ListOrders should sweep lazy-check, got %+v", ordRepo.expireCalls)
	}

	_, err = svc.GetOrder(t.Context(), domain.Actor{ID: otherBuyerID, Role: domain.RoleBuyer}, orderID)
	assertDomainErr(t, err, http.StatusForbidden, domain.ErrCodeForbidden)

	gotOwner, err := svc.GetOrder(t.Context(), domain.Actor{ID: ownerID, Role: domain.RoleBuyer}, orderID)
	if err != nil || gotOwner.ID != orderID {
		t.Fatalf("owner GetOrder got (%v, %v)", gotOwner, err)
	}
	gotOrg, err := svc.GetOrder(t.Context(), domain.Actor{ID: orgOwnerID, Role: domain.RoleOrganizer}, orderID)
	if err != nil || gotOrg.ID != orderID {
		t.Fatalf("organizer GetOrder got (%v, %v)", gotOrg, err)
	}
}

func TestOrderService_GetOrderOrganizerOwnership(t *testing.T) {
	t.Parallel()

	ownerID := uuid.New()
	orgOwnerID := uuid.New()
	otherOrgID := uuid.New()
	eventID := uuid.New()
	orderID := uuid.New()

	evRepo := newMockEventRepo()
	evRepo.events[eventID] = &domain.Event{ID: eventID, OrganizerID: orgOwnerID}
	ordRepo := &mockOrderRepo{
		foundOrder: &domain.Order{ID: orderID, EventID: eventID, BuyerID: ownerID, Status: domain.OrderStatusWaitingPayment},
	}
	svc := service.NewOrderService(ordRepo, evRepo)

	_, err := svc.GetOrder(t.Context(), domain.Actor{ID: otherOrgID, Role: domain.RoleOrganizer}, orderID)
	assertDomainErr(t, err, http.StatusForbidden, domain.ErrCodeForbidden)

	boom := errors.New("event lookup failed")
	errEvRepo := newMockEventRepo()
	errEvRepo.findErr = boom
	errSvc := service.NewOrderService(ordRepo, errEvRepo)
	if _, err := errSvc.GetOrder(t.Context(), domain.Actor{ID: orgOwnerID, Role: domain.RoleOrganizer}, orderID); !errors.Is(err, boom) {
		t.Fatalf("expected FindByID error propagation, got %v", err)
	}

	nilEvRepo := newMockEventRepo()
	nilEvRepo.findOverride = func(uuid.UUID) (*domain.Event, error) { return nil, nil }
	nilEvSvc := service.NewOrderService(ordRepo, nilEvRepo)
	_, err = nilEvSvc.GetOrder(t.Context(), domain.Actor{ID: orgOwnerID, Role: domain.RoleOrganizer}, orderID)
	assertDomainErr(t, err, http.StatusForbidden, domain.ErrCodeForbidden)
}

func TestPaymentService_WebhookAuthAndOrderValidation(t *testing.T) {
	t.Parallel()

	const secret = "webhook-test-secret"
	var nilSvc *service.PaymentService
	assertNonNilError(t, func() error { _, err := nilSvc.ProcessWebhook(t.Context(), service.WebhookInput{}); return err }())

	noSecretSvc := service.NewPaymentService(&mockOrderRepo{}, &mockWebhookRepo{}, newMockEventRepo(), "")
	_, err := noSecretSvc.ProcessWebhook(t.Context(), service.WebhookInput{
		OrderID: uuid.NewString(), StatusCode: "200", GrossAmount: 1000, SignatureKey: "abc",
	})
	assertDomainErr(t, err, http.StatusUnauthorized, domain.ErrCodeUnauthorizedWebhook)

	whRepo := &mockWebhookRepo{}
	svc := service.NewPaymentService(&mockOrderRepo{}, whRepo, newMockEventRepo(), secret)

	_, err = svc.ProcessWebhook(t.Context(), service.WebhookInput{
		OrderID: uuid.NewString(), StatusCode: "200", GrossAmount: 1000, SignatureKey: "wrong",
	})
	assertDomainErr(t, err, http.StatusUnauthorized, domain.ErrCodeUnauthorizedWebhook)
	if len(whRepo.logged) != 1 || whRepo.logged[0].Outcome != domain.OutcomeSignatureInvalid || string(whRepo.logged[0].RawPayload) != "{}" {
		t.Fatalf("unexpected invalid sig log: %+v", whRepo.logged[0])
	}

	for _, badOrderID := range []string{"not-a-uuid", uuid.Nil.String()} {
		sig := domain.ComputeSHA512Signature(badOrderID, "200", 1000, secret)
		_, badErr := svc.ProcessWebhook(t.Context(), service.WebhookInput{
			OrderID: badOrderID, StatusCode: "200", GrossAmount: 1000, TransactionStatus: "settlement", SignatureKey: sig, RawPayload: []byte("  \t "),
		})
		assertDomainErr(t, badErr, http.StatusNotFound, domain.ErrCodeOrderNotFound)
	}
	if whRepo.logged[1].Outcome != domain.OutcomeOrderNotFound || whRepo.logged[2].Outcome != domain.OutcomeOrderNotFound || whRepo.logged[2].ParsedOrderID != nil {
		t.Fatalf("unexpected bad order_id logs: %+v", whRepo.logged)
	}
}

func TestPaymentService_SettlementOutcomes(t *testing.T) {
	t.Parallel()

	const secret = "webhook-settlement-secret"
	orderID := uuid.New()
	sig := domain.ComputeSHA512Signature(orderID.String(), "200", 300000, secret)

	cases := []struct {
		name           string
		confirmRes     *repository.PaymentConfirmResult
		confirmErr     error
		grossAmount    int64
		wantErrCode    string
		wantHTTPStatus int
		wantOutcome    domain.WebhookOutcome
		wantRefund     bool
	}{
		{
			name:        "paid success",
			confirmRes:  &repository.PaymentConfirmResult{Order: &domain.Order{ID: orderID, Status: domain.OrderStatusPaid}},
			grossAmount: 300000,
			wantOutcome: domain.OutcomeProcessedPaid,
		},
		{
			name:        "duplicate replay",
			confirmRes:  &repository.PaymentConfirmResult{Order: &domain.Order{ID: orderID, Status: domain.OrderStatusPaid}, IdempotentReplay: true},
			grossAmount: 300000,
			wantOutcome: domain.OutcomeIgnoredDuplicate,
		},
		{
			name:           "negative gross amount rejected before ConfirmPayment",
			grossAmount:    -100,
			wantErrCode:    domain.ErrCodePaymentAmountMismatch,
			wantHTTPStatus: http.StatusBadRequest,
			wantOutcome:    domain.OutcomeAmountMismatch,
		},
		{
			name:           "expired order logs refund flag",
			confirmErr:     domain.NewOrderExpiredError("expired"),
			grossAmount:    300000,
			wantErrCode:    domain.ErrCodeOrderExpired,
			wantHTTPStatus: http.StatusConflict,
			wantOutcome:    domain.OutcomeRejectedLateExpired,
			wantRefund:     true,
		},
		{
			name:           "cancelled order capture logs refund flag per B9",
			confirmErr:     domain.NewInvalidOrderStateError("order was cancelled"),
			grossAmount:    300000,
			wantErrCode:    domain.ErrCodeInvalidOrderState,
			wantHTTPStatus: http.StatusConflict,
			wantOutcome:    domain.OutcomeRejectedLateExpired,
			wantRefund:     true,
		},
		{
			name:           "amount mismatch error",
			confirmErr:     domain.NewPaymentAmountMismatchError("mismatch"),
			grossAmount:    300000,
			wantErrCode:    domain.ErrCodePaymentAmountMismatch,
			wantHTTPStatus: http.StatusBadRequest,
			wantOutcome:    domain.OutcomeAmountMismatch,
		},
		{
			name:           "order not found error",
			confirmErr:     domain.NewDomainError(http.StatusNotFound, domain.ErrCodeOrderNotFound, "missing"),
			grossAmount:    300000,
			wantErrCode:    domain.ErrCodeOrderNotFound,
			wantHTTPStatus: http.StatusNotFound,
			wantOutcome:    domain.OutcomeOrderNotFound,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			whRepo := &mockWebhookRepo{}
			ordRepo := &mockOrderRepo{confirmRes: tc.confirmRes, confirmErr: tc.confirmErr}
			svc := service.NewPaymentService(ordRepo, whRepo, newMockEventRepo(), secret)
			useSig := sig
			if tc.grossAmount != 300000 {
				useSig = domain.ComputeSHA512Signature(orderID.String(), "200", tc.grossAmount, secret)
			}

			res, err := svc.ProcessWebhook(t.Context(), service.WebhookInput{
				TransactionID: "tx-1", OrderID: orderID.String(), StatusCode: "200",
				GrossAmount: tc.grossAmount, TransactionStatus: "capture", SignatureKey: useSig,
			})
			if tc.wantErrCode != "" {
				assertDomainErr(t, err, tc.wantHTTPStatus, tc.wantErrCode)
			} else if err != nil || res.Outcome != tc.wantOutcome {
				t.Fatalf("got res=%+v err=%v, want outcome=%s", res, err, tc.wantOutcome)
			}
			if len(whRepo.logged) != 1 || whRepo.logged[0].Outcome != tc.wantOutcome || whRepo.logged[0].RequiresRefund != tc.wantRefund {
				t.Fatalf("logged = %+v, want outcome=%s refund=%v", whRepo.logged, tc.wantOutcome, tc.wantRefund)
			}
		})
	}
}

func TestPaymentService_FailUnknownAndDemoPay(t *testing.T) {
	t.Parallel()

	const secret = "demo-pay-secret"
	orderID := uuid.New()
	buyerID := uuid.New()
	whRepo := &mockWebhookRepo{}
	ordRepo := &mockOrderRepo{
		failOutcome: domain.OutcomeProcessedFailed,
		foundOrder:  &domain.Order{ID: orderID, BuyerID: buyerID, TotalAmountIDR: 350000, Status: domain.OrderStatusWaitingPayment},
		confirmRes:  &repository.PaymentConfirmResult{Order: &domain.Order{ID: orderID, BuyerID: buyerID, Status: domain.OrderStatusPaid}},
	}
	svc := service.NewPaymentService(ordRepo, whRepo, newMockEventRepo(), secret)

	sig := domain.ComputeSHA512Signature(orderID.String(), "200", 350000, secret)
	failRes, err := svc.ProcessWebhook(t.Context(), service.WebhookInput{
		OrderID: orderID.String(), StatusCode: "200", GrossAmount: 350000, TransactionStatus: " EXPIRE ", SignatureKey: sig,
	})
	if err != nil || failRes.Outcome != domain.OutcomeProcessedFailed || ordRepo.lastFailReason != "gateway status: expire" {
		t.Fatalf("unexpected fail webhook result: res=%+v err=%v reason=%q", failRes, err, ordRepo.lastFailReason)
	}

	unkRes, err := svc.ProcessWebhook(t.Context(), service.WebhookInput{
		OrderID: orderID.String(), StatusCode: "200", GrossAmount: 350000, TransactionStatus: "pending", SignatureKey: sig,
	})
	if err != nil || unkRes.Outcome != domain.OutcomeIgnoredUnknownEvent {
		t.Fatalf("unexpected unknown status result: res=%+v err=%v", unkRes, err)
	}

	_, err = svc.PayOrderForDemo(t.Context(), domain.Actor{ID: uuid.New(), Role: domain.RoleBuyer}, orderID)
	assertDomainErr(t, err, http.StatusForbidden, domain.ErrCodeForbidden)

	payRes, err := svc.PayOrderForDemo(t.Context(), domain.Actor{ID: buyerID, Role: domain.RoleBuyer}, orderID)
	if err != nil || payRes.Outcome != domain.OutcomeProcessedPaid {
		t.Fatalf("PayOrderForDemo failed: res=%+v err=%v", payRes, err)
	}

	lastLog := whRepo.logged[len(whRepo.logged)-1]
	var decoded map[string]any
	if err := json.Unmarshal(lastLog.RawPayload, &decoded); err != nil {
		t.Fatalf("invalid PayOrderForDemo payload JSON: %v", err)
	}
	if decoded["order_id"] != orderID.String() {
		t.Fatalf("unexpected payload order_id: %v", decoded["order_id"])
	}
}

func TestPaymentService_FailPaymentErrorDoesNotRequireRefund(t *testing.T) {
	t.Parallel()

	const secret = "fail-err-secret"
	orderID := uuid.New()
	whRepo := &mockWebhookRepo{}
	ordRepo := &mockOrderRepo{failErr: domain.NewOrderExpiredError("expired")}
	svc := service.NewPaymentService(ordRepo, whRepo, newMockEventRepo(), secret)

	sig := domain.ComputeSHA512Signature(orderID.String(), "200", 100000, secret)
	_, err := svc.ProcessWebhook(t.Context(), service.WebhookInput{
		OrderID: orderID.String(), StatusCode: "200", GrossAmount: 100000, TransactionStatus: "deny", SignatureKey: sig,
	})
	assertDomainErr(t, err, http.StatusConflict, domain.ErrCodeOrderExpired)
	if len(whRepo.logged) != 1 || whRepo.logged[0].RequiresRefund || whRepo.logged[0].Outcome != domain.OutcomeProcessedFailed {
		t.Fatalf("expected PROCESSED_FAILED with RequiresRefund=false, got %+v", whRepo.logged[0])
	}
}

func TestPaymentService_DemoPayOrganizerOwnership(t *testing.T) {
	t.Parallel()

	const secret = "demo-org-secret"
	orderID := uuid.New()
	buyerID := uuid.New()
	orgOwnerID := uuid.New()
	otherOrgID := uuid.New()
	eventID := uuid.New()

	evRepo := newMockEventRepo()
	evRepo.events[eventID] = &domain.Event{ID: eventID, OrganizerID: orgOwnerID}
	ordRepo := &mockOrderRepo{
		foundOrder: &domain.Order{ID: orderID, EventID: eventID, BuyerID: buyerID, TotalAmountIDR: 150000},
		confirmRes: &repository.PaymentConfirmResult{Order: &domain.Order{ID: orderID, BuyerID: buyerID, Status: domain.OrderStatusPaid}},
	}
	svc := service.NewPaymentService(ordRepo, &mockWebhookRepo{}, evRepo, secret)

	resOwner, err := svc.PayOrderForDemo(t.Context(), domain.Actor{ID: orgOwnerID, Role: domain.RoleOrganizer}, orderID)
	if err != nil || resOwner.Outcome != domain.OutcomeProcessedPaid {
		t.Fatalf("event organizer PayOrderForDemo failed: res=%+v err=%v", resOwner, err)
	}

	_, err = svc.PayOrderForDemo(t.Context(), domain.Actor{ID: otherOrgID, Role: domain.RoleOrganizer}, orderID)
	assertDomainErr(t, err, http.StatusForbidden, domain.ErrCodeForbidden)

	boom := errors.New("event lookup error")
	errEvRepo := newMockEventRepo()
	errEvRepo.findErr = boom
	errSvc := service.NewPaymentService(ordRepo, &mockWebhookRepo{}, errEvRepo, secret)
	if _, err := errSvc.PayOrderForDemo(t.Context(), domain.Actor{ID: orgOwnerID, Role: domain.RoleOrganizer}, orderID); !errors.Is(err, boom) {
		t.Fatalf("expected FindByID error propagation, got %v", err)
	}
}

func TestPaymentService_NilConfirmResultLogsFailure(t *testing.T) {
	t.Parallel()

	const secret = "nil-confirm-secret"
	orderID := uuid.New()
	whRepo := &mockWebhookRepo{}
	ordRepo := &mockOrderRepo{confirmRes: nil, confirmErr: nil}
	svc := service.NewPaymentService(ordRepo, whRepo, newMockEventRepo(), secret)

	sig := domain.ComputeSHA512Signature(orderID.String(), "200", 100000, secret)
	_, err := svc.ProcessWebhook(t.Context(), service.WebhookInput{
		OrderID: orderID.String(), StatusCode: "200", GrossAmount: 100000, TransactionStatus: "settlement", SignatureKey: sig,
	})
	assertNonNilError(t, err)
	if len(whRepo.logged) != 1 || whRepo.logged[0].Outcome != domain.OutcomeProcessedFailed || whRepo.logged[0].RequiresRefund {
		t.Fatalf("expected PROCESSED_FAILED audit log without refund, got %+v", whRepo.logged)
	}
}

func TestDemoService_RunFlashSaleValidation(t *testing.T) {
	t.Parallel()

	var nilSvc *service.DemoService
	assertNonNilError(t, func() error { _, err := nilSvc.RunFlashSale(t.Context(), 10, 5); return err }())

	eRepo := newMockEventRepo()
	dSvc := service.NewDemoService(newMockUserRepo(), eRepo, service.NewOrderService(&mockOrderRepo{eventRepo: eRepo}, eRepo))

	_, err := dSvc.RunFlashSale(t.Context(), 501, 10)
	assertDomainErr(t, err, http.StatusUnprocessableEntity, domain.ErrCodeValidation)
	_, err = dSvc.RunFlashSale(t.Context(), 10, 501)
	assertDomainErr(t, err, http.StatusUnprocessableEntity, domain.ErrCodeValidation)
}

func TestDemoService_RunFlashSaleDefaultSpike(t *testing.T) {
	t.Parallel()

	eRepo := newMockEventRepo()
	dSvc := service.NewDemoService(newMockUserRepo(), eRepo, service.NewOrderService(&mockOrderRepo{eventRepo: eRepo}, eRepo))

	report, err := dSvc.RunFlashSale(t.Context(), 0, 0)
	if err != nil {
		t.Fatalf("RunFlashSale default spike failed: %v", err)
	}
	if report.ConcurrentRequests != 200 || report.SucceededBookings != 100 || report.RejectedQuotaEmpty != 100 {
		t.Fatalf("unexpected booking counts: %+v", report)
	}
	if report.RejectedOther != 0 || report.RemainingQuotaAfter != 0 || !report.ZeroOversell {
		t.Fatalf("unexpected oversell metrics: %+v", report)
	}
}

func TestDemoService_RunFlashSalePartialFill(t *testing.T) {
	t.Parallel()

	eRepo := newMockEventRepo()
	dSvc := service.NewDemoService(newMockUserRepo(), eRepo, service.NewOrderService(&mockOrderRepo{eventRepo: eRepo}, eRepo))

	report, err := dSvc.RunFlashSale(t.Context(), 30, 50)
	if err != nil {
		t.Fatalf("RunFlashSale partial fill failed: %v", err)
	}
	if report.ConcurrentRequests != 30 || report.SucceededBookings != 30 || report.RejectedQuotaEmpty != 0 {
		t.Fatalf("unexpected booking counts: %+v", report)
	}
	if report.RejectedOther != 0 || report.RemainingQuotaAfter != 20 || !report.ZeroOversell {
		t.Fatalf("unexpected oversell metrics: %+v", report)
	}
}

func TestService_RepositoryErrorPropagation(t *testing.T) {
	t.Parallel()

	boom := errors.New("db connection dropped")

	evRepo := newMockEventRepo()
	evSvc := service.NewEventService(evRepo, &mockOrderRepo{expireErr: boom})
	if _, err := evSvc.ListEvents(t.Context()); !errors.Is(err, boom) {
		t.Fatalf("ListEvents should propagate expire error, got %v", err)
	}
	if _, err := evSvc.GetEvent(t.Context(), uuid.New()); !errors.Is(err, boom) {
		t.Fatalf("GetEvent should propagate expire error, got %v", err)
	}
	evRepo.createErr = boom
	org := domain.Actor{ID: uuid.New(), Role: domain.RoleOrganizer}
	_, err := evSvc.CreateEvent(t.Context(), org, service.CreateEventInput{
		Title: "Go Conf", Venue: "Hall", EventDate: time.Now().Add(time.Hour),
		TicketTypes: []service.CreateTierInput{{Name: "Reg", PriceIDR: 100, TotalQuota: 10}, {Name: "VIP", PriceIDR: 200, TotalQuota: 10}},
	})
	if !errors.Is(err, boom) {
		t.Fatalf("CreateEvent should propagate repo error, got %v", err)
	}

	ordSvc := service.NewOrderService(&mockOrderRepo{expireErr: boom}, newMockEventRepo())
	if _, err := ordSvc.ListOrders(t.Context(), domain.Actor{ID: uuid.New(), Role: domain.RoleBuyer}); !errors.Is(err, boom) {
		t.Fatalf("ListOrders should propagate expire error, got %v", err)
	}
	ordSvcFindErr := service.NewOrderService(&mockOrderRepo{findErr: boom}, newMockEventRepo())
	if _, err := ordSvcFindErr.GetOrder(t.Context(), domain.Actor{ID: uuid.New(), Role: domain.RoleBuyer}, uuid.New()); !errors.Is(err, boom) {
		t.Fatalf("GetOrder should propagate find error, got %v", err)
	}
}

func TestPaymentService_AuditLogAndDemoGuards(t *testing.T) {
	t.Parallel()

	const secret = "audit-log-secret"
	orderID := uuid.New()
	eventID := uuid.New()
	orgOwnerID := uuid.New()
	sig := domain.ComputeSHA512Signature(orderID.String(), "200", 150000, secret)
	boom := errors.New("audit insert failed")

	evRepo := newMockEventRepo()
	evRepo.events[eventID] = &domain.Event{ID: eventID, OrganizerID: orgOwnerID}

	svc := service.NewPaymentService(
		&mockOrderRepo{confirmRes: &repository.PaymentConfirmResult{Order: &domain.Order{ID: orderID}}},
		&mockWebhookRepo{logErr: boom},
		evRepo,
		secret,
	)
	_, err := svc.ProcessWebhook(t.Context(), service.WebhookInput{
		OrderID: orderID.String(), StatusCode: "200", GrossAmount: 150000, TransactionStatus: "settlement", SignatureKey: sig,
	})
	if !errors.Is(err, boom) {
		t.Fatalf("settlement LogDelivery failure should return error, got %v", err)
	}

	_, err = svc.ProcessWebhook(t.Context(), service.WebhookInput{
		OrderID: orderID.String(), StatusCode: "200", GrossAmount: 150000, TransactionStatus: "expire", SignatureKey: sig,
	})
	if !errors.Is(err, boom) {
		t.Fatalf("fail status LogDelivery failure should return error, got %v", err)
	}

	_, err = svc.ProcessWebhook(t.Context(), service.WebhookInput{
		OrderID: orderID.String(), StatusCode: "200", GrossAmount: 150000, TransactionStatus: "pending", SignatureKey: sig,
	})
	if !errors.Is(err, boom) {
		t.Fatalf("unknown status LogDelivery failure should return error, got %v", err)
	}

	noSecretPay := service.NewPaymentService(&mockOrderRepo{}, &mockWebhookRepo{}, evRepo, "")
	_, err = noSecretPay.PayOrderForDemo(t.Context(), domain.Actor{ID: uuid.New(), Role: domain.RoleBuyer}, orderID)
	assertDomainErr(t, err, http.StatusUnauthorized, domain.ErrCodeUnauthorizedWebhook)

	validPay := service.NewPaymentService(&mockOrderRepo{
		foundOrder: &domain.Order{ID: orderID, EventID: eventID, BuyerID: uuid.New(), TotalAmountIDR: 150000},
		confirmRes: &repository.PaymentConfirmResult{Order: &domain.Order{ID: orderID, Status: domain.OrderStatusPaid}},
	}, &mockWebhookRepo{}, evRepo, secret)
	_, err = validPay.PayOrderForDemo(t.Context(), domain.Actor{ID: uuid.New(), Role: domain.RoleBuyer}, uuid.Nil)
	assertDomainErr(t, err, http.StatusUnprocessableEntity, domain.ErrCodeValidation)

	res, err := validPay.PayOrderForDemo(t.Context(), domain.Actor{ID: orgOwnerID, Role: domain.RoleOrganizer}, orderID)
	if err != nil || res.Outcome != domain.OutcomeProcessedPaid {
		t.Fatalf("organizer PayOrderForDemo failed: res=%+v err=%v", res, err)
	}
}

func TestDemoService_ErrorAndOtherRejections(t *testing.T) {
	t.Parallel()

	boom := errors.New("downstream failure")
	eRepo := newMockEventRepo()
	dSvc := service.NewDemoService(newMockUserRepo(), eRepo, service.NewOrderService(&mockOrderRepo{createErr: boom}, eRepo))

	report, err := dSvc.RunFlashSale(t.Context(), 5, 5)
	if err != nil {
		t.Fatalf("RunFlashSale should count order errors in RejectedOther, got %v", err)
	}
	if report.RejectedOther != 5 || report.ZeroOversell {
		t.Fatalf("expected RejectedOther=5 and ZeroOversell=false, got %+v", report)
	}

	badUsers := newMockUserRepo()
	badUsers.ensureErr = boom
	dFailUser := service.NewDemoService(badUsers, newMockEventRepo(), service.NewOrderService(&mockOrderRepo{}, newMockEventRepo()))
	if _, err := dFailUser.RunFlashSale(t.Context(), 5, 5); !errors.Is(err, boom) {
		t.Fatalf("expected EnsureDemoUser error, got %v", err)
	}

	badEvents := newMockEventRepo()
	badEvents.createErr = boom
	dFailEv := service.NewDemoService(newMockUserRepo(), badEvents, service.NewOrderService(&mockOrderRepo{}, badEvents))
	if _, err := dFailEv.RunFlashSale(t.Context(), 5, 5); !errors.Is(err, boom) {
		t.Fatalf("expected CreateWithTiers error, got %v", err)
	}
}

func TestDemoService_MissingQuotaLookupErrors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		override func(uuid.UUID) (*domain.Event, error)
	}{
		{
			name: "nil event returned",
			override: func(uuid.UUID) (*domain.Event, error) {
				return nil, nil
			},
		},
		{
			name: "target tier missing from event",
			override: func(id uuid.UUID) (*domain.Event, error) {
				return &domain.Event{
					ID:          id,
					TicketTypes: []domain.TicketType{{ID: uuid.New(), Name: "Other Tier"}},
				}, nil
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			eRepo := newMockEventRepo()
			eRepo.findOverride = tc.override
			dSvc := service.NewDemoService(newMockUserRepo(), eRepo, service.NewOrderService(&mockOrderRepo{eventRepo: eRepo}, eRepo))
			_, err := dSvc.RunFlashSale(t.Context(), 5, 5)
			assertNonNilError(t, err)
		})
	}
}
