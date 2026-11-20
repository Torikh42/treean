package middleware_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/torikh42/treean/internal/domain"
	"github.com/torikh42/treean/internal/handler"
	"github.com/torikh42/treean/internal/middleware"
)

func TestMain(m *testing.M) {
	handler.ConfigureHumaErrorFactory()
	os.Exit(m.Run())
}

type memoryLimiter struct {
	mu      sync.Mutex
	counts  map[string]int
	failErr error
}

func (m *memoryLimiter) Allow(_ context.Context, key string, limit int, window time.Duration) (bool, time.Duration, error) {
	if m.failErr != nil {
		return false, 0, m.failErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.counts == nil {
		m.counts = make(map[string]int)
	}
	m.counts[key]++
	if m.counts[key] > limit {
		return false, window, nil
	}
	return true, 0, nil
}

func TestWithClientIP_ProxyTrustAndSpoofPrevention(t *testing.T) {
	t.Parallel()

	captureIP := func(remoteAddr string, headers map[string]string) string {
		var got string
		h := middleware.WithClientIP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = middleware.ClientIPFromContext(r.Context())
			w.WriteHeader(http.StatusOK)
		}))
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
		req.RemoteAddr = remoteAddr
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		h.ServeHTTP(httptest.NewRecorder(), req)
		return got
	}

	// Untrusted public peer attempting X-Forwarded-For spoofing must fall back to TCP peer IP.
	if got := captureIP("198.51.100.25:43210", map[string]string{
		"X-Forwarded-For": "1.2.3.4",
		"X-Real-IP":       "5.6.7.8",
	}); got != "198.51.100.25" {
		t.Fatalf("untrusted peer IP = %q, want 198.51.100.25", got)
	}

	// Trusted loopback/Docker bridge proxy forwarding verified X-Real-IP takes client IP.
	if got := captureIP("172.18.0.5:55555", map[string]string{
		"X-Real-IP":       "203.0.113.99",
		"X-Forwarded-For": "1.1.1.1, 203.0.113.99",
	}); got != "203.0.113.99" {
		t.Fatalf("trusted proxy X-Real-IP = %q, want 203.0.113.99", got)
	}

	// Trusted proxy without X-Real-IP reads rightmost valid X-Forwarded-For, ignoring client leftmost spoof.
	if got := captureIP("127.0.0.1:40000", map[string]string{
		"X-Forwarded-For": "9.9.9.9, 203.0.113.77",
	}); got != "203.0.113.77" {
		t.Fatalf("trusted proxy rightmost XFF = %q, want 203.0.113.77", got)
	}
}

func TestHumaRateLimitMiddleware_UserIDAndRegisterIP(t *testing.T) {
	t.Parallel()

	buyerA := uuid.MustParse("22222222-2222-2222-2222-222222222221")
	buyerB := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	limiter := &memoryLimiter{}
	bearerSec := []map[string][]string{{"bearer": {}}}

	setupRouter := func(actorID uuid.UUID, lim middleware.RateLimiter) http.Handler {
		r := chi.NewRouter()
		r.Use(middleware.WithClientIP)
		cfg := huma.DefaultConfig("RL Test", "1.0.0")
		cfg.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
			"bearer": {Type: "http", Scheme: "bearer", BearerFormat: "JWT"},
		}
		api := humachi.New(r, cfg)
		if actorID != uuid.Nil {
			api.UseMiddleware(middleware.NewHumaAuthMiddleware(
				api,
				&stubAuthenticator{actor: domain.Actor{ID: actorID, Role: domain.RoleBuyer}},
				slog.New(slog.NewTextHandler(io.Discard, nil)),
				false,
			))
		}
		api.UseMiddleware(middleware.NewHumaRateLimitMiddleware(api, lim, middleware.RateLimitPolicy{
			Enabled:          true,
			RegisterMax:      2,
			LoginIPMax:       10,
			OrderUserMax:     2,
			OrderIPMax:       5,
			DemoFlashSaleMax: 2,
		}))

		huma.Register(api, huma.Operation{OperationID: "register-user", Method: http.MethodPost, Path: "/auth/register"},
			func(context.Context, *struct{}) (*struct{}, error) { return nil, nil })
		huma.Register(api, huma.Operation{OperationID: "create-order", Method: http.MethodPost, Path: "/orders", Security: bearerSec},
			func(context.Context, *struct{}) (*struct{}, error) { return nil, nil })
		return r
	}

	doPost := func(h http.Handler, path, remoteAddr, auth string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, nil)
		req.RemoteAddr = remoteAddr
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	// Register limit is enforced per IP.
	regRouter := setupRouter(uuid.Nil, limiter)
	for i := range 2 {
		if resp := doPost(regRouter, "/auth/register", "10.0.0.5:1234", ""); resp.Code >= 400 {
			t.Fatalf("register call %d unexpected status %d", i+1, resp.Code)
		}
	}
	blockedReg := doPost(regRouter, "/auth/register", "10.0.0.5:1234", "")
	if blockedReg.Code != http.StatusTooManyRequests {
		t.Fatalf("register call 3 status = %d, want 429", blockedReg.Code)
	}
	if blockedReg.Header().Get("Retry-After") == "" {
		t.Fatal("expected Retry-After header on 429 response")
	}
	var errBody map[string]any
	if err := json.Unmarshal(blockedReg.Body.Bytes(), &errBody); err != nil {
		t.Fatalf("decode 429 JSON: %v", err)
	}
	if errBody["error_code"] != domain.ErrCodeRateLimited {
		t.Fatalf("error_code = %v, want %s", errBody["error_code"], domain.ErrCodeRateLimited)
	}

	// Order limit isolates buyers sharing the same IP until IP ceiling hits.
	routerA := setupRouter(buyerA, limiter)
	routerB := setupRouter(buyerB, limiter)
	for range 2 {
		if resp := doPost(routerA, "/orders", "10.0.0.9:1111", "Bearer tokA"); resp.Code >= 400 {
			t.Fatalf("buyerA expected success, got %d", resp.Code)
		}
	}
	if resp := doPost(routerA, "/orders", "10.0.0.9:1111", "Bearer tokA"); resp.Code != http.StatusTooManyRequests {
		t.Fatalf("buyerA 3rd order status = %d, want 429", resp.Code)
	}
	if resp := doPost(routerB, "/orders", "10.0.0.9:1111", "Bearer tokB"); resp.Code >= 400 {
		t.Fatalf("buyerB sharing IP should succeed, got %d", resp.Code)
	}

	// Fail-open when limiter returns an error.
	failingRouter := setupRouter(buyerA, &memoryLimiter{failErr: errors.New("redis down")})
	if resp := doPost(failingRouter, "/orders", "10.0.0.9:1111", "Bearer tokA"); resp.Code >= 400 {
		t.Fatalf("expected fail-open success, got %d", resp.Code)
	}
}
