package middleware_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/humatest"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/torikh42/treean/internal/domain"
	"github.com/torikh42/treean/internal/middleware"
	"github.com/torikh42/treean/internal/service"
)

var _ middleware.Authenticator = (*service.AuthService)(nil)

type stubAuthenticator struct {
	actor      domain.Actor
	parseErr   error
	ensureErr  error
	lastToken  string
	ensureID   uuid.UUID
	ensureRole domain.Role
	ensureHits int
}

func (s *stubAuthenticator) ParseJWT(tokenStr string) (domain.Actor, error) {
	s.lastToken = tokenStr
	if s.parseErr != nil {
		return domain.Actor{}, s.parseErr
	}
	return s.actor, nil
}

func (s *stubAuthenticator) EnsureDemoUser(_ context.Context, id uuid.UUID, role domain.Role) error {
	s.ensureHits++
	s.ensureID = id
	s.ensureRole = role
	return s.ensureErr
}

type logEntry struct {
	Level     string `json:"level"`
	Msg       string `json:"msg"`
	Method    string `json:"method"`
	Path      string `json:"path"`
	Status    int    `json:"status"`
	Duration  *int64 `json:"duration_ms"`
	RequestID string `json:"request_id"`
	Panic     string `json:"panic"`
	Stack     string `json:"stack"`
}

func decodeSingleLog(t *testing.T, raw []byte) logEntry {
	t.Helper()
	var entry logEntry
	if err := json.Unmarshal(bytes.TrimSpace(raw), &entry); err != nil {
		t.Fatalf("failed to decode log JSON %q: %v", string(raw), err)
	}
	return entry
}

func newAuthTestAPI(t *testing.T) huma.API {
	t.Helper()
	_, api := humatest.New(t, huma.DefaultConfig("Test Auth API", "1.0.0"))
	return api
}

func TestActorFromContext(t *testing.T) {
	t.Parallel()

	var nilCtx context.Context
	if actor, ok := middleware.ActorFromContext(nilCtx); ok || actor.ID != uuid.Nil {
		t.Fatalf("expected (zero, false) for nil context, got (%+v, %v)", actor, ok)
	}

	if actor, ok := middleware.ActorFromContext(t.Context()); ok || actor.ID != uuid.Nil {
		t.Fatalf("expected (zero, false) for plain context, got (%+v, %v)", actor, ok)
	}
}

func TestRecoverer(t *testing.T) {
	t.Parallel()

	t.Run("recovers panic before write and returns JSON 500", func(t *testing.T) {
		t.Parallel()

		var buf bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&buf, nil))
		h := middleware.Recoverer(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic("unexpected crash")
		}))

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/boom", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
			t.Fatalf("Content-Type = %q, want application/json", ct)
		}

		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("invalid JSON response %q: %v", rec.Body.String(), err)
		}
		if body["error_code"] != "INTERNAL_ERROR" || body["detail"] != "internal server error" {
			t.Fatalf("unexpected error payload: %+v", body)
		}

		entry := decodeSingleLog(t, buf.Bytes())
		if entry.Level != "ERROR" || entry.Method != http.MethodGet || entry.Path != "/boom" {
			t.Fatalf("unexpected panic log entry: %+v", entry)
		}
		if entry.Panic == "" || entry.Stack == "" {
			t.Fatalf("expected panic and stack fields in log entry: %+v", entry)
		}
	})

	t.Run("does not overwrite already committed response on late panic", func(t *testing.T) {
		t.Parallel()

		var buf bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&buf, nil))
		h := middleware.Recoverer(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte("partial"))
			panic("late panic")
		}))

		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/partial", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201", rec.Code)
		}
		if rec.Body.String() != "partial" {
			t.Fatalf("body = %q, want %q", rec.Body.String(), "partial")
		}
	})

	t.Run("handles nil logger safely", func(t *testing.T) {
		t.Parallel()

		h := middleware.Recoverer(nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic("nil logger panic")
		}))

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/nil-logger", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", rec.Code)
		}
	})
}

func TestRecoverer_RequestIDAndOuterAccessLog(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	chain := chiMiddleware.RequestID(
		middleware.RequestLogger(logger)(
			middleware.Recoverer(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				panic("boom with request id")
			})),
		),
	)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/panic-trace", nil)
	req.Header.Set(chiMiddleware.RequestIDHeader, "trace-999")
	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 JSON log lines (panic + access), got %d: %s", len(lines), buf.String())
	}
	panicEntry := decodeSingleLog(t, []byte(lines[0]))
	accessEntry := decodeSingleLog(t, []byte(lines[1]))
	if panicEntry.RequestID != "trace-999" || panicEntry.Level != "ERROR" {
		t.Fatalf("unexpected panic log entry: %+v", panicEntry)
	}
	if accessEntry.Msg != "http request" || accessEntry.Status != http.StatusInternalServerError || accessEntry.RequestID != "trace-999" {
		t.Fatalf("unexpected access log entry: %+v", accessEntry)
	}
}

func TestRequestLogger(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	handler := chiMiddleware.RequestID(
		middleware.RequestLogger(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte("ok"))
		})),
	)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/orders", nil)
	req.Header.Set("Authorization", "Bearer super-secret-jwt-token")
	req.Header.Set(chiMiddleware.RequestIDHeader, "req-123")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	rawLog := buf.String()
	if strings.Contains(rawLog, "super-secret-jwt-token") || strings.Contains(rawLog, "Authorization") {
		t.Fatalf("request log must never leak auth credentials: %s", rawLog)
	}

	entry := decodeSingleLog(t, buf.Bytes())
	if entry.Method != http.MethodPost || entry.Path != "/api/v1/orders" || entry.Status != http.StatusAccepted {
		t.Fatalf("unexpected log entry: %+v", entry)
	}
	if entry.Duration == nil || *entry.Duration < 0 {
		t.Fatalf("expected non-negative duration_ms, got %v", entry.Duration)
	}
	if entry.RequestID != "req-123" {
		t.Fatalf("request_id = %q, want %q", entry.RequestID, "req-123")
	}

	var defaultBuf bytes.Buffer
	defaultStatusLogger := slog.New(slog.NewJSONHandler(&defaultBuf, nil))
	defaultHandler := middleware.RequestLogger(defaultStatusLogger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	rec2 := httptest.NewRecorder()
	defaultHandler.ServeHTTP(rec2, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/implicit-ok", nil))
	if got := decodeSingleLog(t, defaultBuf.Bytes()).Status; got != http.StatusOK {
		t.Fatalf("default logged status = %d, want 200", got)
	}
}

func TestRequestLogger_SkipsHealthyProbeOnly(t *testing.T) {
	t.Parallel()

	var okBuf bytes.Buffer
	okLogger := slog.New(slog.NewJSONHandler(&okBuf, nil))
	okHandler := middleware.RequestLogger(okLogger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	okRec := httptest.NewRecorder()
	okHandler.ServeHTTP(okRec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health", nil))
	if okBuf.Len() != 0 {
		t.Fatalf("expected /health 200 to be omitted from request logs, got %s", okBuf.String())
	}

	var errBuf bytes.Buffer
	errLogger := slog.New(slog.NewJSONHandler(&errBuf, nil))
	errHandler := middleware.RequestLogger(errLogger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	errRec := httptest.NewRecorder()
	errHandler.ServeHTTP(errRec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health", nil))
	if got := decodeSingleLog(t, errBuf.Bytes()).Status; got != http.StatusServiceUnavailable {
		t.Fatalf("unhealthy /health logged status = %d, want 503", got)
	}
}

func TestMaxBodyBytes(t *testing.T) {
	t.Parallel()

	makeHandler := func(limit int64) http.Handler {
		return middleware.MaxBodyBytes(limit)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, err := io.ReadAll(r.Body)
			if err != nil {
				if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
					http.Error(w, "too large", http.StatusRequestEntityTooLarge)
					return
				}
				http.Error(w, "read error", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
	}

	mw := makeHandler(8)
	smallReq := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/limit", strings.NewReader("12345678"))
	smallRec := httptest.NewRecorder()
	mw.ServeHTTP(smallRec, smallReq)
	if smallRec.Code != http.StatusOK {
		t.Fatalf("small body status = %d, want 200", smallRec.Code)
	}

	largeReq := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/limit", strings.NewReader("123456789"))
	largeRec := httptest.NewRecorder()
	mw.ServeHTTP(largeRec, largeReq)
	if largeRec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("large body status = %d, want 413", largeRec.Code)
	}

	unbounded := makeHandler(0)
	unboundedRec := httptest.NewRecorder()
	unbounded.ServeHTTP(unboundedRec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/limit", strings.NewReader("1234567890")))
	if unboundedRec.Code != http.StatusOK {
		t.Fatalf("non-positive limit status = %d, want 200", unboundedRec.Code)
	}
}

func TestHumaAuthMiddleware_PublicAndNilAuth(t *testing.T) {
	t.Parallel()

	api := newAuthTestAPI(t)

	t.Run("skips auth when operation has no security", func(t *testing.T) {
		t.Parallel()

		mw := middleware.NewHumaAuthMiddleware(api, nil, nil, false)
		op := &huma.Operation{Method: http.MethodGet, Path: "/public"}
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/public", nil)
		rec := httptest.NewRecorder()
		called := false

		mw(humatest.NewContext(op, req, rec), func(huma.Context) {
			called = true
		})

		if !called {
			t.Fatal("expected public route to invoke next handler")
		}
	})

	t.Run("rejects protected route when auth verifier is nil", func(t *testing.T) {
		t.Parallel()

		mw := middleware.NewHumaAuthMiddleware(api, nil, nil, false)
		op := &huma.Operation{
			Method:   http.MethodPost,
			Path:     "/protected",
			Security: []map[string][]string{{"bearer": {}}},
		}
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/protected", nil)
		rec := httptest.NewRecorder()
		called := false

		mw(humatest.NewContext(op, req, rec), func(huma.Context) {
			called = true
		})

		if called || rec.Code != http.StatusUnauthorized {
			t.Fatalf("called=%v status=%d, want false and 401", called, rec.Code)
		}
	})

	t.Run("handles nil api fallback when writing error", func(t *testing.T) {
		t.Parallel()

		mw := middleware.NewHumaAuthMiddleware(nil, nil, nil, false)
		op := &huma.Operation{
			Method:   http.MethodPost,
			Path:     "/protected",
			Security: []map[string][]string{{"bearer": {}}},
		}
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/protected", nil)
		rec := httptest.NewRecorder()

		mw(humatest.NewContext(op, req, rec), func(huma.Context) {
			t.Fatal("next should not be called")
		})

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
		if !strings.Contains(rec.Header().Get("Content-Type"), "application/json") {
			t.Fatalf("Content-Type = %q, want application/json", rec.Header().Get("Content-Type"))
		}
	})
}

func TestHumaAuthMiddleware_BearerTokens(t *testing.T) {
	t.Parallel()

	api := newAuthTestAPI(t)
	expectedActor := domain.Actor{
		ID:    uuid.New(),
		Email: "org@event.local",
		Role:  domain.RoleOrganizer,
	}

	cases := []struct {
		name       string
		header     string
		parseErr   error
		wantStatus int
		wantCalled bool
		wantToken  string
	}{
		{
			name:       "valid standard Bearer prefix",
			header:     "Bearer valid-jwt",
			wantStatus: http.StatusOK,
			wantCalled: true,
			wantToken:  "valid-jwt",
		},
		{
			name:       "case-insensitive bearer prefix",
			header:     "bearer   lower-jwt ",
			wantStatus: http.StatusOK,
			wantCalled: true,
			wantToken:  "lower-jwt",
		},
		{
			name:       "blank bearer token rejected",
			header:     "Bearer   ",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "bare bearer keyword rejected",
			header:     "Bearer",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "invalid token rejected",
			header:     "Bearer expired-jwt",
			parseErr:   domain.NewDomainError(http.StatusUnauthorized, domain.ErrCodeUnauthorized, "bad token"),
			wantStatus: http.StatusUnauthorized,
			wantToken:  "expired-jwt",
		},
		{
			name:       "missing credentials rejected",
			header:     "",
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			stub := &stubAuthenticator{actor: expectedActor, parseErr: tc.parseErr}
			mw := middleware.NewHumaAuthMiddleware(api, stub, nil, false)
			op := &huma.Operation{
				Method:   http.MethodPost,
				Path:     "/api/v1/events",
				Security: []map[string][]string{{"bearer": {}}},
			}
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/events", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			called := false
			var gotActor domain.Actor

			mw(humatest.NewContext(op, req, rec), func(nextCtx huma.Context) {
				called = true
				gotActor, _ = middleware.ActorFromContext(nextCtx.Context())
				nextCtx.SetStatus(http.StatusOK)
			})

			if called != tc.wantCalled || rec.Code != tc.wantStatus {
				t.Fatalf("called=%v status=%d, want called=%v status=%d (body=%s)", called, rec.Code, tc.wantCalled, tc.wantStatus, rec.Body.String())
			}
			if tc.wantToken != "" && stub.lastToken != tc.wantToken {
				t.Fatalf("parsed token = %q, want %q", stub.lastToken, tc.wantToken)
			}
			if tc.wantCalled && gotActor != expectedActor {
				t.Fatalf("actor = %+v, want %+v", gotActor, expectedActor)
			}
		})
	}
}

func TestHumaAuthMiddleware_DemoHeaders(t *testing.T) {
	t.Parallel()

	api := newAuthTestAPI(t)
	validID := uuid.New()

	cases := []struct {
		name       string
		allowDemo  bool
		userID     string
		userRole   string
		ensureErr  error
		wantStatus int
		wantCalled bool
		wantRole   domain.Role
	}{
		{
			name:       "demo headers rejected when disabled",
			allowDemo:  false,
			userID:     validID.String(),
			userRole:   "BUYER",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "valid buyer demo headers succeed when enabled",
			allowDemo:  true,
			userID:     validID.String(),
			userRole:   "buyer",
			wantStatus: http.StatusOK,
			wantCalled: true,
			wantRole:   domain.RoleBuyer,
		},
		{
			name:       "valid organizer demo headers succeed when enabled",
			allowDemo:  true,
			userID:     validID.String(),
			userRole:   "ORGANIZER",
			wantStatus: http.StatusOK,
			wantCalled: true,
			wantRole:   domain.RoleOrganizer,
		},
		{
			name:       "nil UUID rejected",
			allowDemo:  true,
			userID:     uuid.Nil.String(),
			userRole:   "BUYER",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "malformed UUID rejected",
			allowDemo:  true,
			userID:     "not-a-uuid",
			userRole:   "BUYER",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "invalid role rejected",
			allowDemo:  true,
			userID:     validID.String(),
			userRole:   "ADMIN",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "missing role rejected",
			allowDemo:  true,
			userID:     validID.String(),
			userRole:   "",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "repo error returns 500",
			allowDemo:  true,
			userID:     validID.String(),
			userRole:   "BUYER",
			ensureErr:  errors.New("db unavailable"),
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var logBuf bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
			stub := &stubAuthenticator{ensureErr: tc.ensureErr}
			mw := middleware.NewHumaAuthMiddleware(api, stub, logger, tc.allowDemo)
			op := &huma.Operation{
				Method:   http.MethodPost,
				Path:     "/api/v1/orders",
				Security: []map[string][]string{{"bearer": {}}},
			}
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/orders", nil)
			if tc.userID != "" {
				req.Header.Set("X-User-ID", tc.userID)
			}
			if tc.userRole != "" {
				req.Header.Set("X-User-Role", tc.userRole)
			}
			rec := httptest.NewRecorder()
			called := false
			var gotActor domain.Actor

			mw(humatest.NewContext(op, req, rec), func(nextCtx huma.Context) {
				called = true
				gotActor, _ = middleware.ActorFromContext(nextCtx.Context())
				nextCtx.SetStatus(http.StatusOK)
			})

			if called != tc.wantCalled || rec.Code != tc.wantStatus {
				t.Fatalf("called=%v status=%d, want called=%v status=%d (body=%s)", called, rec.Code, tc.wantCalled, tc.wantStatus, rec.Body.String())
			}
			assertDemoRepoErrorLog(t, tc.ensureErr, rec.Body.String(), logBuf.String())
			if tc.wantCalled {
				if gotActor.ID != validID || gotActor.Role != tc.wantRole || gotActor.Email != "demo@event.local" {
					t.Fatalf("unexpected demo actor: %+v", gotActor)
				}
				if stub.ensureHits != 1 || stub.ensureID != validID || stub.ensureRole != tc.wantRole {
					t.Fatalf("unexpected EnsureDemoUser call: hits=%d id=%s role=%s", stub.ensureHits, stub.ensureID, stub.ensureRole)
				}
			}
		})
	}
}

func assertDemoRepoErrorLog(t *testing.T, ensureErr error, body, logs string) {
	t.Helper()
	if ensureErr == nil {
		return
	}
	if strings.Contains(body, ensureErr.Error()) {
		t.Fatalf("HTTP 500 body must not leak internal error: %s", body)
	}
	if !strings.Contains(logs, `"ERROR"`) || !strings.Contains(logs, ensureErr.Error()) {
		t.Fatalf("expected server ERROR log with EnsureDemoUser failure, got: %s", logs)
	}
}

func TestCORS_AllowsTrimmedOriginOnGET(t *testing.T) {
	t.Parallel()

	called := false
	h := middleware.CORS([]string{" http://localhost:5173 ", "", "http://localhost:3000"})(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			called = true
			w.WriteHeader(http.StatusOK)
		}),
	)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/events", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !called || rec.Code != http.StatusOK {
		t.Fatalf("called=%v status=%d, want true and 200", called, rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, "http://localhost:5173")
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Fatalf("Access-Control-Allow-Credentials = %q, want empty", got)
	}
	wantVary := []string{"Origin", "Access-Control-Request-Method", "Access-Control-Request-Headers"}
	if got := rec.Header().Values("Vary"); !slices.Equal(got, wantVary) {
		t.Fatalf("Vary = %v, want %v", got, wantVary)
	}
}

func TestCORS_HandlesOptionsPreflight(t *testing.T) {
	t.Parallel()

	for _, reqMethod := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		t.Run(reqMethod, func(t *testing.T) {
			t.Parallel()

			called := false
			h := middleware.CORS([]string{"http://localhost:5173"})(
				http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
					called = true
				}),
			)

			req := httptest.NewRequestWithContext(t.Context(), http.MethodOptions, "/api/v1/events/123", nil)
			req.Header.Set("Origin", "http://localhost:5173")
			req.Header.Set("Access-Control-Request-Method", reqMethod)
			req.Header.Set("Access-Control-Request-Headers", "Content-Type, Authorization")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if called {
				t.Fatal("expected OPTIONS preflight not to invoke next handler")
			}
			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want 204", rec.Code)
			}
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
				t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, "http://localhost:5173")
			}
			if got := rec.Header().Get("Access-Control-Allow-Methods"); got != "GET, POST, PUT, DELETE, OPTIONS" {
				t.Fatalf("Access-Control-Allow-Methods = %q, want %q", got, "GET, POST, PUT, DELETE, OPTIONS")
			}
			if got := rec.Header().Get("Access-Control-Allow-Headers"); got != "Content-Type, Authorization, X-User-ID, X-User-Role, Idempotency-Key, X-PoW-Solution" {
				t.Fatalf("Access-Control-Allow-Headers = %q, want %q", got, "Content-Type, Authorization, X-User-ID, X-User-Role, Idempotency-Key, X-PoW-Solution")
			}
			if got := rec.Header().Get("Access-Control-Expose-Headers"); got != "Idempotent-Replay, Retry-After, X-PoW-Challenge" {
				t.Fatalf("Access-Control-Expose-Headers = %q, want %q", got, "Idempotent-Replay, Retry-After, X-PoW-Challenge")
			}
			if got := rec.Header().Get("Access-Control-Max-Age"); got != "600" {
				t.Fatalf("Access-Control-Max-Age = %q, want 600", got)
			}
			if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
				t.Fatalf("Access-Control-Allow-Credentials = %q, want empty", got)
			}
		})
	}
}

func TestCORS_SupportsWildcardOrigin(t *testing.T) {
	t.Parallel()

	h := middleware.CORS([]string{"*"})(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health", nil)
	req.Header.Set("Origin", "https://any.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want *", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Fatalf("Access-Control-Allow-Credentials = %q, want empty", got)
	}
}

func TestCORS_RejectsDisallowedOrigin(t *testing.T) {
	t.Parallel()

	called := false
	h := middleware.CORS([]string{"http://localhost:5173"})(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			called = true
			w.WriteHeader(http.StatusOK)
		}),
	)

	getReq := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health", nil)
	getReq.Header.Set("Origin", "https://evil.example.com")
	getRec := httptest.NewRecorder()
	h.ServeHTTP(getRec, getReq)
	if !called || getRec.Code != http.StatusOK {
		t.Fatalf("GET disallowed origin: called=%v status=%d, want true and 200", called, getRec.Code)
	}
	if got := getRec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("expected empty ACAO for disallowed GET origin, got %q", got)
	}

	called = false
	optReq := httptest.NewRequestWithContext(t.Context(), http.MethodOptions, "/api/v1/orders", nil)
	optReq.Header.Set("Origin", "https://evil.example.com")
	optRec := httptest.NewRecorder()
	h.ServeHTTP(optRec, optReq)
	if called {
		t.Fatal("expected disallowed OPTIONS preflight not to invoke next")
	}
	if optRec.Code != http.StatusNoContent {
		t.Fatalf("OPTIONS disallowed origin status = %d, want 204", optRec.Code)
	}
	if got := optRec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("expected empty ACAO for disallowed OPTIONS origin, got %q", got)
	}
}
