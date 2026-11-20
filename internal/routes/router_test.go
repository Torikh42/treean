package routes_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/torikh42/treean/internal/domain"
	"github.com/torikh42/treean/internal/handler"
	"github.com/torikh42/treean/internal/repository"
	"github.com/torikh42/treean/internal/routes"
	"github.com/torikh42/treean/internal/service"
)

const (
	testJWTSecret     = "unit-test-jwt-secret-key"
	testWebhookSecret = "unit-test-webhook-secret-key"
)

type fakePinger struct {
	mu          sync.Mutex
	err         error
	hasDeadline bool
	calls       int
}

func (f *fakePinger) Ping(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	_, f.hasDeadline = ctx.Deadline()
	return f.err
}

type memUserRepo struct {
	mu            sync.Mutex
	usersByEmail  map[string]*domain.User
	usersByID     map[uuid.UUID]*domain.User
	createErr     error
	findEmailErr  error
	ensureDemoErr error
}

func newMemUserRepo() *memUserRepo {
	return &memUserRepo{
		usersByEmail: make(map[string]*domain.User),
		usersByID:    make(map[uuid.UUID]*domain.User),
	}
}

func (m *memUserRepo) Create(_ context.Context, u *domain.User) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.createErr != nil {
		return m.createErr
	}
	if _, exists := m.usersByEmail[u.Email]; exists {
		return domain.NewDomainError(http.StatusConflict, domain.ErrCodeEmailAlreadyExists, "email already registered")
	}
	cp := *u
	m.usersByEmail[u.Email] = &cp
	m.usersByID[u.ID] = &cp
	return nil
}

func (m *memUserRepo) FindByEmail(_ context.Context, email string) (*domain.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.findEmailErr != nil {
		return nil, m.findEmailErr
	}
	u, ok := m.usersByEmail[email]
	if !ok {
		return nil, domain.NewDomainError(http.StatusUnauthorized, domain.ErrCodeInvalidCredentials, "invalid email or password")
	}
	cp := *u
	return &cp, nil
}

func (m *memUserRepo) FindByID(_ context.Context, id uuid.UUID) (*domain.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.usersByID[id]
	if !ok {
		return nil, domain.NewDomainError(http.StatusUnauthorized, domain.ErrCodeUnauthorized, "user not found")
	}
	cp := *u
	return &cp, nil
}

func (m *memUserRepo) EnsureDemoUser(_ context.Context, id uuid.UUID, role domain.Role) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ensureDemoErr != nil {
		return m.ensureDemoErr
	}
	if _, ok := m.usersByID[id]; ok {
		return nil
	}
	email := fmt.Sprintf("demo-%s@event.local", id.String())
	u := &domain.User{
		ID:        id,
		Email:     email,
		Name:      "Demo " + string(role),
		Role:      role,
		CreatedAt: time.Now().UTC(),
	}
	m.usersByID[id] = u
	m.usersByEmail[email] = u
	return nil
}

type memEventRepo struct {
	mu             sync.Mutex
	events         map[uuid.UUID]*domain.Event
	tiers          map[uuid.UUID]*domain.TicketType
	eventHasOrders map[uuid.UUID]bool
	tierHasOrders  map[uuid.UUID]bool
	listErr        error
	findErr        error
}

func newMemEventRepo() *memEventRepo {
	return &memEventRepo{
		events:         make(map[uuid.UUID]*domain.Event),
		tiers:          make(map[uuid.UUID]*domain.TicketType),
		eventHasOrders: make(map[uuid.UUID]bool),
		tierHasOrders:  make(map[uuid.UUID]bool),
	}
}

func (m *memEventRepo) CreateWithTiers(_ context.Context, ev *domain.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *ev
	cp.CreatedAt = time.Now().UTC()
	cp.TicketTypes = slices.Clone(ev.TicketTypes)
	for i := range cp.TicketTypes {
		cp.TicketTypes[i].EventID = cp.ID
		cp.TicketTypes[i].CreatedAt = cp.CreatedAt
		tt := cp.TicketTypes[i]
		m.tiers[tt.ID] = &tt
	}
	m.events[cp.ID] = &cp
	return nil
}

func (m *memEventRepo) ListAll(_ context.Context) ([]domain.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.listErr != nil {
		return nil, m.listErr
	}
	out := make([]domain.Event, 0, len(m.events))
	for _, ev := range m.events {
		out = append(out, m.snapshotLocked(ev))
	}
	return out, nil
}

func (m *memEventRepo) ListByOrganizer(_ context.Context, organizerID uuid.UUID) ([]domain.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.listErr != nil {
		return nil, m.listErr
	}
	out := make([]domain.Event, 0)
	for _, ev := range m.events {
		if ev.OrganizerID == organizerID {
			out = append(out, m.snapshotLocked(ev))
		}
	}
	return out, nil
}

func (m *memEventRepo) FindByID(_ context.Context, id uuid.UUID) (*domain.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.findErr != nil {
		return nil, m.findErr
	}
	ev, ok := m.events[id]
	if !ok {
		return nil, domain.NewDomainError(http.StatusNotFound, domain.ErrCodeEventNotFound, "event not found")
	}
	snap := m.snapshotLocked(ev)
	return &snap, nil
}

func (m *memEventRepo) UpdateWithTiers(_ context.Context, organizerID uuid.UUID, ev *domain.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	stored, ok := m.events[ev.ID]
	if !ok {
		return domain.NewDomainError(http.StatusNotFound, domain.ErrCodeEventNotFound, "event not found")
	}
	if stored.OrganizerID != organizerID {
		return domain.NewDomainError(http.StatusForbidden, domain.ErrCodeForbidden, "cannot modify another organizer's event")
	}

	existing := m.existingTierMapLocked(stored.TicketTypes)
	nextTiers, kept, err := buildMemUpdatedTiers(stored.ID, existing, ev.TicketTypes)
	if err != nil {
		return err
	}
	if err := m.ensureRemovedTiersUnusedLocked(existing, kept); err != nil {
		return err
	}
	m.replaceTiersLocked(existing, kept, nextTiers)

	stored.Title = ev.Title
	stored.Description = ev.Description
	stored.Venue = ev.Venue
	stored.EventDate = ev.EventDate
	stored.TicketTypes = nextTiers

	snap := m.snapshotLocked(stored)
	*ev = snap
	return nil
}

func (m *memEventRepo) existingTierMapLocked(tiers []domain.TicketType) map[uuid.UUID]domain.TicketType {
	existing := make(map[uuid.UUID]domain.TicketType, len(tiers))
	for _, tt := range tiers {
		if live, exists := m.tiers[tt.ID]; exists {
			existing[tt.ID] = *live
		} else {
			existing[tt.ID] = tt
		}
	}
	return existing
}

func buildMemUpdatedTiers(
	eventID uuid.UUID,
	existing map[uuid.UUID]domain.TicketType,
	input []domain.TicketType,
) ([]domain.TicketType, map[uuid.UUID]struct{}, error) {
	kept := make(map[uuid.UUID]struct{}, len(input))
	nextTiers := make([]domain.TicketType, len(input))
	now := time.Now().UTC()
	for i, tt := range input {
		if tt.ID == uuid.Nil {
			tt.ID = uuid.New()
			tt.EventID = eventID
			tt.RemainingQuota = tt.TotalQuota
			tt.CreatedAt = now
			nextTiers[i] = tt
			continue
		}
		prev, exists := existing[tt.ID]
		if !exists {
			return nil, nil, domain.NewDomainError(http.StatusNotFound, domain.ErrCodeTicketTypeNotFound, "ticket type not found for event")
		}
		soldOrHeld := prev.TotalQuota - prev.RemainingQuota
		if tt.TotalQuota < soldOrHeld {
			return nil, nil, domain.NewDomainError(http.StatusConflict, domain.ErrCodeQuotaConflict, "total_quota below reserved or sold count")
		}
		tt.EventID = eventID
		tt.RemainingQuota = tt.TotalQuota - soldOrHeld
		tt.CreatedAt = prev.CreatedAt
		nextTiers[i] = tt
		kept[tt.ID] = struct{}{}
	}
	return nextTiers, kept, nil
}

func (m *memEventRepo) ensureRemovedTiersUnusedLocked(existing map[uuid.UUID]domain.TicketType, kept map[uuid.UUID]struct{}) error {
	for id := range existing {
		if _, isKept := kept[id]; !isKept && m.tierHasOrders[id] {
			return domain.NewDomainError(http.StatusConflict, domain.ErrCodeTierHasOrders, "cannot remove ticket tier with existing orders")
		}
	}
	return nil
}

func (m *memEventRepo) replaceTiersLocked(
	existing map[uuid.UUID]domain.TicketType,
	kept map[uuid.UUID]struct{},
	nextTiers []domain.TicketType,
) {
	for id := range existing {
		if _, isKept := kept[id]; !isKept {
			delete(m.tiers, id)
		}
	}
	for i := range nextTiers {
		tt := nextTiers[i]
		m.tiers[tt.ID] = &tt
	}
}

func (m *memEventRepo) DeleteUnused(_ context.Context, organizerID, eventID uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	stored, ok := m.events[eventID]
	if !ok {
		return domain.NewDomainError(http.StatusNotFound, domain.ErrCodeEventNotFound, "event not found")
	}
	if stored.OrganizerID != organizerID {
		return domain.NewDomainError(http.StatusForbidden, domain.ErrCodeForbidden, "cannot delete another organizer's event")
	}
	if m.eventHasOrders[eventID] {
		return domain.NewDomainError(http.StatusConflict, domain.ErrCodeEventHasOrders, "cannot delete event with existing orders")
	}
	for _, tt := range stored.TicketTypes {
		delete(m.tiers, tt.ID)
	}
	delete(m.events, eventID)
	return nil
}

func (m *memEventRepo) snapshotLocked(ev *domain.Event) domain.Event {
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

type memOrderRepo struct {
	mu      sync.Mutex
	events  *memEventRepo
	orders  map[uuid.UUID]*domain.Order
	seq     atomic.Int64
	panicOn bool
}

func newMemOrderRepo(events *memEventRepo) *memOrderRepo {
	return &memOrderRepo{
		events: events,
		orders: make(map[uuid.UUID]*domain.Order),
	}
}

func (m *memOrderRepo) CreateOrder(_ context.Context, eventID, buyerID uuid.UUID, items []repository.CheckoutItemInput, now time.Time) (*domain.Order, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events.mu.Lock()
	defer m.events.mu.Unlock()

	if _, ok := m.events.events[eventID]; !ok {
		return nil, domain.NewDomainError(http.StatusNotFound, domain.ErrCodeEventNotFound, "event not found")
	}

	activeTickets := m.activeTicketsLocked(eventID, buyerID)
	reqQty, totalAmount, orderItems, err := m.prepareItemsLocked(eventID, items)
	if err != nil {
		return nil, err
	}
	if activeTickets+reqQty > domain.MaxTicketsPerBuyerPerEvent {
		return nil, domain.NewBuyerLimitExceededError("buyer ticket limit exceeded")
	}

	for _, it := range items {
		m.events.tiers[it.TicketTypeID].RemainingQuota -= it.Quantity
		m.events.tierHasOrders[it.TicketTypeID] = true
	}
	m.events.eventHasOrders[eventID] = true

	orderID := uuid.New()
	for i := range orderItems {
		orderItems[i].OrderID = orderID
	}
	seq := m.seq.Add(1)
	ord := &domain.Order{
		ID:             orderID,
		InvoiceNumber:  fmt.Sprintf("INV-TEST-%05d", seq),
		EventID:        eventID,
		BuyerID:        buyerID,
		TotalTickets:   reqQty,
		TotalAmountIDR: totalAmount,
		Status:         domain.OrderStatusWaitingPayment,
		ExpiresAt:      now.Add(domain.OrderHoldDuration),
		CreatedAt:      now,
		Items:          orderItems,
	}
	m.orders[orderID] = ord
	return cloneOrder(ord), nil
}

func (m *memOrderRepo) activeTicketsLocked(eventID, buyerID uuid.UUID) int32 {
	var total int32
	for _, o := range m.orders {
		if o.EventID == eventID && o.BuyerID == buyerID &&
			(o.Status == domain.OrderStatusWaitingPayment || o.Status == domain.OrderStatusPaid) {
			total += o.TotalTickets
		}
	}
	return total
}

func (m *memOrderRepo) prepareItemsLocked(eventID uuid.UUID, items []repository.CheckoutItemInput) (int32, int64, []domain.OrderItem, error) {
	var reqQty int32
	var totalAmount int64
	orderItems := make([]domain.OrderItem, 0, len(items))
	for _, it := range items {
		tt, ok := m.events.tiers[it.TicketTypeID]
		if !ok || tt.EventID != eventID {
			return 0, 0, nil, domain.NewDomainError(http.StatusNotFound, domain.ErrCodeTicketTypeNotFound, "ticket type not found")
		}
		if tt.RemainingQuota < it.Quantity {
			return 0, 0, nil, domain.NewQuotaExhaustedError("ticket quota exhausted")
		}
		reqQty += it.Quantity
		sub := tt.PriceIDR * int64(it.Quantity)
		totalAmount += sub
		orderItems = append(orderItems, domain.OrderItem{
			ID:             uuid.New(),
			TicketTypeID:   tt.ID,
			TicketTypeName: tt.Name,
			Quantity:       it.Quantity,
			UnitPriceIDR:   tt.PriceIDR,
			SubtotalIDR:    sub,
		})
	}
	return reqQty, totalAmount, orderItems, nil
}

func (m *memOrderRepo) CancelOrder(_ context.Context, orderID, buyerID uuid.UUID, now time.Time) (*domain.Order, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events.mu.Lock()
	defer m.events.mu.Unlock()

	ord, ok := m.orders[orderID]
	if !ok {
		return nil, domain.NewDomainError(http.StatusNotFound, domain.ErrCodeOrderNotFound, "order not found")
	}
	if ord.BuyerID != buyerID {
		return nil, domain.NewDomainError(http.StatusForbidden, domain.ErrCodeForbidden, "cannot cancel another buyer's order")
	}
	if err := ord.CanCancel(now); err != nil {
		return nil, err
	}
	for _, it := range ord.Items {
		if tt, exists := m.events.tiers[it.TicketTypeID]; exists {
			tt.RemainingQuota += it.Quantity
		}
	}
	ord.Status = domain.OrderStatusCancelled
	ord.CancelledAt = &now
	return cloneOrder(ord), nil
}

func (m *memOrderRepo) ConfirmPayment(_ context.Context, orderID uuid.UUID, grossAmount int64, now time.Time) (*repository.PaymentConfirmResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	ord, ok := m.orders[orderID]
	if !ok {
		return nil, domain.NewDomainError(http.StatusNotFound, domain.ErrCodeOrderNotFound, "order not found")
	}
	if ord.Status == domain.OrderStatusPaid {
		return &repository.PaymentConfirmResult{Order: cloneOrder(ord), IdempotentReplay: true}, nil
	}
	if err := ord.CanPay(now); err != nil {
		return nil, err
	}
	if ord.TotalAmountIDR != grossAmount {
		return nil, domain.NewPaymentAmountMismatchError("gross_amount does not match order total")
	}

	ord.Status = domain.OrderStatusPaid
	ord.PaidAt = &now
	for _, it := range ord.Items {
		for seat := range it.Quantity {
			ord.Tickets = append(ord.Tickets, domain.Ticket{
				ID:           uuid.New(),
				TicketCode:   fmt.Sprintf("TKT-%s-%d", ord.InvoiceNumber, seat+1),
				OrderID:      ord.ID,
				OrderItemID:  it.ID,
				EventID:      ord.EventID,
				TicketTypeID: it.TicketTypeID,
				OwnerID:      ord.BuyerID,
				SeatSequence: seat + 1,
				IssuedAt:     now,
			})
		}
	}
	return &repository.PaymentConfirmResult{Order: cloneOrder(ord), IdempotentReplay: false}, nil
}

func (m *memOrderRepo) FailPayment(_ context.Context, orderID uuid.UUID, reason string, now time.Time) (domain.WebhookOutcome, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ord, ok := m.orders[orderID]
	if !ok {
		return "", domain.NewDomainError(http.StatusNotFound, domain.ErrCodeOrderNotFound, "order not found")
	}
	if ord.Status == domain.OrderStatusWaitingPayment {
		ord.Status = domain.OrderStatusCancelled
		ord.CancelledAt = &now
		ord.CancelledReason = &reason
	}
	return domain.OutcomeProcessedFailed, nil
}

func (m *memOrderRepo) ExpireDueOrders(_ context.Context, _ *uuid.UUID, _ int, _ string, _ time.Time) (int, error) {
	return 0, nil
}

func (m *memOrderRepo) FindByID(_ context.Context, orderID uuid.UUID) (*domain.Order, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.panicOn {
		panic("simulated repo panic")
	}
	ord, ok := m.orders[orderID]
	if !ok {
		return nil, domain.NewDomainError(http.StatusNotFound, domain.ErrCodeOrderNotFound, "order not found")
	}
	return cloneOrder(ord), nil
}

func (m *memOrderRepo) ListByBuyer(_ context.Context, buyerID uuid.UUID) ([]domain.Order, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Order
	for _, o := range m.orders {
		if o.BuyerID == buyerID {
			out = append(out, *cloneOrder(o))
		}
	}
	return out, nil
}

func cloneOrder(o *domain.Order) *domain.Order {
	cp := *o
	cp.Items = slices.Clone(o.Items)
	cp.Tickets = slices.Clone(o.Tickets)
	cp.Logs = slices.Clone(o.Logs)
	return &cp
}

type memWebhookRepo struct {
	mu        sync.Mutex
	delivered []*domain.PaymentWebhook
}

func (m *memWebhookRepo) LogDelivery(_ context.Context, w *domain.PaymentWebhook) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *w
	m.delivered = append(m.delivered, &cp)
	return nil
}

type testHarness struct {
	router   http.Handler
	pinger   *fakePinger
	users    *memUserRepo
	events   *memEventRepo
	orders   *memOrderRepo
	webhooks *memWebhookRepo
}

func newTestHarness(allowDemo bool) *testHarness {
	pinger := &fakePinger{}
	users := newMemUserRepo()
	events := newMemEventRepo()
	orders := newMemOrderRepo(events)
	webhooks := &memWebhookRepo{}

	authSvc := service.NewAuthService(users, testJWTSecret)
	eventSvc := service.NewEventService(events, orders)
	orderSvc := service.NewOrderService(orders, events)
	paymentSvc := service.NewPaymentService(orders, webhooks, events, testWebhookSecret)
	demoSvc := service.NewDemoService(users, events, orderSvc)

	logger := slog.New(slog.DiscardHandler)
	r := routes.NewRouter(routes.RouterDeps{
		Logger:              logger,
		Authenticator:       authSvc,
		System:              handler.NewSystemHandler(pinger, logger),
		Auth:                handler.NewAuthHandler(authSvc, logger),
		Events:              handler.NewEventHandler(eventSvc, logger),
		Orders:              handler.NewOrderHandler(orderSvc, logger),
		Webhooks:            handler.NewWebhookHandler(paymentSvc, logger),
		Demo:                handler.NewDemoHandler(demoSvc, logger),
		AllowDemoHeaderAuth: allowDemo,
	})
	return &testHarness{
		router:   r,
		pinger:   pinger,
		users:    users,
		events:   events,
		orders:   orders,
		webhooks: webhooks,
	}
}

func doJSONReq(t *testing.T, r http.Handler, method, path string, body any, headers ...[2]string) *httptest.ResponseRecorder {
	t.Helper()
	var bodyReader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		bodyReader = bytes.NewReader(raw)
	}
	req := httptest.NewRequestWithContext(t.Context(), method, path, bodyReader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, h := range headers {
		req.Header.Set(h[0], h[1])
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func assertAPIError(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int, wantCode string) {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("status = %d, want %d (body=%s)", rec.Code, wantStatus, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("expected JSON error body, got %q: %v", rec.Body.String(), err)
	}
	if payload["error_code"] != wantCode {
		t.Fatalf("error_code = %v, want %q (body=%s)", payload["error_code"], wantCode, rec.Body.String())
	}
	statusVal, ok := payload["status"].(float64)
	if !ok || int(statusVal) != wantStatus {
		t.Fatalf("body status = %v, want %d", payload["status"], wantStatus)
	}
	detail, ok := payload["detail"].(string)
	if !ok || strings.TrimSpace(detail) == "" {
		t.Fatalf("expected non-empty error detail in %+v", payload)
	}
}

func registerTestUser(t *testing.T, r http.Handler, email, name, role string) service.AuthResult {
	t.Helper()
	rec := doJSONReq(t, r, http.MethodPost, "/api/v1/auth/register", map[string]any{
		"email":    email,
		"name":     name,
		"password": "password123",
		"role":     role,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("register %s status = %d, want 201 (body=%s)", email, rec.Code, rec.Body.String())
	}
	var out service.AuthResult
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode register output: %v", err)
	}
	return out
}

func createTestEvent(t *testing.T, r http.Handler, token string) domain.Event {
	t.Helper()
	rec := doJSONReq(t, r, http.MethodPost, "/api/v1/events", map[string]any{
		"title":      "Go Concurrency Conf",
		"venue":      "Jakarta Convention Center",
		"event_date": time.Now().UTC().Add(72 * time.Hour).Format(time.RFC3339),
		"ticket_types": []map[string]any{
			{"name": "Reguler", "price_idr": 150000, "total_quota": 50},
			{"name": "VIP", "price_idr": 350000, "total_quota": 10},
		},
	}, [2]string{"Authorization", "Bearer " + token})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create event status = %d, want 201 (body=%s)", rec.Code, rec.Body.String())
	}
	var ev domain.Event
	if err := json.Unmarshal(rec.Body.Bytes(), &ev); err != nil {
		t.Fatalf("decode created event: %v", err)
	}
	return ev
}

func createTestOrder(t *testing.T, r http.Handler, token string, eventID, tierID uuid.UUID, qty int32) domain.Order {
	t.Helper()
	rec := doJSONReq(t, r, http.MethodPost, "/api/v1/orders", map[string]any{
		"event_id": eventID.String(),
		"items": []map[string]any{
			{"ticket_type_id": tierID.String(), "quantity": qty},
		},
	}, [2]string{"Authorization", "Bearer " + token})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create order status = %d, want 201 (body=%s)", rec.Code, rec.Body.String())
	}
	var ord domain.Order
	if err := json.Unmarshal(rec.Body.Bytes(), &ord); err != nil {
		t.Fatalf("decode order: %v", err)
	}
	return ord
}

func TestRouter_Health(t *testing.T) {
	t.Parallel()

	h := newTestHarness(false)
	rec := doJSONReq(t, h.router, http.MethodGet, "/health", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok"`) {
		t.Fatalf("GET /health = %d %s, want 200 ok", rec.Code, rec.Body.String())
	}
	if !h.pinger.hasDeadline {
		t.Fatal("expected /health DB ping to set timeout context")
	}

	h.pinger.mu.Lock()
	h.pinger.err = errors.New("connection refused")
	h.pinger.mu.Unlock()
	recFail := doJSONReq(t, h.router, http.MethodGet, "/health", nil)
	if recFail.Code != http.StatusServiceUnavailable || !strings.Contains(recFail.Body.String(), `"unhealthy"`) {
		t.Fatalf("GET /health on error = %d %s, want 503 unhealthy", recFail.Code, recFail.Body.String())
	}

	nilDBRouter := routes.NewRouter(routes.RouterDeps{})
	recNilDB := doJSONReq(t, nilDBRouter, http.MethodGet, "/health", nil)
	if recNilDB.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /health with nil DB = %d, want 503", recNilDB.Code)
	}
}

func TestRouter_DocsDemoAndOpenAPI(t *testing.T) {
	t.Parallel()

	h := newTestHarness(false)

	demoRec := doJSONReq(t, h.router, http.MethodGet, "/demo", nil)
	if demoRec.Code != http.StatusOK {
		t.Fatalf("GET /demo status = %d, want 200", demoRec.Code)
	}
	if ct := demoRec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("GET /demo Content-Type = %q, want text/html", ct)
	}
	demoHTML := demoRec.Body.String()
	for _, wantLink := range []string{`href="/docs"`, `href="/openapi.json"`, `href="/health"`} {
		if !strings.Contains(demoHTML, wantLink) {
			t.Fatalf("demo.html missing expected link %s", wantLink)
		}
	}
	if strings.Contains(demoHTML, "\u2014") {
		t.Fatal("demo.html must not contain em dash")
	}

	docsRec := doJSONReq(t, h.router, http.MethodGet, "/docs", nil)
	if docsRec.Code != http.StatusOK || !strings.Contains(docsRec.Body.String(), "@scalar/api-reference") {
		t.Fatalf("GET /docs = %d, expected Scalar HTML", docsRec.Code)
	}

	specRec := doJSONReq(t, h.router, http.MethodGet, "/openapi.json", nil)
	if specRec.Code != http.StatusOK {
		t.Fatalf("GET /openapi.json status = %d, want 200", specRec.Code)
	}
	var spec struct {
		Paths map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(specRec.Body.Bytes(), &spec); err != nil {
		t.Fatalf("invalid openapi.json: %v", err)
	}
	expectedPaths := []string{
		"/api/v1/auth/register",
		"/api/v1/auth/login",
		"/api/v1/auth/me",
		"/api/v1/events",
		"/api/v1/events/{id}",
		"/api/v1/organizer/events",
		"/api/v1/orders",
		"/api/v1/orders/{id}",
		"/api/v1/orders/{id}/cancel",
		"/api/v1/orders/{id}/pay",
		"/api/v1/webhooks/payment",
		"/api/v1/demo/flash-sale",
	}
	for _, p := range expectedPaths {
		if _, ok := spec.Paths[p]; !ok {
			t.Fatalf("openapi.json missing path %s", p)
		}
	}
	eventItemOps, ok := spec.Paths["/api/v1/events/{id}"].(map[string]any)
	if !ok {
		t.Fatalf("expected map for /api/v1/events/{id}, got %T", spec.Paths["/api/v1/events/{id}"])
	}
	for _, method := range []string{"get", "put", "delete"} {
		if _, hasMethod := eventItemOps[method]; !hasMethod {
			t.Fatalf("openapi.json missing %s on /api/v1/events/{id}", method)
		}
	}
}

func TestRouter_AuthAndEventsFlow(t *testing.T) {
	t.Parallel()

	h := newTestHarness(false)
	orgAuth := registerTestUser(t, h.router, "org@event.local", "Org One", "ORGANIZER")
	buyerAuth := registerTestUser(t, h.router, "buyer@event.local", "Buyer One", "BUYER")

	dupRegRec := doJSONReq(t, h.router, http.MethodPost, "/api/v1/auth/register", map[string]any{
		"email":    "org@event.local",
		"name":     "Dup Org",
		"password": "password123",
		"role":     "ORGANIZER",
	})
	assertAPIError(t, dupRegRec, http.StatusConflict, domain.ErrCodeEmailAlreadyExists)

	loginRec := doJSONReq(t, h.router, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"email":    "org@event.local",
		"password": "password123",
	})
	if loginRec.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200 (body=%s)", loginRec.Code, loginRec.Body.String())
	}

	badLoginRec := doJSONReq(t, h.router, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"email":    "org@event.local",
		"password": "wrong-password",
	})
	assertAPIError(t, badLoginRec, http.StatusUnauthorized, domain.ErrCodeInvalidCredentials)

	badRegRec := doJSONReq(t, h.router, http.MethodPost, "/api/v1/auth/register", map[string]any{
		"email":    "not-email",
		"name":     "A",
		"password": "12",
		"role":     "INVALID",
	})
	assertAPIError(t, badRegRec, http.StatusUnprocessableEntity, domain.ErrCodeValidation)

	unauthEventRec := doJSONReq(t, h.router, http.MethodPost, "/api/v1/events", map[string]any{
		"title":      "Unauth Event",
		"venue":      "Hall",
		"event_date": time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339),
		"ticket_types": []map[string]any{
			{"name": "Tier A", "price_idr": 10000, "total_quota": 10},
			{"name": "Tier B", "price_idr": 20000, "total_quota": 10},
		},
	})
	assertAPIError(t, unauthEventRec, http.StatusUnauthorized, domain.ErrCodeUnauthorized)

	buyerCreateRec := doJSONReq(t, h.router, http.MethodPost, "/api/v1/events", map[string]any{
		"title":      "Blocked Event",
		"venue":      "Hall",
		"event_date": time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339),
		"ticket_types": []map[string]any{
			{"name": "Tier A", "price_idr": 10000, "total_quota": 10},
			{"name": "Tier B", "price_idr": 20000, "total_quota": 10},
		},
	}, [2]string{"Authorization", "Bearer " + buyerAuth.AccessToken})
	assertAPIError(t, buyerCreateRec, http.StatusForbidden, domain.ErrCodeForbidden)

	ev := createTestEvent(t, h.router, orgAuth.AccessToken)
	listRec := doJSONReq(t, h.router, http.MethodGet, "/api/v1/events", nil)
	if listRec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/events status = %d, want 200", listRec.Code)
	}
	var listed []domain.Event
	if err := json.Unmarshal(listRec.Body.Bytes(), &listed); err != nil || len(listed) != 1 {
		t.Fatalf("unexpected event list %s (err=%v)", listRec.Body.String(), err)
	}

	getRec := doJSONReq(t, h.router, http.MethodGet, "/api/v1/events/"+ev.ID.String(), nil)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/events/{id} status = %d, want 200", getRec.Code)
	}

	assertAPIError(t, doJSONReq(t, h.router, http.MethodGet, "/api/v1/events/not-a-uuid", nil), http.StatusBadRequest, domain.ErrCodeValidation)
	assertAPIError(t, doJSONReq(t, h.router, http.MethodGet, "/api/v1/events/"+uuid.Nil.String(), nil), http.StatusBadRequest, domain.ErrCodeValidation)
	assertAPIError(t, doJSONReq(t, h.router, http.MethodGet, "/api/v1/events/"+uuid.New().String(), nil), http.StatusNotFound, domain.ErrCodeEventNotFound)
}

func TestRouter_AuthMe(t *testing.T) {
	t.Parallel()

	h := newTestHarness(false)
	buyer := registerTestUser(t, h.router, "buyer-me@event.local", "Buyer Me", "BUYER")

	assertAPIError(t, doJSONReq(t, h.router, http.MethodGet, "/api/v1/auth/me", nil), http.StatusUnauthorized, domain.ErrCodeUnauthorized)
	meRec := doJSONReq(t, h.router, http.MethodGet, "/api/v1/auth/me", nil, [2]string{"Authorization", "Bearer " + buyer.AccessToken})
	if meRec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/auth/me status = %d, want 200 (body=%s)", meRec.Code, meRec.Body.String())
	}
	if strings.Contains(meRec.Body.String(), "password_hash") {
		t.Fatalf("GET /api/v1/auth/me leaked password_hash: %s", meRec.Body.String())
	}
	var me domain.User
	if err := json.Unmarshal(meRec.Body.Bytes(), &me); err != nil || me.ID != buyer.User.ID || me.Role != domain.RoleBuyer {
		t.Fatalf("unexpected /auth/me user: %+v (err=%v)", me, err)
	}
}

func TestRouter_OrganizerEventsList(t *testing.T) {
	t.Parallel()

	h := newTestHarness(false)
	org1 := registerTestUser(t, h.router, "org1-list@event.local", "Org One", "ORGANIZER")
	org2 := registerTestUser(t, h.router, "org2-list@event.local", "Org Two", "ORGANIZER")
	buyer := registerTestUser(t, h.router, "buyer-list@event.local", "Buyer One", "BUYER")

	ev1 := createTestEvent(t, h.router, org1.AccessToken)
	_ = createTestEvent(t, h.router, org2.AccessToken)

	assertAPIError(t, doJSONReq(t, h.router, http.MethodGet, "/api/v1/organizer/events", nil, [2]string{"Authorization", "Bearer " + buyer.AccessToken}), http.StatusForbidden, domain.ErrCodeForbidden)
	orgListRec := doJSONReq(t, h.router, http.MethodGet, "/api/v1/organizer/events", nil, [2]string{"Authorization", "Bearer " + org1.AccessToken})
	if orgListRec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/organizer/events status = %d, want 200 (body=%s)", orgListRec.Code, orgListRec.Body.String())
	}
	var orgEvents []domain.Event
	if err := json.Unmarshal(orgListRec.Body.Bytes(), &orgEvents); err != nil || len(orgEvents) != 1 || orgEvents[0].ID != ev1.ID {
		t.Fatalf("GET /api/v1/organizer/events = %+v (err=%v), want only ev1", orgEvents, err)
	}
}

func TestRouter_UpdateEvent(t *testing.T) {
	t.Parallel()

	h := newTestHarness(false)
	org1 := registerTestUser(t, h.router, "org1-upd@event.local", "Org One", "ORGANIZER")
	org2 := registerTestUser(t, h.router, "org2-upd@event.local", "Org Two", "ORGANIZER")
	buyer := registerTestUser(t, h.router, "buyer-upd@event.local", "Buyer One", "BUYER")

	ev1 := createTestEvent(t, h.router, org1.AccessToken)
	regID := ev1.TicketTypes[0].ID
	vipID := ev1.TicketTypes[1].ID
	_ = createTestOrder(t, h.router, buyer.AccessToken, ev1.ID, regID, 2)

	future := time.Now().UTC().Add(96 * time.Hour).Format(time.RFC3339)

	t.Run("authorization and not found guards", func(t *testing.T) {
		body := map[string]any{
			"title": "Blocked Edit", "venue": "Hall", "event_date": future,
			"ticket_types": []map[string]any{
				{"id": regID.String(), "name": "Reguler", "price_idr": 150000, "total_quota": 50},
				{"id": vipID.String(), "name": "VIP", "price_idr": 350000, "total_quota": 10},
			},
		}
		assertAPIError(t, doJSONReq(t, h.router, http.MethodPut, "/api/v1/events/"+ev1.ID.String(), body, [2]string{"Authorization", "Bearer " + buyer.AccessToken}), http.StatusForbidden, domain.ErrCodeForbidden)
		assertAPIError(t, doJSONReq(t, h.router, http.MethodPut, "/api/v1/events/"+ev1.ID.String(), body, [2]string{"Authorization", "Bearer " + org2.AccessToken}), http.StatusForbidden, domain.ErrCodeForbidden)

		missingBody := map[string]any{
			"title": "Missing Event", "venue": "Hall", "event_date": future,
			"ticket_types": []map[string]any{
				{"name": "Reguler", "price_idr": 150000, "total_quota": 50},
				{"name": "VIP", "price_idr": 350000, "total_quota": 10},
			},
		}
		assertAPIError(t, doJSONReq(t, h.router, http.MethodPut, "/api/v1/events/"+uuid.New().String(), missingBody, [2]string{"Authorization", "Bearer " + org1.AccessToken}), http.StatusNotFound, domain.ErrCodeEventNotFound)
	})

	t.Run("quota conflict and ordered tier removal guards", func(t *testing.T) {
		assertAPIError(t, doJSONReq(t, h.router, http.MethodPut, "/api/v1/events/"+ev1.ID.String(), map[string]any{
			"title": "Shrink Quota", "venue": "Hall", "event_date": future,
			"ticket_types": []map[string]any{
				{"id": regID.String(), "name": "Reguler", "price_idr": 150000, "total_quota": 1},
				{"id": vipID.String(), "name": "VIP", "price_idr": 350000, "total_quota": 10},
			},
		}, [2]string{"Authorization", "Bearer " + org1.AccessToken}), http.StatusConflict, domain.ErrCodeQuotaConflict)

		assertAPIError(t, doJSONReq(t, h.router, http.MethodPut, "/api/v1/events/"+ev1.ID.String(), map[string]any{
			"title": "Drop Ordered Tier", "venue": "Hall", "event_date": future,
			"ticket_types": []map[string]any{
				{"id": vipID.String(), "name": "VIP", "price_idr": 350000, "total_quota": 10},
				{"name": "VVIP", "price_idr": 500000, "total_quota": 5},
			},
		}, [2]string{"Authorization", "Bearer " + org1.AccessToken}), http.StatusConflict, domain.ErrCodeTierHasOrders)
	})

	t.Run("owner updates metadata and tiers", func(t *testing.T) {
		updRec := doJSONReq(t, h.router, http.MethodPut, "/api/v1/events/"+ev1.ID.String(), map[string]any{
			"title": "Go Concurrency Conf 2.0", "description": "Updated desc", "venue": "ICE BSD", "event_date": future,
			"ticket_types": []map[string]any{
				{"id": regID.String(), "name": "Reguler", "price_idr": 175000, "total_quota": 60},
				{"name": "VVIP", "price_idr": 550000, "total_quota": 15},
			},
		}, [2]string{"Authorization", "Bearer " + org1.AccessToken})
		if updRec.Code != http.StatusOK {
			t.Fatalf("PUT /api/v1/events/{id} status = %d, want 200 (body=%s)", updRec.Code, updRec.Body.String())
		}
		var updatedEv domain.Event
		if err := json.Unmarshal(updRec.Body.Bytes(), &updatedEv); err != nil {
			t.Fatalf("decode updated event: %v", err)
		}
		if updatedEv.Title != "Go Concurrency Conf 2.0" || len(updatedEv.TicketTypes) != 2 {
			t.Fatalf("unexpected updated event header: %+v", updatedEv)
		}
		if updatedEv.TicketTypes[0].RemainingQuota != 58 || updatedEv.TicketTypes[1].RemainingQuota != 15 {
			t.Fatalf("unexpected updated tier quotas: %+v", updatedEv.TicketTypes)
		}
	})
}

func TestRouter_DeleteEvent(t *testing.T) {
	t.Parallel()

	h := newTestHarness(false)
	org1 := registerTestUser(t, h.router, "org1-del@event.local", "Org One", "ORGANIZER")
	org2 := registerTestUser(t, h.router, "org2-del@event.local", "Org Two", "ORGANIZER")
	buyer := registerTestUser(t, h.router, "buyer-del@event.local", "Buyer One", "BUYER")

	usedEv := createTestEvent(t, h.router, org1.AccessToken)
	_ = createTestOrder(t, h.router, buyer.AccessToken, usedEv.ID, usedEv.TicketTypes[0].ID, 1)

	assertAPIError(t, doJSONReq(t, h.router, http.MethodDelete, "/api/v1/events/"+usedEv.ID.String(), nil, [2]string{"Authorization", "Bearer " + buyer.AccessToken}), http.StatusForbidden, domain.ErrCodeForbidden)
	assertAPIError(t, doJSONReq(t, h.router, http.MethodDelete, "/api/v1/events/"+usedEv.ID.String(), nil, [2]string{"Authorization", "Bearer " + org2.AccessToken}), http.StatusForbidden, domain.ErrCodeForbidden)
	assertAPIError(t, doJSONReq(t, h.router, http.MethodDelete, "/api/v1/events/"+uuid.New().String(), nil, [2]string{"Authorization", "Bearer " + org1.AccessToken}), http.StatusNotFound, domain.ErrCodeEventNotFound)
	assertAPIError(t, doJSONReq(t, h.router, http.MethodDelete, "/api/v1/events/"+usedEv.ID.String(), nil, [2]string{"Authorization", "Bearer " + org1.AccessToken}), http.StatusConflict, domain.ErrCodeEventHasOrders)

	unusedEv := createTestEvent(t, h.router, org1.AccessToken)
	delRec := doJSONReq(t, h.router, http.MethodDelete, "/api/v1/events/"+unusedEv.ID.String(), nil, [2]string{"Authorization", "Bearer " + org1.AccessToken})
	if delRec.Code != http.StatusNoContent || delRec.Body.Len() != 0 {
		t.Fatalf("DELETE unused event = (%d, %q), want 204 empty body", delRec.Code, delRec.Body.String())
	}
	assertAPIError(t, doJSONReq(t, h.router, http.MethodGet, "/api/v1/events/"+unusedEv.ID.String(), nil), http.StatusNotFound, domain.ErrCodeEventNotFound)
}

func TestRouter_OrdersFlow(t *testing.T) {
	t.Parallel()

	h := newTestHarness(false)
	orgAuth := registerTestUser(t, h.router, "org-ord@event.local", "Org Orders", "ORGANIZER")
	buyer1 := registerTestUser(t, h.router, "b1-ord@event.local", "Buyer 1", "BUYER")
	buyer2 := registerTestUser(t, h.router, "b2-ord@event.local", "Buyer 2", "BUYER")
	ev := createTestEvent(t, h.router, orgAuth.AccessToken)

	nilEventOrder := doJSONReq(t, h.router, http.MethodPost, "/api/v1/orders", map[string]any{
		"event_id": uuid.Nil.String(),
		"items":    []map[string]any{{"ticket_type_id": ev.TicketTypes[0].ID.String(), "quantity": 1}},
	}, [2]string{"Authorization", "Bearer " + buyer1.AccessToken})
	assertAPIError(t, nilEventOrder, http.StatusBadRequest, domain.ErrCodeValidation)

	nilTierOrder := doJSONReq(t, h.router, http.MethodPost, "/api/v1/orders", map[string]any{
		"event_id": ev.ID.String(),
		"items":    []map[string]any{{"ticket_type_id": uuid.Nil.String(), "quantity": 1}},
	}, [2]string{"Authorization", "Bearer " + buyer1.AccessToken})
	assertAPIError(t, nilTierOrder, http.StatusBadRequest, domain.ErrCodeValidation)

	ord1 := createTestOrder(t, h.router, buyer1.AccessToken, ev.ID, ev.TicketTypes[0].ID, 2)

	listRec := doJSONReq(t, h.router, http.MethodGet, "/api/v1/orders", nil, [2]string{"Authorization", "Bearer " + buyer1.AccessToken})
	if listRec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/orders status = %d, want 200", listRec.Code)
	}
	getOwnRec := doJSONReq(t, h.router, http.MethodGet, "/api/v1/orders/"+ord1.ID.String(), nil, [2]string{"Authorization", "Bearer " + buyer1.AccessToken})
	if getOwnRec.Code != http.StatusOK {
		t.Fatalf("GET own order status = %d, want 200", getOwnRec.Code)
	}
	getOrgRec := doJSONReq(t, h.router, http.MethodGet, "/api/v1/orders/"+ord1.ID.String(), nil, [2]string{"Authorization", "Bearer " + orgAuth.AccessToken})
	if getOrgRec.Code != http.StatusOK {
		t.Fatalf("GET order as event organizer status = %d, want 200", getOrgRec.Code)
	}

	getOtherRec := doJSONReq(t, h.router, http.MethodGet, "/api/v1/orders/"+ord1.ID.String(), nil, [2]string{"Authorization", "Bearer " + buyer2.AccessToken})
	assertAPIError(t, getOtherRec, http.StatusForbidden, domain.ErrCodeForbidden)

	assertAPIError(t, doJSONReq(t, h.router, http.MethodGet, "/api/v1/orders/not-a-uuid", nil, [2]string{"Authorization", "Bearer " + buyer1.AccessToken}), http.StatusBadRequest, domain.ErrCodeValidation)
	assertAPIError(t, doJSONReq(t, h.router, http.MethodPost, "/api/v1/orders/not-a-uuid/cancel", nil, [2]string{"Authorization", "Bearer " + buyer1.AccessToken}), http.StatusBadRequest, domain.ErrCodeValidation)

	cancelRec := doJSONReq(t, h.router, http.MethodPost, "/api/v1/orders/"+ord1.ID.String()+"/cancel", nil, [2]string{"Authorization", "Bearer " + buyer1.AccessToken})
	if cancelRec.Code != http.StatusOK {
		t.Fatalf("cancel order status = %d, want 200 (body=%s)", cancelRec.Code, cancelRec.Body.String())
	}
}

func TestRouter_WebhooksAndDemoPayFlow(t *testing.T) {
	t.Parallel()

	h := newTestHarness(false)
	orgAuth := registerTestUser(t, h.router, "org-pay@event.local", "Org Pay", "ORGANIZER")
	buyer1 := registerTestUser(t, h.router, "b1-pay@event.local", "Buyer 1", "BUYER")
	buyer2 := registerTestUser(t, h.router, "b2-pay@event.local", "Buyer 2", "BUYER")
	ev := createTestEvent(t, h.router, orgAuth.AccessToken)

	ord1 := createTestOrder(t, h.router, buyer1.AccessToken, ev.ID, ev.TicketTypes[0].ID, 2)
	badSigRec := doJSONReq(t, h.router, http.MethodPost, "/api/v1/webhooks/payment", map[string]any{
		"transaction_id":     "tx-bad",
		"order_id":           ord1.ID.String(),
		"status_code":        "200",
		"gross_amount":       ord1.TotalAmountIDR,
		"transaction_status": "settlement",
		"signature_key":      "invalid-signature",
	})
	assertAPIError(t, badSigRec, http.StatusUnauthorized, domain.ErrCodeUnauthorizedWebhook)

	wrongAmt := ord1.TotalAmountIDR - 1
	wrongAmtSig := domain.ComputeSHA512Signature(ord1.ID.String(), "200", wrongAmt, testWebhookSecret)
	wrongAmtRec := doJSONReq(t, h.router, http.MethodPost, "/api/v1/webhooks/payment", map[string]any{
		"transaction_id":     "tx-wrong-amt",
		"order_id":           ord1.ID.String(),
		"status_code":        "200",
		"gross_amount":       wrongAmt,
		"transaction_status": "settlement",
		"signature_key":      wrongAmtSig,
	})
	assertAPIError(t, wrongAmtRec, http.StatusBadRequest, domain.ErrCodePaymentAmountMismatch)

	validSig := domain.ComputeSHA512Signature(ord1.ID.String(), "200", ord1.TotalAmountIDR, testWebhookSecret)
	hookBody := map[string]any{
		"transaction_id":     "tx-good",
		"order_id":           ord1.ID.String(),
		"status_code":        "200",
		"gross_amount":       ord1.TotalAmountIDR,
		"transaction_status": "settlement",
		"signature_key":      validSig,
	}
	hookRec := doJSONReq(t, h.router, http.MethodPost, "/api/v1/webhooks/payment", hookBody)
	if hookRec.Code != http.StatusOK || !strings.Contains(hookRec.Body.String(), `"PROCESSED_PAID"`) {
		t.Fatalf("valid webhook = %d %s, want 200 PROCESSED_PAID", hookRec.Code, hookRec.Body.String())
	}

	dupRec := doJSONReq(t, h.router, http.MethodPost, "/api/v1/webhooks/payment", hookBody)
	if dupRec.Code != http.StatusOK || !strings.Contains(dupRec.Body.String(), `"IGNORED_DUPLICATE"`) {
		t.Fatalf("duplicate webhook = %d %s, want 200 IGNORED_DUPLICATE", dupRec.Code, dupRec.Body.String())
	}

	ord2 := createTestOrder(t, h.router, buyer2.AccessToken, ev.ID, ev.TicketTypes[0].ID, 1)
	payDemoRec := doJSONReq(t, h.router, http.MethodPost, "/api/v1/orders/"+ord2.ID.String()+"/pay", nil, [2]string{"Authorization", "Bearer " + buyer2.AccessToken})
	if payDemoRec.Code != http.StatusOK || !strings.Contains(payDemoRec.Body.String(), `"PROCESSED_PAID"`) {
		t.Fatalf("pay demo order = %d %s, want 200 PROCESSED_PAID", payDemoRec.Code, payDemoRec.Body.String())
	}

	badPayRec := doJSONReq(t, h.router, http.MethodPost, "/api/v1/orders/not-a-uuid/pay", nil, [2]string{"Authorization", "Bearer " + buyer2.AccessToken})
	assertAPIError(t, badPayRec, http.StatusBadRequest, domain.ErrCodeValidation)
}

func assertLastWebhookOutcome(t *testing.T, repo *memWebhookRepo, want domain.WebhookOutcome) {
	t.Helper()
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.delivered) == 0 {
		t.Fatalf("expected webhook audit record (%s), got none", want)
	}
	got := repo.delivered[len(repo.delivered)-1].Outcome
	if got != want {
		t.Fatalf("last webhook audit outcome = %s, want %s", got, want)
	}
}

func TestRouter_WebhookValidationBypassAndAudit(t *testing.T) {
	t.Parallel()

	h := newTestHarness(false)
	orgAuth := registerTestUser(t, h.router, "org-audit@event.local", "Org Audit", "ORGANIZER")
	buyer := registerTestUser(t, h.router, "buyer-audit@event.local", "Buyer Audit", "BUYER")
	ev := createTestEvent(t, h.router, orgAuth.AccessToken)
	ord := createTestOrder(t, h.router, buyer.AccessToken, ev.ID, ev.TicketTypes[0].ID, 1)

	omittedSigRec := doJSONReq(t, h.router, http.MethodPost, "/api/v1/webhooks/payment", map[string]any{
		"transaction_id":     "tx-omitted-sig",
		"order_id":           ord.ID.String(),
		"status_code":        "200",
		"gross_amount":       ord.TotalAmountIDR,
		"transaction_status": "settlement",
	})
	assertAPIError(t, omittedSigRec, http.StatusUnauthorized, domain.ErrCodeUnauthorizedWebhook)
	assertLastWebhookOutcome(t, h.webhooks, domain.OutcomeSignatureInvalid)

	emptySigRec := doJSONReq(t, h.router, http.MethodPost, "/api/v1/webhooks/payment", map[string]any{
		"transaction_id":     "tx-empty-sig",
		"order_id":           ord.ID.String(),
		"status_code":        "200",
		"gross_amount":       ord.TotalAmountIDR,
		"transaction_status": "settlement",
		"signature_key":      "",
	})
	assertAPIError(t, emptySigRec, http.StatusUnauthorized, domain.ErrCodeUnauthorizedWebhook)
	assertLastWebhookOutcome(t, h.webhooks, domain.OutcomeSignatureInvalid)

	emptyObjRec := doJSONReq(t, h.router, http.MethodPost, "/api/v1/webhooks/payment", map[string]any{})
	assertAPIError(t, emptyObjRec, http.StatusUnauthorized, domain.ErrCodeUnauthorizedWebhook)
	assertLastWebhookOutcome(t, h.webhooks, domain.OutcomeSignatureInvalid)

	emptyRawRec := doJSONReq(t, h.router, http.MethodPost, "/api/v1/webhooks/payment", nil)
	assertAPIError(t, emptyRawRec, http.StatusUnauthorized, domain.ErrCodeUnauthorizedWebhook)
	assertLastWebhookOutcome(t, h.webhooks, domain.OutcomeSignatureInvalid)

	negAmt := int64(-100)
	negSig := domain.ComputeSHA512Signature(ord.ID.String(), "200", negAmt, testWebhookSecret)
	negAmtRec := doJSONReq(t, h.router, http.MethodPost, "/api/v1/webhooks/payment", map[string]any{
		"transaction_id":     "tx-neg-amt",
		"order_id":           ord.ID.String(),
		"status_code":        "200",
		"gross_amount":       negAmt,
		"transaction_status": "settlement",
		"signature_key":      negSig,
	})
	assertAPIError(t, negAmtRec, http.StatusBadRequest, domain.ErrCodePaymentAmountMismatch)
	assertLastWebhookOutcome(t, h.webhooks, domain.OutcomeAmountMismatch)

	badOrderSig := domain.ComputeSHA512Signature("not-a-uuid", "200", ord.TotalAmountIDR, testWebhookSecret)
	badOrderRec := doJSONReq(t, h.router, http.MethodPost, "/api/v1/webhooks/payment", map[string]any{
		"transaction_id":     "tx-bad-oid",
		"order_id":           "not-a-uuid",
		"status_code":        "200",
		"gross_amount":       ord.TotalAmountIDR,
		"transaction_status": "settlement",
		"signature_key":      badOrderSig,
	})
	assertAPIError(t, badOrderRec, http.StatusNotFound, domain.ErrCodeOrderNotFound)
	assertLastWebhookOutcome(t, h.webhooks, domain.OutcomeOrderNotFound)
}

func TestRouter_DemoFlashSale(t *testing.T) {
	t.Parallel()

	h := newTestHarness(false)

	customRec := doJSONReq(t, h.router, http.MethodPost, "/api/v1/demo/flash-sale", map[string]any{
		"requests": 40,
		"quota":    15,
	})
	if customRec.Code != http.StatusOK {
		t.Fatalf("flash sale status = %d, want 200 (body=%s)", customRec.Code, customRec.Body.String())
	}
	var report service.FlashSaleReport
	if err := json.Unmarshal(customRec.Body.Bytes(), &report); err != nil {
		t.Fatalf("decode flash sale report: %v", err)
	}
	if !report.ZeroOversell || report.SucceededBookings != 15 || report.RejectedQuotaEmpty != 25 || report.RemainingQuotaAfter != 0 {
		t.Fatalf("unexpected flash sale report: %+v", report)
	}

	emptyJSONRec := doJSONReq(t, h.router, http.MethodPost, "/api/v1/demo/flash-sale", map[string]any{})
	if emptyJSONRec.Code != http.StatusOK {
		t.Fatalf("flash sale {} body status = %d, want 200 (body=%s)", emptyJSONRec.Code, emptyJSONRec.Body.String())
	}

	omittedBodyRec := doJSONReq(t, h.router, http.MethodPost, "/api/v1/demo/flash-sale", nil)
	if omittedBodyRec.Code != http.StatusOK {
		t.Fatalf("flash sale omitted body status = %d, want 200 (body=%s)", omittedBodyRec.Code, omittedBodyRec.Body.String())
	}
	var defaultReport service.FlashSaleReport
	if err := json.Unmarshal(omittedBodyRec.Body.Bytes(), &defaultReport); err != nil {
		t.Fatalf("decode default flash sale report: %v", err)
	}
	if defaultReport.ConcurrentRequests != 200 || !defaultReport.ZeroOversell {
		t.Fatalf("unexpected default flash sale report: %+v", defaultReport)
	}

	tooBigRec := doJSONReq(t, h.router, http.MethodPost, "/api/v1/demo/flash-sale", map[string]any{"requests": 501})
	assertAPIError(t, tooBigRec, http.StatusUnprocessableEntity, domain.ErrCodeValidation)
}

func TestRouter_DemoHeaderAuthAndErrorSafety(t *testing.T) {
	t.Parallel()

	h := newTestHarness(true)
	demoOrgID := uuid.New().String()

	demoEvRec := doJSONReq(t, h.router, http.MethodPost, "/api/v1/events", map[string]any{
		"title":      "Demo Header Event",
		"venue":      "Main Hall",
		"event_date": time.Now().UTC().Add(48 * time.Hour).Format(time.RFC3339),
		"ticket_types": []map[string]any{
			{"name": "Reguler", "price_idr": 50000, "total_quota": 20},
			{"name": "VIP", "price_idr": 150000, "total_quota": 10},
		},
	}, [2]string{"X-User-ID", demoOrgID}, [2]string{"X-User-Role", "ORGANIZER"})
	if demoEvRec.Code != http.StatusCreated {
		t.Fatalf("demo header event create status = %d, want 201 (body=%s)", demoEvRec.Code, demoEvRec.Body.String())
	}

	h.events.mu.Lock()
	h.events.listErr = errors.New("sensitive postgres dsn internal failure")
	h.events.mu.Unlock()
	err500Rec := doJSONReq(t, h.router, http.MethodGet, "/api/v1/events", nil)
	assertAPIError(t, err500Rec, http.StatusInternalServerError, "INTERNAL_ERROR")
	if strings.Contains(err500Rec.Body.String(), "sensitive postgres dsn") {
		t.Fatalf("500 response leaked internal error details: %s", err500Rec.Body.String())
	}

	h.orders.mu.Lock()
	h.orders.panicOn = true
	h.orders.mu.Unlock()
	panicRec := doJSONReq(t, h.router, http.MethodGet, "/api/v1/orders/"+uuid.New().String(), nil, [2]string{"X-User-ID", demoOrgID}, [2]string{"X-User-Role", "ORGANIZER"})
	assertAPIError(t, panicRec, http.StatusInternalServerError, "INTERNAL_ERROR")

	h.users.mu.Lock()
	h.users.ensureDemoErr = errors.New("db unavailable during demo ensure")
	h.users.mu.Unlock()
	demoAuthErrRec := doJSONReq(t, h.router, http.MethodGet, "/api/v1/orders", nil, [2]string{"X-User-ID", uuid.New().String()}, [2]string{"X-User-Role", "BUYER"})
	assertAPIError(t, demoAuthErrRec, http.StatusInternalServerError, "INTERNAL_ERROR")
	h.users.mu.Lock()
	h.users.ensureDemoErr = nil
	h.users.mu.Unlock()

	oversized := strings.Repeat("a", (1<<20)+64)
	hugeReq := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"email":"a@b.com","password":"`+oversized+`"}`))
	hugeReq.Header.Set("Content-Type", "application/json")
	hugeRec := httptest.NewRecorder()
	h.router.ServeHTTP(hugeRec, hugeReq)
	if hugeRec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized payload status = %d, want 413 (body=%s)", hugeRec.Code, hugeRec.Body.String())
	}

	nilAuthRouter := routes.NewRouter(routes.RouterDeps{
		Orders:              handler.NewOrderHandler(service.NewOrderService(newMemOrderRepo(newMemEventRepo()), newMemEventRepo()), slog.New(slog.DiscardHandler)),
		AllowDemoHeaderAuth: true,
	})
	nilAuthRec := doJSONReq(t, nilAuthRouter, http.MethodGet, "/api/v1/orders", nil,
		[2]string{"X-User-ID", uuid.New().String()},
		[2]string{"X-User-Role", "BUYER"},
	)
	assertAPIError(t, nilAuthRec, http.StatusUnauthorized, domain.ErrCodeUnauthorized)
	if !strings.Contains(nilAuthRec.Body.String(), "unauthorized: auth verifier unavailable") {
		t.Fatalf("nil auth demo header detail = %s, want auth verifier unavailable", nilAuthRec.Body.String())
	}
}

func TestRouter_PanicLogsAccessAndRecoveryWithRequestID(t *testing.T) {
	t.Parallel()

	var logBuf bytes.Buffer
	users := newMemUserRepo()
	events := newMemEventRepo()
	orders := newMemOrderRepo(events)
	orders.panicOn = true

	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
	authSvc := service.NewAuthService(users, testJWTSecret)
	eventSvc := service.NewEventService(events, orders)
	orderSvc := service.NewOrderService(orders, events)
	paymentSvc := service.NewPaymentService(orders, &memWebhookRepo{}, events, testWebhookSecret)
	demoSvc := service.NewDemoService(users, events, orderSvc)

	r := routes.NewRouter(routes.RouterDeps{
		Logger:              logger,
		Authenticator:       authSvc,
		System:              handler.NewSystemHandler(&fakePinger{}, logger),
		Auth:                handler.NewAuthHandler(authSvc, logger),
		Events:              handler.NewEventHandler(eventSvc, logger),
		Orders:              handler.NewOrderHandler(orderSvc, logger),
		Webhooks:            handler.NewWebhookHandler(paymentSvc, logger),
		Demo:                handler.NewDemoHandler(demoSvc, logger),
		AllowDemoHeaderAuth: true,
	})

	rec := doJSONReq(t, r, http.MethodGet, "/api/v1/orders/"+uuid.New().String(), nil,
		[2]string{"X-User-ID", uuid.New().String()},
		[2]string{"X-User-Role", "ORGANIZER"},
	)
	assertAPIError(t, rec, http.StatusInternalServerError, "INTERNAL_ERROR")

	logs := logBuf.String()
	if !strings.Contains(logs, `"http handler panic recovered"`) {
		t.Fatalf("expected panic recovery log in %s", logs)
	}
	if !strings.Contains(logs, `"http request"`) || !strings.Contains(logs, `"status":500`) {
		t.Fatalf("expected access log with status 500 on recovered panic in %s", logs)
	}
	if !strings.Contains(logs, `"request_id":"`) || strings.Contains(logs, `"request_id":""`) {
		t.Fatalf("expected non-empty request_id on panic and access logs in %s", logs)
	}
}

func TestRouter_ConcurrentRequestsRace(t *testing.T) {
	t.Parallel()

	const workers = 16
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := range workers {
		go func(idx int) {
			defer wg.Done()
			h := newTestHarness(false)
			path := "/health"
			if idx%2 == 0 {
				path = "/openapi.json"
			}
			rec := doJSONReq(t, h.router, http.MethodGet, path, nil)
			if rec.Code != http.StatusOK {
				t.Errorf("worker %d %s status = %d, want 200", idx, path, rec.Code)
			}
		}(i)
	}
	wg.Wait()
}

func TestRouter_CORS(t *testing.T) {
	t.Parallel()

	t.Run("uses default allowed origins when CORSAllowedOrigins is empty", func(t *testing.T) {
		t.Parallel()

		h := newTestHarness(false)

		optReq := httptest.NewRequestWithContext(t.Context(), http.MethodOptions, "/api/v1/orders", nil)
		optReq.Header.Set("Origin", "http://localhost:5173")
		optReq.Header.Set("Access-Control-Request-Method", http.MethodPost)
		optRec := httptest.NewRecorder()
		h.router.ServeHTTP(optRec, optReq)

		if optRec.Code != http.StatusNoContent {
			t.Fatalf("OPTIONS status = %d, want 204", optRec.Code)
		}
		if got := optRec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
			t.Fatalf("Access-Control-Allow-Origin = %q, want http://localhost:5173", got)
		}
		if got := optRec.Header().Get("Access-Control-Allow-Methods"); got != "GET, POST, PUT, DELETE, OPTIONS" {
			t.Fatalf("Access-Control-Allow-Methods = %q, want GET, POST, PUT, DELETE, OPTIONS", got)
		}
		if got := optRec.Header().Get("Access-Control-Allow-Headers"); got != "Content-Type, Authorization, X-User-ID, X-User-Role" {
			t.Fatalf("Access-Control-Allow-Headers = %q, want Content-Type, Authorization, X-User-ID, X-User-Role", got)
		}

		disallowedReq := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health", nil)
		disallowedReq.Header.Set("Origin", "https://not-allowed.example.com")
		disallowedRec := httptest.NewRecorder()
		h.router.ServeHTTP(disallowedRec, disallowedReq)

		if disallowedRec.Code != http.StatusOK {
			t.Fatalf("GET /health status = %d, want 200", disallowedRec.Code)
		}
		if got := disallowedRec.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Fatalf("expected empty ACAO for disallowed origin, got %q", got)
		}
	})

	t.Run("honors custom CORSAllowedOrigins in RouterDeps", func(t *testing.T) {
		t.Parallel()

		r := routes.NewRouter(routes.RouterDeps{
			System:             handler.NewSystemHandler(&fakePinger{}, nil),
			CORSAllowedOrigins: []string{"https://tickets.example.com"},
		})

		allowedReq := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health", nil)
		allowedReq.Header.Set("Origin", "https://tickets.example.com")
		allowedRec := httptest.NewRecorder()
		r.ServeHTTP(allowedRec, allowedReq)

		if got := allowedRec.Header().Get("Access-Control-Allow-Origin"); got != "https://tickets.example.com" {
			t.Fatalf("Access-Control-Allow-Origin = %q, want https://tickets.example.com", got)
		}

		defaultLocalReq := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health", nil)
		defaultLocalReq.Header.Set("Origin", "http://localhost:5173")
		defaultLocalRec := httptest.NewRecorder()
		r.ServeHTTP(defaultLocalRec, defaultLocalReq)

		if got := defaultLocalRec.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Fatalf("expected empty ACAO when custom origins override defaults, got %q", got)
		}
	})
}
