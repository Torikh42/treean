//go:build integration

package tests_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/torikh42/treean/internal/config"
	"github.com/torikh42/treean/internal/domain"
	"github.com/torikh42/treean/internal/handler"
	"github.com/torikh42/treean/internal/middleware"
	"github.com/torikh42/treean/internal/redisstore"
	"github.com/torikh42/treean/internal/repository"
	"github.com/torikh42/treean/internal/routes"
	"github.com/torikh42/treean/internal/security"
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
	pool        *pgxpool.Pool
	server      *httptest.Server
	client      *http.Client
	sweeper     *worker.ExpirySweeper
	redisRaw    *redis.Client
	redisClient *redisstore.Client
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
	return setupTestEnvWithRateLimit(t, middleware.RateLimitPolicy{Enabled: false}, 0)
}

func setupTestEnvWithRateLimit(t *testing.T, policy middleware.RateLimitPolicy, loginAcctMax int) *testEnv {
	t.Helper()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = defaultTestDBURL
	}
	redisURL := os.Getenv("REDIS_URL")

	cfg := config.Config{
		DatabaseURL:         dbURL,
		RedisURL:            redisURL,
		RedisTimeout:        500 * time.Millisecond,
		EventCacheTTL:       3 * time.Second,
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

	logger := slog.New(slog.DiscardHandler)
	var redisRaw *redis.Client
	var redisStore *redisstore.Client
	if redisURL != "" {
		opts, parseErr := redis.ParseURL(redisURL)
		if parseErr != nil {
			t.Fatalf("parsing test REDIS_URL: %v", parseErr)
		}
		redisRaw = redis.NewClient(opts)
		t.Cleanup(func() { _ = redisRaw.Close() })
		if err := redisRaw.FlushDB(ctx).Err(); err != nil {
			t.Fatalf("flushing test Redis DB: %v", err)
		}
		redisStore = redisstore.NewClientFromGoRedis(redisRaw, cfg.RedisTimeout, logger)
	}

	userRepo := repository.NewUserRepo(pool)
	eventRepo := repository.NewEventRepo(pool)
	orderRepo := repository.NewOrderRepo(pool)
	webhookRepo := repository.NewWebhookRepo(pool)

	authSvc := service.NewAuthService(userRepo, cfg.JWTSecret)
	eventSvc := service.NewEventService(eventRepo, orderRepo)
	orderSvc := service.NewOrderService(orderRepo, eventRepo)
	paymentSvc := service.NewPaymentService(orderRepo, webhookRepo, eventRepo, cfg.WebhookSecret)
	sweeper := worker.NewExpirySweeper(orderRepo, logger, time.Second, 50)
	var limiter middleware.RateLimiter
	if redisStore != nil {
		eventSvc.WithCache(redisStore, cfg.EventCacheTTL)
		orderSvc.WithCache(redisStore)
		paymentSvc.WithCache(redisStore)
		sweeper.WithCache(redisStore)
		if policy.Enabled {
			limiter = redisStore
			if policy.Challenge != nil && policy.NonceStore == nil {
				policy.NonceStore = redisStore
			}
			if loginAcctMax > 0 {
				authSvc.WithLoginRateLimiter(redisStore, loginAcctMax)
			}
		}
	}
	demoSvc := service.NewDemoService(userRepo, eventRepo, orderSvc)

	r := routes.NewRouter(routes.RouterDeps{
		Logger:              logger,
		Authenticator:       authSvc,
		System:              handler.NewSystemHandler(pool, logger),
		Auth:                handler.NewAuthHandler(authSvc, logger),
		Events:              handler.NewEventHandler(eventSvc, logger),
		Orders:              handler.NewOrderHandler(orderSvc, logger),
		Webhooks:            handler.NewWebhookHandler(paymentSvc, logger),
		Demo:                handler.NewDemoHandler(demoSvc, logger),
		RateLimiter:         limiter,
		RateLimitPolicy:     policy,
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
		pool:        pool,
		server:      srv,
		client:      &http.Client{Timeout: 30 * time.Second, Transport: transport},
		sweeper:     sweeper,
		redisRaw:    redisRaw,
		redisClient: redisStore,
	}
}

func executeJSONWithHeaders(
	ctx context.Context,
	client *http.Client,
	targetURL string,
	method string,
	body any,
	headers map[string]string,
) (int, http.Header, []byte, error) {
	if client == nil {
		return 0, nil, nil, errors.New("http client is required")
	}

	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, nil, fmt.Errorf("marshaling request body: %w", err)
		}
		reader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, targetURL, reader)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("building %s %s request: %w", method, targetURL, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("executing %s %s: %w", method, targetURL, err)
	}

	raw, readErr := io.ReadAll(resp.Body)
	closeErr := resp.Body.Close()
	if readErr != nil {
		return resp.StatusCode, resp.Header, nil, fmt.Errorf("reading %s %s response body: %w", method, targetURL, readErr)
	}
	if closeErr != nil {
		return resp.StatusCode, resp.Header, nil, fmt.Errorf("closing %s %s response body: %w", method, targetURL, closeErr)
	}
	return resp.StatusCode, resp.Header, raw, nil
}

func executeJSON(
	ctx context.Context,
	client *http.Client,
	targetURL string,
	method string,
	body any,
	headers map[string]string,
) (int, []byte, error) {
	status, _, raw, err := executeJSONWithHeaders(ctx, client, targetURL, method, body, headers)
	return status, raw, err
}

func mustJSONWithHeaders(
	t *testing.T,
	env *testEnv,
	path string,
	method string,
	body any,
	headers map[string]string,
) (int, http.Header, []byte) {
	t.Helper()

	status, respHeaders, raw, err := executeJSONWithHeaders(t.Context(), env.client, env.server.URL+path, method, body, headers)
	if err != nil {
		t.Fatalf("%s %s failed: %v", method, path, err)
	}
	return status, respHeaders, raw
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

func registerIntegrationJWT(t *testing.T, env *testEnv, emailPrefix, name, role string) service.AuthResult {
	t.Helper()

	email := fmt.Sprintf("%s-%s@event.local", emailPrefix, uuid.NewString()[:8])
	status, raw := mustJSON(t, env, "/api/v1/auth/register", http.MethodPost, map[string]any{
		"email":    email,
		"name":     name,
		"password": "password123",
		"role":     role,
	}, nil)
	if status != http.StatusCreated {
		t.Fatalf("register %s status = %d, want 201 (body=%s)", email, status, string(raw))
	}
	return mustUnmarshal[service.AuthResult](t, raw)
}

func bearerHeaders(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

func findTierByName(t *testing.T, ev domain.Event, name string) domain.TicketType {
	t.Helper()

	for _, tt := range ev.TicketTypes {
		if tt.Name == name {
			return tt
		}
	}
	t.Fatalf("tier %q not found in event %s (%+v)", name, ev.ID, ev.TicketTypes)
	return domain.TicketType{}
}

func TestIntegration_AuthMe_And_OrganizerEventsList(t *testing.T) {
	env := setupTestEnv(t)

	org1 := registerIntegrationJWT(t, env, "org1", "Org One", roleOrganizer)
	org2 := registerIntegrationJWT(t, env, "org2", "Org Two", roleOrganizer)
	buyer := registerIntegrationJWT(t, env, "buyer", "Buyer One", roleBuyer)

	unauthStatus, unauthRaw := mustJSON(t, env, "/api/v1/auth/me", http.MethodGet, nil, nil)
	assertAPIError(t, unauthStatus, unauthRaw, http.StatusUnauthorized, domain.ErrCodeUnauthorized)

	meStatus, meRaw := mustJSON(t, env, "/api/v1/auth/me", http.MethodGet, nil, bearerHeaders(buyer.AccessToken))
	if meStatus != http.StatusOK {
		t.Fatalf("GET /api/v1/auth/me status = %d, want 200 (body=%s)", meStatus, string(meRaw))
	}
	if bytes.Contains(meRaw, []byte("password_hash")) {
		t.Fatalf("GET /api/v1/auth/me leaked password_hash: %s", string(meRaw))
	}
	me := mustUnmarshal[domain.User](t, meRaw)
	if me.ID != buyer.User.ID || me.Role != domain.RoleBuyer {
		t.Fatalf("unexpected /auth/me payload: %+v", me)
	}

	future := time.Now().UTC().Add(48 * time.Hour)
	ev1Status, ev1Raw := mustJSON(t, env, "/api/v1/events", http.MethodPost, map[string]any{
		"title": "Org1 Event", "venue": "Hall 1", "event_date": future,
		"ticket_types": []map[string]any{
			{"name": "Reguler", "price_idr": 100000, "total_quota": 20},
			{"name": "VIP", "price_idr": 250000, "total_quota": 10},
		},
	}, bearerHeaders(org1.AccessToken))
	if ev1Status != http.StatusCreated {
		t.Fatalf("create org1 event status = %d, want 201 (body=%s)", ev1Status, string(ev1Raw))
	}
	ev1 := mustUnmarshal[domain.Event](t, ev1Raw)

	ev2Status, ev2Raw := mustJSON(t, env, "/api/v1/events", http.MethodPost, map[string]any{
		"title": "Org2 Event", "venue": "Hall 2", "event_date": future,
		"ticket_types": []map[string]any{
			{"name": "Reguler", "price_idr": 100000, "total_quota": 20},
			{"name": "VIP", "price_idr": 250000, "total_quota": 10},
		},
	}, bearerHeaders(org2.AccessToken))
	if ev2Status != http.StatusCreated {
		t.Fatalf("create org2 event status = %d, want 201 (body=%s)", ev2Status, string(ev2Raw))
	}

	buyerListStatus, buyerListRaw := mustJSON(t, env, "/api/v1/organizer/events", http.MethodGet, nil, bearerHeaders(buyer.AccessToken))
	assertAPIError(t, buyerListStatus, buyerListRaw, http.StatusForbidden, domain.ErrCodeForbidden)

	org1ListStatus, org1ListRaw := mustJSON(t, env, "/api/v1/organizer/events", http.MethodGet, nil, bearerHeaders(org1.AccessToken))
	if org1ListStatus != http.StatusOK {
		t.Fatalf("GET /api/v1/organizer/events status = %d, want 200 (body=%s)", org1ListStatus, string(org1ListRaw))
	}
	listed := mustUnmarshal[[]domain.Event](t, org1ListRaw)
	if len(listed) != 1 || listed[0].ID != ev1.ID {
		t.Fatalf("GET /api/v1/organizer/events = %+v, want only event %s", listed, ev1.ID)
	}
}

func TestIntegration_OrganizerEventUpdate_SwapReuseAndConflicts(t *testing.T) {
	env := setupTestEnv(t)

	org := registerIntegrationJWT(t, env, "org-upd", "Org Update", roleOrganizer)
	buyer := registerIntegrationJWT(t, env, "buyer-upd", "Buyer Update", roleBuyer)
	future := time.Now().UTC().Add(72 * time.Hour)

	createStatus, createRaw := mustJSON(t, env, "/api/v1/events", http.MethodPost, map[string]any{
		"title": "Swap Event", "venue": "Hall A", "event_date": future,
		"ticket_types": []map[string]any{
			{"name": "Reguler", "price_idr": 150000, "total_quota": 20},
			{"name": "VIP", "price_idr": 350000, "total_quota": 10},
		},
	}, bearerHeaders(org.AccessToken))
	if createStatus != http.StatusCreated {
		t.Fatalf("create event status = %d, want 201 (body=%s)", createStatus, string(createRaw))
	}
	ev := mustUnmarshal[domain.Event](t, createRaw)
	regTier := findTierByName(t, ev, "Reguler")
	vipTier := findTierByName(t, ev, "VIP")

	_ = createOrder(t, env, bearerHeaders(buyer.AccessToken), ev.ID, []map[string]any{
		{"ticket_type_id": regTier.ID.String(), "quantity": 3},
	})

	// 1. Quota shrink below sold/held (3) -> 409 QUOTA_CONFLICT.
	shrinkStatus, shrinkRaw := mustJSON(t, env, "/api/v1/events/"+ev.ID.String(), http.MethodPut, map[string]any{
		"title": "Swap Event", "venue": "Hall A", "event_date": future,
		"ticket_types": []map[string]any{
			{"id": regTier.ID.String(), "name": "Reguler", "price_idr": 150000, "total_quota": 2},
			{"id": vipTier.ID.String(), "name": "VIP", "price_idr": 350000, "total_quota": 10},
		},
	}, bearerHeaders(org.AccessToken))
	assertAPIError(t, shrinkStatus, shrinkRaw, http.StatusConflict, domain.ErrCodeQuotaConflict)

	// 2. Removing tier with orders -> 409 TIER_HAS_ORDERS.
	dropStatus, dropRaw := mustJSON(t, env, "/api/v1/events/"+ev.ID.String(), http.MethodPut, map[string]any{
		"title": "Swap Event", "venue": "Hall A", "event_date": future,
		"ticket_types": []map[string]any{
			{"id": vipTier.ID.String(), "name": "VIP", "price_idr": 350000, "total_quota": 10},
			{"name": "VVIP", "price_idr": 500000, "total_quota": 5},
		},
	}, bearerHeaders(org.AccessToken))
	assertAPIError(t, dropStatus, dropRaw, http.StatusConflict, domain.ErrCodeTierHasOrders)

	// 3. Swap names between existing tiers (Reguler <-> VIP) -> 200 without unique violation.
	swapStatus, swapRaw := mustJSON(t, env, "/api/v1/events/"+ev.ID.String(), http.MethodPut, map[string]any{
		"title": "Swap Event v2", "venue": "Hall B", "event_date": future,
		"ticket_types": []map[string]any{
			{"id": regTier.ID.String(), "name": "VIP", "price_idr": 175000, "total_quota": 25},
			{"id": vipTier.ID.String(), "name": "Reguler", "price_idr": 350000, "total_quota": 12},
		},
	}, bearerHeaders(org.AccessToken))
	if swapStatus != http.StatusOK {
		t.Fatalf("swap PUT status = %d, want 200 (body=%s)", swapStatus, string(swapRaw))
	}
	swapped := mustUnmarshal[domain.Event](t, swapRaw)
	if got := findTierByName(t, swapped, "VIP"); got.ID != regTier.ID || got.RemainingQuota != 22 {
		t.Fatalf("unexpected swapped VIP tier: %+v", got)
	}

	// 4. Rename kept tier ("VIP" -> "Patron") and reuse old name ("VIP") for a brand-new tier -> 200.
	reuseStatus, reuseRaw := mustJSON(t, env, "/api/v1/events/"+ev.ID.String(), http.MethodPut, map[string]any{
		"title": "Swap Event v3", "venue": "Hall C", "event_date": future,
		"ticket_types": []map[string]any{
			{"id": regTier.ID.String(), "name": "Patron", "price_idr": 200000, "total_quota": 30},
			{"id": vipTier.ID.String(), "name": "Reguler", "price_idr": 350000, "total_quota": 12},
			{"name": "VIP", "price_idr": 600000, "total_quota": 8},
		},
	}, bearerHeaders(org.AccessToken))
	if reuseStatus != http.StatusOK {
		t.Fatalf("rename+reuse PUT status = %d, want 200 (body=%s)", reuseStatus, string(reuseRaw))
	}
	reused := mustUnmarshal[domain.Event](t, reuseRaw)
	if len(reused.TicketTypes) != 3 {
		t.Fatalf("expected 3 tiers after reuse PUT, got %d", len(reused.TicketTypes))
	}
	if newVIP := findTierByName(t, reused, "VIP"); newVIP.ID == regTier.ID || newVIP.RemainingQuota != 8 {
		t.Fatalf("unexpected new VIP tier: %+v", newVIP)
	}
}

func TestIntegration_OrganizerEventDelete(t *testing.T) {
	env := setupTestEnv(t)

	org := registerIntegrationJWT(t, env, "org-del", "Org Delete", roleOrganizer)
	buyer := registerIntegrationJWT(t, env, "buyer-del", "Buyer Delete", roleBuyer)
	future := time.Now().UTC().Add(72 * time.Hour)

	makeEvent := func(title string) domain.Event {
		st, raw := mustJSON(t, env, "/api/v1/events", http.MethodPost, map[string]any{
			"title": title, "venue": "Hall D", "event_date": future,
			"ticket_types": []map[string]any{
				{"name": "Reguler", "price_idr": 100000, "total_quota": 10},
				{"name": "VIP", "price_idr": 200000, "total_quota": 5},
			},
		}, bearerHeaders(org.AccessToken))
		if st != http.StatusCreated {
			t.Fatalf("create event status = %d, want 201 (body=%s)", st, string(raw))
		}
		return mustUnmarshal[domain.Event](t, raw)
	}

	usedEv := makeEvent("Used Event")
	unusedEv := makeEvent("Unused Event")
	_ = createOrder(t, env, bearerHeaders(buyer.AccessToken), usedEv.ID, []map[string]any{
		{"ticket_type_id": usedEv.TicketTypes[0].ID.String(), "quantity": 1},
	})

	delUsedStatus, delUsedRaw := mustJSON(t, env, "/api/v1/events/"+usedEv.ID.String(), http.MethodDelete, nil, bearerHeaders(org.AccessToken))
	assertAPIError(t, delUsedStatus, delUsedRaw, http.StatusConflict, domain.ErrCodeEventHasOrders)

	delUnusedStatus, delUnusedRaw := mustJSON(t, env, "/api/v1/events/"+unusedEv.ID.String(), http.MethodDelete, nil, bearerHeaders(org.AccessToken))
	if delUnusedStatus != http.StatusNoContent || len(delUnusedRaw) != 0 {
		t.Fatalf("DELETE unused event = (%d, %q), want 204 empty body", delUnusedStatus, string(delUnusedRaw))
	}

	getGoneStatus, getGoneRaw := mustJSON(t, env, "/api/v1/events/"+unusedEv.ID.String(), http.MethodGet, nil, nil)
	assertAPIError(t, getGoneStatus, getGoneRaw, http.StatusNotFound, domain.ErrCodeEventNotFound)
}

func TestIntegration_OrderIdempotencyKey_ConcurrentReplay(t *testing.T) {
	env := setupTestEnv(t)

	buyerID := uuid.NewString()
	ev := createTestEvent(t, env, 2, 10)
	regTier := findTierByName(t, ev, "Reguler")
	idempKey := "checkout-idemp-" + uuid.NewString()

	specs := make([]concurrentRequestSpec, 5)
	for i := range specs {
		headers := roleHeaders(buyerID, roleBuyer)
		headers["Idempotency-Key"] = idempKey
		specs[i] = concurrentRequestSpec{
			targetURL: env.server.URL + "/api/v1/orders",
			method:    http.MethodPost,
			body: map[string]any{
				"event_id": ev.ID.String(),
				"items": []map[string]any{
					{"ticket_type_id": regTier.ID.String(), "quantity": 2},
				},
			},
			headers: headers,
		}
	}
	results := runConcurrentRequests(t.Context(), env.client, specs)
	orderIDs := make(map[uuid.UUID]struct{})
	for _, r := range results {
		if r.err != nil {
			t.Fatalf("concurrent idempotent order request error: %v", r.err)
		}
		if r.status != http.StatusCreated {
			t.Fatalf("expected 201 on idempotent burst, got %d (%s)", r.status, string(r.raw))
		}
		ord := mustUnmarshal[domain.Order](t, r.raw)
		orderIDs[ord.ID] = struct{}{}
	}
	if len(orderIDs) != 1 {
		t.Fatalf("expected exactly 1 distinct order created for identical Idempotency-Key, got %d", len(orderIDs))
	}

	// Verify quota reached 0 (2 requested from 2 total) and repeating same key STILL replays 201 instead of 409.
	updatedEv := fetchEvent(t, env, ev.ID)
	if rem := findTierQuota(t, updatedEv, regTier.ID); rem != 0 {
		t.Fatalf("expected Reguler quota=0 after single order, got %d", rem)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, env.server.URL+"/api/v1/orders", bytes.NewReader(mustMarshalJSON(t, specs[0].body)))
	if err != nil {
		t.Fatalf("build replay request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", buyerID)
	req.Header.Set("X-User-Role", roleBuyer)
	req.Header.Set("Idempotency-Key", idempKey)
	resp, err := env.client.Do(req)
	if err != nil {
		t.Fatalf("execute replay request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated || resp.Header.Get("Idempotent-Replay") != "true" {
		t.Fatalf("expected 201 + Idempotent-Replay: true, got status=%d header=%q", resp.StatusCode, resp.Header.Get("Idempotent-Replay"))
	}
}

func TestIntegration_RateLimiting_And_EventCache_WithRedis(t *testing.T) {
	if os.Getenv("REDIS_URL") == "" {
		t.Skip("REDIS_URL not configured")
	}

	policy := middleware.RateLimitPolicy{
		Enabled:          true,
		RegisterMax:      2,
		LoginIPMax:       20,
		OrderUserMax:     2,
		OrderIPMax:       20,
		DemoFlashSaleMax: 2,
	}
	env := setupTestEnvWithRateLimit(t, policy, 2)

	// 1. Register endpoint enforces strict IP rate limit (allows 2, blocks 3rd with 429 RATE_LIMITED).
	for i := range 2 {
		st, raw := mustJSON(t, env, "/api/v1/auth/register", http.MethodPost, map[string]any{
			"email": fmt.Sprintf("rl-reg-%d-%s@event.local", i, uuid.NewString()[:8]), "name": "RL User",
			"password": "password123", "role": roleBuyer,
		}, nil)
		if st != http.StatusCreated {
			t.Fatalf("register %d status = %d, want 201 (%s)", i+1, st, string(raw))
		}
	}
	st3, raw3 := mustJSON(t, env, "/api/v1/auth/register", http.MethodPost, map[string]any{
		"email": "rl-reg-3@event.local", "name": "RL Blocked",
		"password": "password123", "role": roleBuyer,
	}, nil)
	assertAPIError(t, st3, raw3, http.StatusTooManyRequests, domain.ErrCodeRateLimited)

	// 2. Per-user checkout rate limiting blocks buyer1 on 3rd order while buyer2 on same IP succeeds.
	buyer1Headers := roleHeaders(uuid.NewString(), roleBuyer)
	buyer2Headers := roleHeaders(uuid.NewString(), roleBuyer)

	ev := createTestEvent(t, env, 20, 10)
	regTier := findTierByName(t, ev, "Reguler")
	for range 2 {
		_ = createOrder(t, env, buyer1Headers, ev.ID, []map[string]any{
			{"ticket_type_id": regTier.ID.String(), "quantity": 1},
		})
	}
	stB1, rawB1 := mustJSON(t, env, "/api/v1/orders", http.MethodPost, map[string]any{
		"event_id": ev.ID.String(),
		"items":    []map[string]any{{"ticket_type_id": regTier.ID.String(), "quantity": 1}},
	}, buyer1Headers)
	assertAPIError(t, stB1, rawB1, http.StatusTooManyRequests, domain.ErrCodeRateLimited)

	ordB2 := createOrder(t, env, buyer2Headers, ev.ID, []map[string]any{
		{"ticket_type_id": regTier.ID.String(), "quantity": 1},
	})

	// 3. Populate detail cache, then expire buyer2's order via ExpirySweeper and verify cache immediately invalidates.
	cachedView := fetchEvent(t, env, ev.ID)
	if rem := findTierQuota(t, cachedView, regTier.ID); rem != 17 {
		t.Fatalf("expected cached remaining_quota=17, got %d", rem)
	}
	if _, err := env.pool.Exec(t.Context(), "UPDATE orders SET expires_at = NOW() - INTERVAL '1 minute' WHERE id = $1", ordB2.ID); err != nil {
		t.Fatalf("backdate expires_at: %v", err)
	}
	env.sweeper.RunOnce(t.Context())
	afterSweep := fetchEvent(t, env, ev.ID)
	if rem := findTierQuota(t, afterSweep, regTier.ID); rem != 18 {
		t.Fatalf("expected invalidated event cache after sweep showing 18, got %d", rem)
	}
}

func TestIntegration_CGNATSharedIPCheckoutsAndAdaptivePoW(t *testing.T) {
	if os.Getenv("REDIS_URL") == "" {
		t.Skip("REDIS_URL not configured")
	}

	const (
		sharedCGNATIP = "103.55.10.2"
		powDifficulty = 8
	)
	pow, err := security.NewPoWVerifier("integration-pow-secret", powDifficulty, 2*time.Minute)
	if err != nil {
		t.Fatalf("NewPoWVerifier: %v", err)
	}

	policy := middleware.RateLimitPolicy{
		Enabled:          true,
		RegisterMax:      5,
		RegisterHardMax:  100,
		LoginIPMax:       60,
		LoginIPHardMax:   600,
		OrderUserMax:     20,
		OrderIPMax:       0,
		DemoFlashSaleMax: 5,
		Challenge:        pow,
	}
	env := setupTestEnvWithRateLimit(t, policy, 10)

	// 1. 10 distinct authenticated buyers sharing one CG-NAT IP checkout without hitting IP 429.
	ev := createTestEvent(t, env, 20, 10)
	regTier := findTierByName(t, ev, "Reguler")
	for range 10 {
		headers := roleHeaders(uuid.NewString(), roleBuyer)
		headers["X-Real-IP"] = sharedCGNATIP
		ord := createOrder(t, env, headers, ev.ID, []map[string]any{
			{"ticket_type_id": regTier.ID.String(), "quantity": 1},
		})
		if ord.TotalTickets != 1 {
			t.Fatalf("order total_tickets = %d, want 1", ord.TotalTickets)
		}
	}
	assertEventTierQuotas(t, env, ev.ID, regTier.ID, ev.TicketTypes[1].ID, 10, 10)

	// 2. Shared-IP registrations 1..5 pass without PoW; 6th triggers 403 CHALLENGE_REQUIRED.
	for i := range 5 {
		st, _, raw := mustJSONWithHeaders(t, env, "/api/v1/auth/register", http.MethodPost, map[string]any{
			"email":    fmt.Sprintf("cgnat-reg-%d-%s@event.local", i+1, uuid.NewString()[:8]),
			"name":     fmt.Sprintf("CGNAT Buyer %d", i+1),
			"password": "password123",
			"role":     roleBuyer,
		}, map[string]string{"X-Real-IP": sharedCGNATIP})
		if st != http.StatusCreated {
			t.Fatalf("registration %d status = %d, want 201 (body=%s)", i+1, st, string(raw))
		}
	}

	chStatus, chHeaders, chRaw := mustJSONWithHeaders(t, env, "/api/v1/auth/register", http.MethodPost, map[string]any{
		"email":    fmt.Sprintf("cgnat-reg-6-%s@event.local", uuid.NewString()[:8]),
		"name":     "CGNAT Buyer 6",
		"password": "password123",
		"role":     roleBuyer,
	}, map[string]string{"X-Real-IP": sharedCGNATIP})
	assertAPIError(t, chStatus, chRaw, http.StatusForbidden, domain.ErrCodeChallengeRequired)
	challenge := chHeaders.Get("X-PoW-Challenge")
	if challenge == "" {
		t.Fatal("expected non-empty X-PoW-Challenge header on 403 response")
	}

	// 3. Solving challenge and retrying with X-PoW-Solution succeeds (201 Created).
	solution := solvePoWChallengeForTest(t, challenge, powDifficulty)
	okStatus, _, okRaw := mustJSONWithHeaders(t, env, "/api/v1/auth/register", http.MethodPost, map[string]any{
		"email":    fmt.Sprintf("cgnat-reg-solved-%s@event.local", uuid.NewString()[:8]),
		"name":     "CGNAT Buyer Solved",
		"password": "password123",
		"role":     roleBuyer,
	}, map[string]string{
		"X-Real-IP":      sharedCGNATIP,
		"X-PoW-Solution": solution,
	})
	if okStatus != http.StatusCreated {
		t.Fatalf("solved PoW retry status = %d, want 201 (body=%s)", okStatus, string(okRaw))
	}

	// 4. Replaying the same X-PoW-Solution against live Redis is rejected (403 CHALLENGE_REQUIRED).
	replayStatus, replayHeaders, replayRaw := mustJSONWithHeaders(t, env, "/api/v1/auth/register", http.MethodPost, map[string]any{
		"email":    fmt.Sprintf("cgnat-reg-replay-%s@event.local", uuid.NewString()[:8]),
		"name":     "CGNAT Buyer Replay",
		"password": "password123",
		"role":     roleBuyer,
	}, map[string]string{
		"X-Real-IP":      sharedCGNATIP,
		"X-PoW-Solution": solution,
	})
	assertAPIError(t, replayStatus, replayRaw, http.StatusForbidden, domain.ErrCodeChallengeRequired)
	freshChallenge := replayHeaders.Get("X-PoW-Challenge")
	if freshChallenge == "" || freshChallenge == challenge {
		t.Fatalf("expected fresh X-PoW-Challenge on replay, got %q (prev %q)", freshChallenge, challenge)
	}
}

func solvePoWChallengeForTest(t *testing.T, challenge string, difficulty int) string {
	t.Helper()
	for nonce := 0; nonce < 5_000_000; nonce++ {
		nonceStr := strconv.Itoa(nonce)
		sum := sha256.Sum256([]byte(challenge + ":" + nonceStr))
		if hasLeadingZeroBitsForTest(sum[:], difficulty) {
			return challenge + ":" + nonceStr
		}
	}
	t.Fatalf("failed to solve PoW challenge %q at difficulty %d", challenge, difficulty)
	return ""
}

func hasLeadingZeroBitsForTest(digest []byte, bits int) bool {
	fullBytes := bits / 8
	for i := range fullBytes {
		if digest[i] != 0 {
			return false
		}
	}
	remBits := bits % 8
	if remBits == 0 {
		return true
	}
	mask := byte(0xFF << (8 - remBits))
	return (digest[fullBytes] & mask) == 0
}

func mustMarshalJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return b
}
