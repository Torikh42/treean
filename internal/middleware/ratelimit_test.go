package middleware_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
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
	"github.com/torikh42/treean/internal/security"
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
				slog.New(slog.DiscardHandler),
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

type memoryNonceStore struct {
	mu      sync.Mutex
	used    map[string]struct{}
	failErr error
}

func (m *memoryNonceStore) ConsumeOnce(_ context.Context, key string, _ time.Duration) (bool, error) {
	if m.failErr != nil {
		return false, m.failErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.used == nil {
		m.used = make(map[string]struct{})
	}
	if _, exists := m.used[key]; exists {
		return false, nil
	}
	m.used[key] = struct{}{}
	return true, nil
}

func solveChallengeForTest(t *testing.T, challenge string, difficulty int) string {
	t.Helper()
	for nonce := 0; nonce < 5_000_000; nonce++ {
		nonceStr := strconv.Itoa(nonce)
		sum := sha256.Sum256([]byte(challenge + ":" + nonceStr))
		if hasLeadingZeroBitsForTest(sum[:], difficulty) {
			return challenge + ":" + nonceStr
		}
	}
	t.Fatalf("failed to solve challenge %q at difficulty %d", challenge, difficulty)
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

func TestHumaRateLimitMiddleware_OrderUserOnlyWhenIPMaxZero(t *testing.T) {
	t.Parallel()

	limiter := &memoryLimiter{}
	bearerSec := []map[string][]string{{"bearer": {}}}
	var currentActor uuid.UUID
	var mu sync.Mutex

	r := chi.NewRouter()
	r.Use(middleware.WithClientIP)
	cfg := huma.DefaultConfig("Order User-Only Test", "1.0.0")
	cfg.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		"bearer": {Type: "http", Scheme: "bearer", BearerFormat: "JWT"},
	}
	api := humachi.New(r, cfg)
	api.UseMiddleware(func(ctx huma.Context, next func(huma.Context)) {
		mu.Lock()
		uid := currentActor
		mu.Unlock()
		middleware.NewHumaAuthMiddleware(
			api,
			&stubAuthenticator{actor: domain.Actor{ID: uid, Role: domain.RoleBuyer}},
			slog.New(slog.DiscardHandler),
			false,
		)(ctx, next)
	})
	api.UseMiddleware(middleware.NewHumaRateLimitMiddleware(api, limiter, middleware.RateLimitPolicy{
		Enabled:      true,
		OrderUserMax: 20,
		OrderIPMax:   0,
	}))
	huma.Register(api, huma.Operation{OperationID: "create-order", Method: http.MethodPost, Path: "/orders", Security: bearerSec},
		func(context.Context, *struct{}) (*struct{}, error) { return nil, nil })
	huma.Register(api, huma.Operation{OperationID: "cancel-order", Method: http.MethodPost, Path: "/orders/cancel", Security: bearerSec},
		func(context.Context, *struct{}) (*struct{}, error) { return nil, nil })

	// 500 distinct buyers sharing CG-NAT IP 103.1.2.3 must all succeed when OrderIPMax == 0.
	for i := range 500 {
		mu.Lock()
		currentActor = uuid.New()
		mu.Unlock()

		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/orders", nil)
		req.RemoteAddr = "103.1.2.3:4000"
		req.Header.Set("Authorization", "Bearer tok")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code >= 400 {
			t.Fatalf("distinct buyer %d failed with status %d: %s", i+1, rec.Code, rec.Body.String())
		}
	}

	// Cancel-order from another distinct buyer on same IP also succeeds when OrderIPMax == 0.
	mu.Lock()
	currentActor = uuid.New()
	mu.Unlock()
	cancelReq := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/orders/cancel", nil)
	cancelReq.RemoteAddr = "103.1.2.3:4000"
	cancelReq.Header.Set("Authorization", "Bearer tok")
	cancelRec := httptest.NewRecorder()
	r.ServeHTTP(cancelRec, cancelReq)
	if cancelRec.Code >= 400 {
		t.Fatalf("distinct buyer cancel-order on shared IP failed with status %d", cancelRec.Code)
	}

	// Single buyer hitting 21st request on same IP is throttled by actor.ID.
	hotBuyer := uuid.New()
	mu.Lock()
	currentActor = hotBuyer
	mu.Unlock()
	for i := range 20 {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/orders", nil)
		req.RemoteAddr = "103.1.2.3:4000"
		req.Header.Set("Authorization", "Bearer tok")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code >= 400 {
			t.Fatalf("hot buyer call %d unexpected status %d", i+1, rec.Code)
		}
	}
	req21 := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/orders", nil)
	req21.RemoteAddr = "103.1.2.3:4000"
	req21.Header.Set("Authorization", "Bearer tok")
	rec21 := httptest.NewRecorder()
	r.ServeHTTP(rec21, req21)
	if rec21.Code != http.StatusTooManyRequests {
		t.Fatalf("21st call status = %d, want 429", rec21.Code)
	}
	if rec21.Header().Get("Retry-After") == "" {
		t.Fatal("expected Retry-After header on 429 response")
	}
	var errBody map[string]any
	if err := json.Unmarshal(rec21.Body.Bytes(), &errBody); err != nil {
		t.Fatalf("decode 429 JSON: %v", err)
	}
	if errBody["error_code"] != domain.ErrCodeRateLimited {
		t.Fatalf("error_code = %v, want %s", errBody["error_code"], domain.ErrCodeRateLimited)
	}
}

func TestHumaRateLimitMiddleware_RegisterAdaptivePoW(t *testing.T) {
	t.Parallel()

	const difficulty = 8
	pow, err := security.NewPoWVerifier("test-pow-secret", difficulty, 2*time.Minute)
	if err != nil {
		t.Fatalf("NewPoWVerifier: %v", err)
	}
	nonceStore := &memoryNonceStore{}
	limiter := &memoryLimiter{}

	r := chi.NewRouter()
	r.Use(middleware.WithClientIP)
	cfg := huma.DefaultConfig("Register PoW Test", "1.0.0")
	api := humachi.New(r, cfg)
	api.UseMiddleware(middleware.NewHumaRateLimitMiddleware(api, limiter, middleware.RateLimitPolicy{
		Enabled:         true,
		RegisterMax:     5,
		RegisterHardMax: 10,
		Challenge:       pow,
		NonceStore:      nonceStore,
	}))
	huma.Register(api, huma.Operation{
		OperationID:   "register-user",
		Method:        http.MethodPost,
		Path:          "/auth/register",
		DefaultStatus: http.StatusCreated,
	}, func(context.Context, *struct{}) (*struct{}, error) { return nil, nil })

	postReg := func(powSolution string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/auth/register", nil)
		req.RemoteAddr = "103.1.2.3:5000"
		if powSolution != "" {
			req.Header.Set("X-PoW-Solution", powSolution)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}

	// Requests 1..5 pass cleanly without PoW.
	for i := range 5 {
		rec := postReg("")
		if rec.Code != http.StatusCreated {
			t.Fatalf("request %d status = %d, want 201", i+1, rec.Code)
		}
	}

	// Request 6 without X-PoW-Solution returns 403 CHALLENGE_REQUIRED with X-PoW-Challenge.
	challengeRec := postReg("")
	if challengeRec.Code != http.StatusForbidden {
		t.Fatalf("request 6 status = %d, want 403 (body=%s)", challengeRec.Code, challengeRec.Body.String())
	}
	challenge := challengeRec.Header().Get("X-PoW-Challenge")
	if challenge == "" {
		t.Fatal("expected non-empty X-PoW-Challenge header on 403 response")
	}
	var challengeBody map[string]any
	if err := json.Unmarshal(challengeRec.Body.Bytes(), &challengeBody); err != nil {
		t.Fatalf("decode 403 JSON: %v", err)
	}
	if challengeBody["error_code"] != domain.ErrCodeChallengeRequired {
		t.Fatalf("error_code = %v, want %s", challengeBody["error_code"], domain.ErrCodeChallengeRequired)
	}

	// Solving challenge lets request succeed (hard count = 7).
	solved := solveChallengeForTest(t, challenge, difficulty)
	okRec := postReg(solved)
	if okRec.Code != http.StatusCreated {
		t.Fatalf("solved retry status = %d, want 201 (body=%s)", okRec.Code, okRec.Body.String())
	}

	// Replaying same X-PoW-Solution fails with 403 CHALLENGE_REQUIRED and fresh challenge (hard count = 8).
	replayRec := postReg(solved)
	if replayRec.Code != http.StatusForbidden {
		t.Fatalf("replay status = %d, want 403", replayRec.Code)
	}
	freshChallenge := replayRec.Header().Get("X-PoW-Challenge")
	if freshChallenge == "" || freshChallenge == challenge {
		t.Fatalf("expected fresh X-PoW-Challenge on replay, got %q", freshChallenge)
	}
	var replayBody map[string]any
	if err := json.Unmarshal(replayRec.Body.Bytes(), &replayBody); err != nil {
		t.Fatalf("decode replay 403 JSON: %v", err)
	}
	if replayBody["error_code"] != domain.ErrCodeChallengeRequired {
		t.Fatalf("replay error_code = %v, want %s", replayBody["error_code"], domain.ErrCodeChallengeRequired)
	}

	// Use remaining hard budget (9, 10) with fresh solved challenges, then 11th hits 429 even with valid PoW.
	for i := 9; i <= 10; i++ {
		c, err := pow.Issue("103.1.2.3", time.Now().UTC())
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		sol := solveChallengeForTest(t, c, difficulty)
		rec := postReg(sol)
		if rec.Code != http.StatusCreated {
			t.Fatalf("request %d with valid PoW status = %d, want 201", i, rec.Code)
		}
	}

	hardChallenge, err := pow.Issue("103.1.2.3", time.Now().UTC())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	hardRec := postReg(solveChallengeForTest(t, hardChallenge, difficulty))
	if hardRec.Code != http.StatusTooManyRequests {
		t.Fatalf("request 11 above hard max status = %d, want 429", hardRec.Code)
	}
	if hardRec.Header().Get("Retry-After") == "" {
		t.Fatal("expected Retry-After header on 429 hard cap response")
	}
	var hardBody map[string]any
	if err := json.Unmarshal(hardRec.Body.Bytes(), &hardBody); err != nil {
		t.Fatalf("decode 429 JSON: %v", err)
	}
	if hardBody["error_code"] != domain.ErrCodeRateLimited {
		t.Fatalf("hard error_code = %v, want %s", hardBody["error_code"], domain.ErrCodeRateLimited)
	}
}

func TestHumaRateLimitMiddleware_LoginAdaptivePoW(t *testing.T) {
	t.Parallel()

	const difficulty = 8
	pow, err := security.NewPoWVerifier("test-pow-secret", difficulty, 2*time.Minute)
	if err != nil {
		t.Fatalf("NewPoWVerifier: %v", err)
	}
	nonceStore := &memoryNonceStore{}
	limiter := &memoryLimiter{}

	r := chi.NewRouter()
	r.Use(middleware.WithClientIP)
	cfg := huma.DefaultConfig("Login PoW Test", "1.0.0")
	api := humachi.New(r, cfg)
	api.UseMiddleware(middleware.NewHumaRateLimitMiddleware(api, limiter, middleware.RateLimitPolicy{
		Enabled:        true,
		LoginIPMax:     3,
		LoginIPHardMax: 6,
		Challenge:      pow,
		NonceStore:     nonceStore,
	}))
	huma.Register(api, huma.Operation{
		OperationID: "login-user",
		Method:      http.MethodPost,
		Path:        "/auth/login",
	}, func(context.Context, *struct{}) (*struct{}, error) { return nil, nil })

	postLogin := func(powSolution string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/auth/login", nil)
		req.RemoteAddr = "103.1.2.9:6000"
		if powSolution != "" {
			req.Header.Set("X-PoW-Solution", powSolution)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}

	// Requests 1..3 pass cleanly below soft limit.
	for i := range 3 {
		if rec := postLogin(""); rec.Code >= 400 {
			t.Fatalf("login %d unexpected status %d", i+1, rec.Code)
		}
	}

	// Request 4 without PoW gets 403 CHALLENGE_REQUIRED.
	rec4 := postLogin("")
	if rec4.Code != http.StatusForbidden {
		t.Fatalf("login 4 without PoW status = %d, want 403", rec4.Code)
	}
	ch := rec4.Header().Get("X-PoW-Challenge")
	if ch == "" {
		t.Fatal("expected X-PoW-Challenge header on login 403")
	}

	// Solve PoW for request 5 and 6 -> passes.
	if rec5 := postLogin(solveChallengeForTest(t, ch, difficulty)); rec5.Code >= 400 {
		t.Fatalf("login 5 with solved PoW status = %d, want <400", rec5.Code)
	}
	ch2, err := pow.Issue("103.1.2.9", time.Now().UTC())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if rec6 := postLogin(solveChallengeForTest(t, ch2, difficulty)); rec6.Code >= 400 {
		t.Fatalf("login 6 with solved PoW status = %d, want <400", rec6.Code)
	}

	// Request 7 exceeds LoginIPHardMax (6) -> 429 even with valid PoW.
	ch3, err := pow.Issue("103.1.2.9", time.Now().UTC())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	rec7 := postLogin(solveChallengeForTest(t, ch3, difficulty))
	if rec7.Code != http.StatusTooManyRequests {
		t.Fatalf("login 7 above hard max status = %d, want 429", rec7.Code)
	}
	if rec7.Header().Get("Retry-After") == "" {
		t.Fatal("expected Retry-After header on login 429")
	}
}

type failingChallengeGuard struct {
	issueErr error
}

func (f failingChallengeGuard) Issue(string, time.Time) (string, error) {
	return "", f.issueErr
}

func (f failingChallengeGuard) Verify(string, string, time.Time) (string, error) {
	return "", errors.New("verify failed")
}

func TestHumaRateLimitMiddleware_PoWFailOpenNonceAndFailClosedIssue(t *testing.T) {
	t.Parallel()

	// 1. NonceStore error fails open once PoW signature & leading-zero check pass.
	const difficulty = 8
	pow, err := security.NewPoWVerifier("test-pow-secret", difficulty, 2*time.Minute)
	if err != nil {
		t.Fatalf("NewPoWVerifier: %v", err)
	}
	r := chi.NewRouter()
	r.Use(middleware.WithClientIP)
	api := humachi.New(r, huma.DefaultConfig("PoW Edge Test", "1.0.0"))
	api.UseMiddleware(middleware.NewHumaRateLimitMiddleware(api, &memoryLimiter{}, middleware.RateLimitPolicy{
		Enabled:         true,
		RegisterMax:     1,
		RegisterHardMax: 10,
		Challenge:       pow,
		NonceStore:      &memoryNonceStore{failErr: errors.New("redis timeout")},
	}))
	huma.Register(api, huma.Operation{OperationID: "register-user", Method: http.MethodPost, Path: "/auth/register", DefaultStatus: http.StatusCreated},
		func(context.Context, *struct{}) (*struct{}, error) { return nil, nil })

	first := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/auth/register", nil)
	first.RemoteAddr = "103.1.2.4:1000"
	firstRec := httptest.NewRecorder()
	r.ServeHTTP(firstRec, first)
	if firstRec.Code != http.StatusCreated {
		t.Fatalf("first status = %d, want 201", firstRec.Code)
	}

	ch, err := pow.Issue("103.1.2.4", time.Now().UTC())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	second := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/auth/register", nil)
	second.RemoteAddr = "103.1.2.4:1000"
	second.Header.Set("X-PoW-Solution", solveChallengeForTest(t, ch, difficulty))
	secondRec := httptest.NewRecorder()
	r.ServeHTTP(secondRec, second)
	if secondRec.Code != http.StatusCreated {
		t.Fatalf("expected fail-open when NonceStore errors, got status %d (%s)", secondRec.Code, secondRec.Body.String())
	}

	// 2. Challenge.Issue failure fails closed with 403 CHALLENGE_REQUIRED and no X-PoW-Challenge header.
	r2 := chi.NewRouter()
	r2.Use(middleware.WithClientIP)
	api2 := humachi.New(r2, huma.DefaultConfig("PoW Issue Fail Test", "1.0.0"))
	api2.UseMiddleware(middleware.NewHumaRateLimitMiddleware(api2, &memoryLimiter{}, middleware.RateLimitPolicy{
		Enabled:        true,
		LoginIPMax:     1,
		LoginIPHardMax: 10,
		Challenge:      failingChallengeGuard{issueErr: errors.New("issue failure")},
		NonceStore:     &memoryNonceStore{},
	}))
	huma.Register(api2, huma.Operation{OperationID: "login-user", Method: http.MethodPost, Path: "/auth/login"},
		func(context.Context, *struct{}) (*struct{}, error) { return nil, nil })

	// 1st passes soft limit, 2nd triggers Issue failure -> 403 without X-PoW-Challenge.
	req1 := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/auth/login", nil)
	req1.RemoteAddr = "103.1.2.5:1000"
	r2.ServeHTTP(httptest.NewRecorder(), req1)

	req2 := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/auth/login", nil)
	req2.RemoteAddr = "103.1.2.5:1000"
	rec2 := httptest.NewRecorder()
	r2.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("issue failure status = %d, want 403", rec2.Code)
	}
	if rec2.Header().Get("X-PoW-Challenge") != "" {
		t.Fatalf("expected empty X-PoW-Challenge when Issue fails, got %q", rec2.Header().Get("X-PoW-Challenge"))
	}
	var errBody map[string]any
	if err := json.Unmarshal(rec2.Body.Bytes(), &errBody); err != nil {
		t.Fatalf("decode 403 JSON: %v", err)
	}
	if errBody["error_code"] != domain.ErrCodeChallengeRequired {
		t.Fatalf("error_code = %v, want %s", errBody["error_code"], domain.ErrCodeChallengeRequired)
	}
}
