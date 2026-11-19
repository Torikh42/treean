//go:build integration

package tests_test

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
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/torikh42/treean/internal/config"
	"github.com/torikh42/treean/internal/domain"
	"github.com/torikh42/treean/internal/handler"
	"github.com/torikh42/treean/internal/repository"
	"github.com/torikh42/treean/internal/service"
	"github.com/torikh42/treean/internal/worker"
)

const (
	testWebhookSecret  = "integration-webhook-secret"
	defaultTestDBURL   = "postgres://ticketing:ticketing_secret@localhost:5432/event_ticketing?sslmode=disable"
	webhookStatusOK    = "200"
	roleOrganizer      = "ORGANIZER"
	roleBuyer          = "BUYER"
	txStatusSettlement = "settlement"
	txStatusDeny       = "deny"
)

type testEnv struct {
	pool    *pgxpool.Pool
	server  *httptest.Server
	client  *http.Client
	sweeper *worker.ExpirySweeper
}

type concurrentRequestSpec struct {
	targetURL string
	method    string
	body      any
	headers   map[string]string
}

type concurrentResult struct {
	status int
	raw    []byte
	err    error
}

type webhookAuditRow struct {
	outcome        string
	requiresRefund bool
}

func setupTestEnv(t *testing.T) *testEnv {
	t.Helper()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = defaultTestDBURL
	}

	cfg := config.Config{
		DatabaseURL:         dbURL,
		JWTSecret:           "integration-jwt-secret",
		WebhookSecret:       testWebhookSecret,
		AllowDemoHeaderAuth: true,
		ExpirySweepInterval: 5 * time.Second,
		HTTPReadTimeout:     15 * time.Second,
		HTTPWriteTimeout:    30 * time.Second,
		DBMaxConns:          50,
		DBMinConns:          5,
	}

	ctx := t.Context()
	pool, err := repository.NewPool(ctx, cfg)
	if err != nil {
		t.Fatalf("connecting test database: %v", err)
	}
	t.Cleanup(pool.Close)

	if err := repository.ApplyMigrations(ctx, pool); err != nil {
		t.Fatalf("applying migrations: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	userRepo := repository.NewUserRepo(pool)
	eventRepo := repository.NewEventRepo(pool)
	orderRepo := repository.NewOrderRepo(pool)
	webhookRepo := repository.NewWebhookRepo(pool)

	authSvc := service.NewAuthService(userRepo, cfg.JWTSecret)
	eventSvc := service.NewEventService(eventRepo, orderRepo)
	orderSvc := service.NewOrderService(orderRepo, eventRepo)
	paymentSvc := service.NewPaymentService(orderRepo, webhookRepo, eventRepo, cfg.WebhookSecret)
	demoSvc := service.NewDemoService(userRepo, eventRepo, orderSvc)

	r := handler.NewRouter(handler.RouterDeps{
		Logger:              logger,
		DB:                  pool,
		AuthService:         authSvc,
		EventService:        eventSvc,
		OrderService:        orderSvc,
		PaymentService:      paymentSvc,
		DemoService:         demoSvc,
		AllowDemoHeaderAuth: true,
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	transport := &http.Transport{
		MaxIdleConns:        256,
		MaxIdleConnsPerHost: 256,
	}
	t.Cleanup(transport.CloseIdleConnections)

	return &testEnv{
		pool:    pool,
		server:  srv,
		client:  &http.Client{Timeout: 30 * time.Second, Transport: transport},
		sweeper: worker.NewExpirySweeper(orderRepo, logger, time.Second, 50),
	}
}

func executeJSON(
	ctx context.Context,
	client *http.Client,
	targetURL string,
	method string,
	body any,
	headers map[string]string,
) (int, []byte, error) {
	if client == nil {
		return 0, nil, errors.New("http client is required")
	}

	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("marshaling request body: %w", err)
		}
		reader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, targetURL, reader)
	if err != nil {
		return 0, nil, fmt.Errorf("building %s %s request: %w", method, targetURL, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("executing %s %s: %w", method, targetURL, err)
	}

	raw, readErr := io.ReadAll(resp.Body)
	closeErr := resp.Body.Close()
	if readErr != nil {
		return resp.StatusCode, nil, fmt.Errorf("reading %s %s response body: %w", method, targetURL, readErr)
	}
	if closeErr != nil {
		return resp.StatusCode, nil, fmt.Errorf("closing %s %s response body: %w", method, targetURL, closeErr)
	}
	return resp.StatusCode, raw, nil
}

func mustJSON(
	t *testing.T,
	env *testEnv,
	path string,
	method string,
	body any,
	headers map[string]string,
) (int, []byte) {
	t.Helper()

	status, raw, err := executeJSON(t.Context(), env.client, env.server.URL+path, method, body, headers)
	if err != nil {
		t.Fatalf("%s %s failed: %v", method, path, err)
	}
	return status, raw
}

func mustUnmarshal[T any](t *testing.T, raw []byte) T {
	t.Helper()

	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshaling JSON %q: %v", string(raw), err)
	}
	return out
}

func runConcurrentRequests(
	ctx context.Context,
	client *http.Client,
	specs []concurrentRequestSpec,
) []concurrentResult {
	gate := make(chan struct{})
	resCh := make(chan concurrentResult, len(specs))
	var wg sync.WaitGroup
	wg.Add(len(specs))

	for _, spec := range specs {
		go func(s concurrentRequestSpec) {
			defer wg.Done()
			<-gate
			status, raw, err := executeJSON(ctx, client, s.targetURL, s.method, s.body, s.headers)
			resCh <- concurrentResult{status: status, raw: raw, err: err}
		}(spec)
	}

	close(gate)
	wg.Wait()
	close(resCh)

	results := make([]concurrentResult, 0, len(specs))
	for res := range resCh {
		results = append(results, res)
	}
	return results
}

func assertAPIError(t *testing.T, status int, raw []byte, wantStatus int, wantCode string) {
	t.Helper()

	if status != wantStatus {
		t.Fatalf("status = %d, want %d (body=%s)", status, wantStatus, string(raw))
	}
	payload := mustUnmarshal[map[string]any](t, raw)
	if payload["error_code"] != wantCode {
		t.Fatalf("error_code = %v, want %q (body=%s)", payload["error_code"], wantCode, string(raw))
	}
	detail, ok := payload["detail"].(string)
	if !ok || detail == "" {
		t.Fatalf("expected non-empty error detail in %s", string(raw))
	}
}

func roleHeaders(userID, role string) map[string]string {
	return map[string]string{
		"X-User-ID":   userID,
		"X-User-Role": role,
	}
}

func createTestEvent(t *testing.T, env *testEnv, regQuota, vipQuota int32) domain.Event {
	t.Helper()

	orgHeaders := roleHeaders(uuid.NewString(), roleOrganizer)
	status, raw := mustJSON(t, env, "/api/v1/events", http.MethodPost, map[string]any{
		"title":       "Integration Event " + uuid.NewString()[:8],
		"description": "Integration test event",
		"venue":       "Hall A",
		"event_date":  time.Now().UTC().Add(48 * time.Hour),
		"ticket_types": []map[string]any{
			{"name": "Reguler", "price_idr": 150000, "total_quota": regQuota},
			{"name": "VIP", "price_idr": 350000, "total_quota": vipQuota},
		},
	}, orgHeaders)
	if status != http.StatusCreated {
		t.Fatalf("create event status = %d, want 201 (body=%s)", status, string(raw))
	}
	ev := mustUnmarshal[domain.Event](t, raw)
	if len(ev.TicketTypes) != 2 {
		t.Fatalf("expected 2 ticket types, got %d", len(ev.TicketTypes))
	}
	return ev
}

func createOrder(
	t *testing.T,
	env *testEnv,
	headers map[string]string,
	eventID uuid.UUID,
	items []map[string]any,
) domain.Order {
	t.Helper()

	status, raw := mustJSON(t, env, "/api/v1/orders", http.MethodPost, map[string]any{
		"event_id": eventID.String(),
		"items":    items,
	}, headers)
	if status != http.StatusCreated {
		t.Fatalf("create order status = %d, want 201 (body=%s)", status, string(raw))
	}
	return mustUnmarshal[domain.Order](t, raw)
}

func fetchEvent(t *testing.T, env *testEnv, eventID uuid.UUID) domain.Event {
	t.Helper()

	status, raw := mustJSON(t, env, "/api/v1/events/"+eventID.String(), http.MethodGet, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("get event status = %d, want 200 (body=%s)", status, string(raw))
	}
	return mustUnmarshal[domain.Event](t, raw)
}

func fetchOrder(t *testing.T, env *testEnv, orderID uuid.UUID, headers map[string]string) domain.Order {
	t.Helper()

	status, raw := mustJSON(t, env, "/api/v1/orders/"+orderID.String(), http.MethodGet, nil, headers)
	if status != http.StatusOK {
		t.Fatalf("get order status = %d, want 200 (body=%s)", status, string(raw))
	}
	return mustUnmarshal[domain.Order](t, raw)
}

func findTierQuota(t *testing.T, ev domain.Event, tierID uuid.UUID) int32 {
	t.Helper()

	for _, tt := range ev.TicketTypes {
		if tt.ID == tierID {
			return tt.RemainingQuota
		}
	}
	t.Fatalf("ticket tier %s not found in event %s", tierID, ev.ID)
	return 0
}

func signedWebhookPayload(txID string, orderID uuid.UUID, grossAmount int64, txStatus string) map[string]any {
	sig := domain.ComputeSHA512Signature(orderID.String(), webhookStatusOK, grossAmount, testWebhookSecret)
	return map[string]any{
		"transaction_id":     txID,
		"order_id":           orderID.String(),
		"status_code":        webhookStatusOK,
		"gross_amount":       grossAmount,
		"transaction_status": txStatus,
		"signature_key":      sig,
	}
}

func querySingleValue[T any](t *testing.T, env *testEnv, query string, args ...any) T {
	t.Helper()

	var val T
	if err := env.pool.QueryRow(t.Context(), query, args...).Scan(&val); err != nil {
		t.Fatalf("querying %q: %v", query, err)
	}
	return val
}

func queryWebhookAuditByOrder(t *testing.T, env *testEnv, orderID uuid.UUID) webhookAuditRow {
	t.Helper()

	var row webhookAuditRow
	err := env.pool.QueryRow(
		t.Context(),
		"SELECT outcome, requires_refund FROM payment_webhooks WHERE parsed_order_id = $1 ORDER BY received_at DESC LIMIT 1",
		orderID,
	).Scan(&row.outcome, &row.requiresRefund)
	if err != nil {
		t.Fatalf("querying webhook audit by order %s: %v", orderID, err)
	}
	return row
}

func queryWebhookAuditByTxID(t *testing.T, env *testEnv, txID string) webhookAuditRow {
	t.Helper()

	var row webhookAuditRow
	err := env.pool.QueryRow(
		t.Context(),
		"SELECT outcome, requires_refund FROM payment_webhooks WHERE transaction_id = $1 ORDER BY received_at DESC LIMIT 1",
		txID,
	).Scan(&row.outcome, &row.requiresRefund)
	if err != nil {
		t.Fatalf("querying webhook audit by transaction_id %q: %v", txID, err)
	}
	return row
}

func assertWebhookAuditByTxID(
	t *testing.T,
	env *testEnv,
	txID string,
	wantOutcome domain.WebhookOutcome,
	wantRefund bool,
) {
	t.Helper()

	audit := queryWebhookAuditByTxID(t, env, txID)
	if audit.outcome != string(wantOutcome) || audit.requiresRefund != wantRefund {
		t.Fatalf(
			"webhook audit for %q = (%q, requires_refund=%v), want (%q, requires_refund=%v)",
			txID, audit.outcome, audit.requiresRefund, wantOutcome, wantRefund,
		)
	}
}

func verifyListedEventQuota(t *testing.T, events []domain.Event, eventID, regID, vipID uuid.UUID, wantReg, wantVIP int32) {
	t.Helper()

	for _, ev := range events {
		if ev.ID == eventID {
			if gotReg := findTierQuota(t, ev, regID); gotReg != wantReg {
				t.Fatalf("listed reguler quota = %d, want %d", gotReg, wantReg)
			}
			if gotVIP := findTierQuota(t, ev, vipID); gotVIP != wantVIP {
				t.Fatalf("listed VIP quota = %d, want %d", gotVIP, wantVIP)
			}
			return
		}
	}
	t.Fatalf("created event %s missing from GET /api/v1/events", eventID)
}

func verifyUniqueTicketCodes(t *testing.T, tickets []domain.Ticket, wantCount int) {
	t.Helper()

	if len(tickets) != wantCount {
		t.Fatalf("expected %d tickets, got %d", wantCount, len(tickets))
	}
	seen := make(map[string]struct{}, wantCount)
	for _, tk := range tickets {
		if tk.TicketCode == "" {
			t.Fatal("ticket_code must not be empty")
		}
		if _, dup := seen[tk.TicketCode]; dup {
			t.Fatalf("duplicate ticket_code issued: %q", tk.TicketCode)
		}
		seen[tk.TicketCode] = struct{}{}
	}
}

func assertOrderSummary(
	t *testing.T,
	ord domain.Order,
	wantTickets int32,
	wantAmount int64,
	wantStatus domain.OrderStatus,
) {
	t.Helper()

	if ord.TotalTickets != wantTickets {
		t.Fatalf("order total_tickets = %d, want %d", ord.TotalTickets, wantTickets)
	}
	if ord.TotalAmountIDR != wantAmount {
		t.Fatalf("order total_amount_idr = %d, want %d", ord.TotalAmountIDR, wantAmount)
	}
	if ord.Status != wantStatus {
		t.Fatalf("order status = %s, want %s", ord.Status, wantStatus)
	}
	if hold := ord.ExpiresAt.Sub(ord.CreatedAt); hold != domain.OrderHoldDuration {
		t.Fatalf("hold duration = %v, want %v", hold, domain.OrderHoldDuration)
	}
}

func assertEventTierQuotas(
	t *testing.T,
	env *testEnv,
	eventID, regID, vipID uuid.UUID,
	wantReg, wantVIP int32,
) {
	t.Helper()

	ev := fetchEvent(t, env, eventID)
	if got := findTierQuota(t, ev, regID); got != wantReg {
		t.Fatalf("reguler remaining_quota = %d, want %d", got, wantReg)
	}
	if got := findTierQuota(t, ev, vipID); got != wantVIP {
		t.Fatalf("vip remaining_quota = %d, want %d", got, wantVIP)
	}
}

func assertBuyerOrderHistory(t *testing.T, env *testEnv, buyer map[string]string, orderID uuid.UUID, wantTickets int) {
	t.Helper()

	histStatus, histRaw := mustJSON(t, env, "/api/v1/orders", http.MethodGet, nil, buyer)
	if histStatus != http.StatusOK {
		t.Fatalf("list orders status = %d, want 200 (body=%s)", histStatus, string(histRaw))
	}
	history := mustUnmarshal[[]domain.Order](t, histRaw)
	if len(history) != 1 || history[0].ID != orderID || history[0].Status != domain.OrderStatusPaid {
		t.Fatalf("unexpected order history: %+v", history)
	}
	verifyUniqueTicketCodes(t, history[0].Tickets, wantTickets)
}

func tallyConcurrentOrderResults(t *testing.T, results []concurrentResult) (uuid.UUID, int, int) {
	t.Helper()

	var createdID uuid.UUID
	var okCount, rejectCount int
	for _, res := range results {
		if res.err != nil {
			t.Fatalf("concurrent buyer request error: %v", res.err)
		}
		switch res.status {
		case http.StatusCreated:
			okCount++
			ord := mustUnmarshal[domain.Order](t, res.raw)
			createdID = ord.ID
		case http.StatusConflict:
			rejectCount++
			assertAPIError(t, res.status, res.raw, http.StatusConflict, domain.ErrCodeBuyerLimitExceeded)
		default:
			t.Fatalf("unexpected status %d (body=%s)", res.status, string(res.raw))
		}
	}
	return createdID, okCount, rejectCount
}

func tallyWebhookReplayResults(t *testing.T, results []concurrentResult) (int, int) {
	t.Helper()

	var paidCount, dupCount int
	for _, res := range results {
		if res.err != nil {
			t.Fatalf("parallel webhook error: %v", res.err)
		}
		if res.status != http.StatusOK {
			t.Fatalf("webhook replay status = %d, want 200 (body=%s)", res.status, string(res.raw))
		}
		parsed := mustUnmarshal[service.WebhookResult](t, res.raw)
		if parsed.Outcome == domain.OutcomeProcessedPaid {
			if parsed.IdempotentReplay {
				t.Fatalf("initial PROCESSED_PAID response unexpectedly flagged idempotent_replay=true")
			}
			paidCount++
			continue
		}
		if parsed.Outcome != domain.OutcomeIgnoredDuplicate || !parsed.IdempotentReplay {
			t.Fatalf("unexpected webhook result %+v", parsed)
		}
		dupCount++
	}
	return paidCount, dupCount
}

func TestIntegration_HappyFlow_OrderPayTicketsHistory(t *testing.T) {
	env := setupTestEnv(t)
	ev := createTestEvent(t, env, 10, 5)
	regID := ev.TicketTypes[0].ID
	vipID := ev.TicketTypes[1].ID

	listStatus, listRaw := mustJSON(t, env, "/api/v1/events", http.MethodGet, nil, nil)
	if listStatus != http.StatusOK {
		t.Fatalf("list events status = %d, want 200 (body=%s)", listStatus, string(listRaw))
	}
	listedEvents := mustUnmarshal[[]domain.Event](t, listRaw)
	verifyListedEventQuota(t, listedEvents, ev.ID, regID, vipID, 10, 5)

	buyer := roleHeaders(uuid.NewString(), roleBuyer)
	ord := createOrder(t, env, buyer, ev.ID, []map[string]any{
		{"ticket_type_id": regID.String(), "quantity": 2},
		{"ticket_type_id": vipID.String(), "quantity": 1},
	})
	assertOrderSummary(t, ord, 3, 650000, domain.OrderStatusWaitingPayment)
	assertEventTierQuotas(t, env, ev.ID, regID, vipID, 8, 4)

	orgHeaders := roleHeaders(ev.OrganizerID.String(), roleOrganizer)
	if orgOrd := fetchOrder(t, env, ord.ID, orgHeaders); orgOrd.ID != ord.ID {
		t.Fatalf("organizer fetched order id = %s, want %s", orgOrd.ID, ord.ID)
	}

	otherBuyer := roleHeaders(uuid.NewString(), roleBuyer)
	forbidStatus, forbidRaw := mustJSON(t, env, "/api/v1/orders/"+ord.ID.String(), http.MethodGet, nil, otherBuyer)
	assertAPIError(t, forbidStatus, forbidRaw, http.StatusForbidden, domain.ErrCodeForbidden)

	payStatus, payRaw := mustJSON(t, env, "/api/v1/orders/"+ord.ID.String()+"/pay", http.MethodPost, nil, buyer)
	if payStatus != http.StatusOK {
		t.Fatalf("pay order status = %d, want 200 (body=%s)", payStatus, string(payRaw))
	}
	payRes := mustUnmarshal[service.WebhookResult](t, payRaw)
	if payRes.Outcome != domain.OutcomeProcessedPaid || payRes.IdempotentReplay {
		t.Fatalf("unexpected pay result: %+v", payRes)
	}

	loaded := fetchOrder(t, env, ord.ID, buyer)
	if loaded.Status != domain.OrderStatusPaid {
		t.Fatalf("loaded order status = %s, want PAID", loaded.Status)
	}
	verifyUniqueTicketCodes(t, loaded.Tickets, 3)
	assertBuyerOrderHistory(t, env, buyer, ord.ID, 3)
}

func TestIntegration_BuyerFourTicketLimit_AndCancellation(t *testing.T) {
	env := setupTestEnv(t)
	ev := createTestEvent(t, env, 50, 10)
	regID := ev.TicketTypes[0].ID
	buyer := roleHeaders(uuid.NewString(), roleBuyer)

	reqBody := map[string]any{
		"event_id": ev.ID.String(),
		"items":    []map[string]any{{"ticket_type_id": regID.String(), "quantity": 3}},
	}
	specs := make([]concurrentRequestSpec, 2)
	for i := range specs {
		specs[i] = concurrentRequestSpec{
			targetURL: env.server.URL + "/api/v1/orders",
			method:    http.MethodPost,
			body:      reqBody,
			headers:   buyer,
		}
	}

	results := runConcurrentRequests(t.Context(), env.client, specs)
	createdID, okCount, rejectCount := tallyConcurrentOrderResults(t, results)
	if okCount != 1 || rejectCount != 1 || createdID == uuid.Nil {
		t.Fatalf("expected 1 created and 1 conflict, got ok=%d reject=%d", okCount, rejectCount)
	}

	cancelStatus, cancelRaw := mustJSON(t, env, "/api/v1/orders/"+createdID.String()+"/cancel", http.MethodPost, nil, buyer)
	if cancelStatus != http.StatusOK {
		t.Fatalf("cancel order status = %d, want 200 (body=%s)", cancelStatus, string(cancelRaw))
	}
	cancelled := mustUnmarshal[domain.Order](t, cancelRaw)
	if cancelled.Status != domain.OrderStatusCancelled {
		t.Fatalf("cancelled order status = %s, want CANCELLED", cancelled.Status)
	}

	restoredEv := fetchEvent(t, env, ev.ID)
	if rem := findTierQuota(t, restoredEv, regID); rem != 50 {
		t.Fatalf("remaining_quota after cancel = %d, want 50", rem)
	}

	rebooked := createOrder(t, env, buyer, ev.ID, []map[string]any{
		{"ticket_type_id": regID.String(), "quantity": 4},
	})
	if rebooked.TotalTickets != 4 {
		t.Fatalf("rebooked total_tickets = %d, want 4", rebooked.TotalTickets)
	}
	afterRebookEv := fetchEvent(t, env, ev.ID)
	if rem := findTierQuota(t, afterRebookEv, regID); rem != 46 {
		t.Fatalf("remaining_quota after rebook = %d, want 46", rem)
	}
}

func TestIntegration_ExpirySweeper_AndLateWebhookRejection(t *testing.T) {
	ctx := t.Context()
	env := setupTestEnv(t)
	ev := createTestEvent(t, env, 5, 5)
	regID := ev.TicketTypes[0].ID
	buyer := roleHeaders(uuid.NewString(), roleBuyer)

	ord := createOrder(t, env, buyer, ev.ID, []map[string]any{
		{"ticket_type_id": regID.String(), "quantity": 2},
	})
	if _, err := env.pool.Exec(ctx, "UPDATE orders SET expires_at = NOW() - INTERVAL '1 minute' WHERE id = $1", ord.ID); err != nil {
		t.Fatalf("backdating expires_at: %v", err)
	}

	env.sweeper.RunOnce(ctx)

	restoredEv := fetchEvent(t, env, ev.ID)
	if rem := findTierQuota(t, restoredEv, regID); rem != 5 {
		t.Fatalf("remaining_quota after expiry = %d, want 5", rem)
	}
	expiredOrd := fetchOrder(t, env, ord.ID, buyer)
	if expiredOrd.Status != domain.OrderStatusExpired {
		t.Fatalf("order status after sweep = %s, want EXPIRED", expiredOrd.Status)
	}

	latePayload := signedWebhookPayload("tx-late-"+uuid.NewString(), ord.ID, ord.TotalAmountIDR, txStatusSettlement)
	wStatus, wRaw := mustJSON(t, env, "/api/v1/webhooks/payment", http.MethodPost, latePayload, nil)
	assertAPIError(t, wStatus, wRaw, http.StatusConflict, domain.ErrCodeOrderExpired)

	ticketCount := querySingleValue[int64](t, env, "SELECT COUNT(*) FROM tickets WHERE order_id = $1", ord.ID)
	if ticketCount != 0 {
		t.Fatalf("expected 0 tickets for expired order, got %d", ticketCount)
	}

	audit := queryWebhookAuditByOrder(t, env, ord.ID)
	if audit.outcome != string(domain.OutcomeRejectedLateExpired) || !audit.requiresRefund {
		t.Fatalf("expected REJECTED_LATE_EXPIRED with requires_refund=true, got %+v", audit)
	}
}

func TestIntegration_WebhookValidationAudit_AndFailureTransition(t *testing.T) {
	env := setupTestEnv(t)
	ev := createTestEvent(t, env, 10, 5)
	regID := ev.TicketTypes[0].ID
	buyer := roleHeaders(uuid.NewString(), roleBuyer)

	ord := createOrder(t, env, buyer, ev.ID, []map[string]any{
		{"ticket_type_id": regID.String(), "quantity": 2},
	})

	omittedSigTx := "tx-omitted-sig-" + uuid.NewString()
	omittedStatus, omittedRaw := mustJSON(t, env, "/api/v1/webhooks/payment", http.MethodPost, map[string]any{
		"transaction_id":     omittedSigTx,
		"order_id":           ord.ID.String(),
		"status_code":        webhookStatusOK,
		"gross_amount":       ord.TotalAmountIDR,
		"transaction_status": txStatusSettlement,
	}, nil)
	assertAPIError(t, omittedStatus, omittedRaw, http.StatusUnauthorized, domain.ErrCodeUnauthorizedWebhook)
	assertWebhookAuditByTxID(t, env, omittedSigTx, domain.OutcomeSignatureInvalid, false)

	emptySigTx := "tx-empty-sig-" + uuid.NewString()
	emptyStatus, emptyRaw := mustJSON(t, env, "/api/v1/webhooks/payment", http.MethodPost, map[string]any{
		"transaction_id":     emptySigTx,
		"order_id":           ord.ID.String(),
		"status_code":        webhookStatusOK,
		"gross_amount":       ord.TotalAmountIDR,
		"transaction_status": txStatusSettlement,
		"signature_key":      "",
	}, nil)
	assertAPIError(t, emptyStatus, emptyRaw, http.StatusUnauthorized, domain.ErrCodeUnauthorizedWebhook)
	assertWebhookAuditByTxID(t, env, emptySigTx, domain.OutcomeSignatureInvalid, false)

	negAmtTx := "tx-neg-amt-" + uuid.NewString()
	negStatus, negRaw := mustJSON(
		t,
		env,
		"/api/v1/webhooks/payment",
		http.MethodPost,
		signedWebhookPayload(negAmtTx, ord.ID, -100, txStatusSettlement),
		nil,
	)
	assertAPIError(t, negStatus, negRaw, http.StatusBadRequest, domain.ErrCodePaymentAmountMismatch)
	assertWebhookAuditByTxID(t, env, negAmtTx, domain.OutcomeAmountMismatch, false)

	wrongAmtTx := "tx-wrong-amt-" + uuid.NewString()
	wrongStatus, wrongRaw := mustJSON(
		t,
		env,
		"/api/v1/webhooks/payment",
		http.MethodPost,
		signedWebhookPayload(wrongAmtTx, ord.ID, ord.TotalAmountIDR-1, txStatusSettlement),
		nil,
	)
	assertAPIError(t, wrongStatus, wrongRaw, http.StatusBadRequest, domain.ErrCodePaymentAmountMismatch)
	assertWebhookAuditByTxID(t, env, wrongAmtTx, domain.OutcomeAmountMismatch, false)

	denyTx := "tx-deny-" + uuid.NewString()
	denyStatus, denyRaw := mustJSON(
		t,
		env,
		"/api/v1/webhooks/payment",
		http.MethodPost,
		signedWebhookPayload(denyTx, ord.ID, ord.TotalAmountIDR, txStatusDeny),
		nil,
	)
	if denyStatus != http.StatusOK {
		t.Fatalf("deny webhook status = %d, want 200 (body=%s)", denyStatus, string(denyRaw))
	}
	denyRes := mustUnmarshal[service.WebhookResult](t, denyRaw)
	if denyRes.Outcome != domain.OutcomeProcessedFailed {
		t.Fatalf("deny webhook outcome = %s, want PROCESSED_FAILED", denyRes.Outcome)
	}
	assertWebhookAuditByTxID(t, env, denyTx, domain.OutcomeProcessedFailed, false)

	failedOrd := fetchOrder(t, env, ord.ID, buyer)
	if failedOrd.Status != domain.OrderStatusCancelled {
		t.Fatalf("order status after deny webhook = %s, want CANCELLED", failedOrd.Status)
	}
	restoredEv := fetchEvent(t, env, ev.ID)
	if rem := findTierQuota(t, restoredEv, regID); rem != 10 {
		t.Fatalf("remaining_quota after deny webhook = %d, want 10", rem)
	}
}

func TestIntegration_WebhookIdempotency_10ParallelReplays(t *testing.T) {
	env := setupTestEnv(t)
	ev := createTestEvent(t, env, 10, 5)
	regID := ev.TicketTypes[0].ID
	buyer := roleHeaders(uuid.NewString(), roleBuyer)

	ord := createOrder(t, env, buyer, ev.ID, []map[string]any{
		{"ticket_type_id": regID.String(), "quantity": 4},
	})

	txID := "tx-idempotent-" + uuid.NewString()
	payload := signedWebhookPayload(txID, ord.ID, ord.TotalAmountIDR, txStatusSettlement)
	const replayCount = 10
	specs := make([]concurrentRequestSpec, replayCount)
	for i := range specs {
		specs[i] = concurrentRequestSpec{
			targetURL: env.server.URL + "/api/v1/webhooks/payment",
			method:    http.MethodPost,
			body:      payload,
		}
	}

	results := runConcurrentRequests(t.Context(), env.client, specs)
	paidCount, dupCount := tallyWebhookReplayResults(t, results)
	if paidCount != 1 || dupCount != 9 {
		t.Fatalf("expected 1 PROCESSED_PAID and 9 IGNORED_DUPLICATE, got paid=%d dup=%d", paidCount, dupCount)
	}

	issuedTickets := querySingleValue[int64](t, env, "SELECT COUNT(*) FROM tickets WHERE order_id = $1", ord.ID)
	if issuedTickets != 4 {
		t.Fatalf("expected 4 issued tickets, got %d", issuedTickets)
	}

	auditPaid := querySingleValue[int64](
		t,
		env,
		"SELECT COUNT(*) FROM payment_webhooks WHERE transaction_id = $1 AND outcome = $2",
		txID,
		string(domain.OutcomeProcessedPaid),
	)
	auditDup := querySingleValue[int64](
		t,
		env,
		"SELECT COUNT(*) FROM payment_webhooks WHERE transaction_id = $1 AND outcome = $2",
		txID,
		string(domain.OutcomeIgnoredDuplicate),
	)
	if auditPaid != 1 || auditDup != 9 {
		t.Fatalf("expected webhook audit counts (1, 9), got (%d, %d)", auditPaid, auditDup)
	}
}

func TestIntegration_FlashSale_200ConcurrentBuyers_ZeroOversell(t *testing.T) {
	env := setupTestEnv(t)
	ev := createTestEvent(t, env, 100, 10)
	regTierID := ev.TicketTypes[0].ID

	const totalBuyers = 200
	specs := make([]concurrentRequestSpec, totalBuyers)
	for i := range specs {
		specs[i] = concurrentRequestSpec{
			targetURL: env.server.URL + "/api/v1/orders",
			method:    http.MethodPost,
			body: map[string]any{
				"event_id": ev.ID.String(),
				"items":    []map[string]any{{"ticket_type_id": regTierID.String(), "quantity": 1}},
			},
			headers: roleHeaders(uuid.NewString(), roleBuyer),
		}
	}

	results := runConcurrentRequests(t.Context(), env.client, specs)
	var soldCount, rejectedCount, unexpectedCount int
	for _, res := range results {
		if res.err != nil {
			t.Fatalf("flash sale request error: %v", res.err)
		}
		switch res.status {
		case http.StatusCreated:
			soldCount++
		case http.StatusConflict:
			rejectedCount++
		default:
			unexpectedCount++
		}
	}

	if unexpectedCount != 0 {
		t.Fatalf("unexpected non-201/409 responses: %d", unexpectedCount)
	}
	if soldCount != 100 || rejectedCount != 100 {
		t.Fatalf("expected 100 sold and 100 rejected, got sold=%d rejected=%d", soldCount, rejectedCount)
	}

	dbSold := querySingleValue[int64](
		t,
		env,
		"SELECT COALESCE(SUM(total_tickets), 0) FROM orders WHERE event_id = $1 AND status IN ('WAITING_PAYMENT', 'PAID')",
		ev.ID,
	)
	if dbSold != 100 {
		t.Fatalf("expected DB total_tickets = 100, got %d", dbSold)
	}

	remQuota := querySingleValue[int32](t, env, "SELECT remaining_quota FROM ticket_types WHERE id = $1", regTierID)
	if remQuota != 0 {
		t.Fatalf("expected remaining_quota = 0, got %d", remQuota)
	}
}
