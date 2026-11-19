package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/torikh42/treean/internal/config"
)

func doTestRequest(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), method, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestSafeLogger_HandlesNilAndPreservesProvided(t *testing.T) {
	t.Parallel()

	if safeLogger(nil) == nil {
		t.Fatal("safeLogger(nil) returned nil")
	}

	provided := slog.New(slog.NewTextHandler(io.Discard, nil))
	if safeLogger(provided) != provided {
		t.Fatal("safeLogger did not preserve non-nil logger")
	}
}

func TestBuildServer_ConfiguresTimeoutsAndAddr(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	cfg := config.Config{
		Port:             "9090",
		HTTPReadTimeout:  12 * time.Second,
		HTTPWriteTimeout: 25 * time.Second,
	}

	srv := buildServer(cfg, mux)
	if srv == nil {
		t.Fatal("buildServer returned nil")
	}
	if srv.Addr != ":9090" {
		t.Fatalf("srv.Addr = %q, want %q", srv.Addr, ":9090")
	}
	if srv.Handler != mux {
		t.Fatal("srv.Handler does not match provided handler")
	}
	if srv.ReadHeaderTimeout != 5*time.Second {
		t.Fatalf("srv.ReadHeaderTimeout = %v, want %v", srv.ReadHeaderTimeout, 5*time.Second)
	}
	if srv.ReadTimeout != 12*time.Second {
		t.Fatalf("srv.ReadTimeout = %v, want %v", srv.ReadTimeout, 12*time.Second)
	}
	if srv.WriteTimeout != 25*time.Second {
		t.Fatalf("srv.WriteTimeout = %v, want %v", srv.WriteTimeout, 25*time.Second)
	}
	if srv.IdleTimeout != 60*time.Second {
		t.Fatalf("srv.IdleTimeout = %v, want %v", srv.IdleTimeout, 60*time.Second)
	}
}

func TestBuildHandler_WiresRoutesHealth503(t *testing.T) {
	t.Parallel()

	cfg := config.Config{
		JWTSecret:           "test-jwt-secret",
		WebhookSecret:       "test-webhook-secret",
		AllowDemoHeaderAuth: true,
		CORSAllowedOrigins:  []string{"https://custom-ui.example.com"},
	}

	h := buildHandler(nil, cfg, nil)
	if h == nil {
		t.Fatal("buildHandler returned nil")
	}

	corsReq := httptest.NewRequestWithContext(t.Context(), http.MethodOptions, "/api/v1/orders", nil)
	corsReq.Header.Set("Origin", "https://custom-ui.example.com")
	corsRec := httptest.NewRecorder()
	h.ServeHTTP(corsRec, corsReq)
	if corsRec.Code != http.StatusNoContent {
		t.Fatalf("OPTIONS /api/v1/orders status = %d, want %d", corsRec.Code, http.StatusNoContent)
	}
	if got := corsRec.Header().Get("Access-Control-Allow-Origin"); got != "https://custom-ui.example.com" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, "https://custom-ui.example.com")
	}

	healthRec := doTestRequest(t, h, http.MethodGet, "/health")
	if healthRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /health status = %d, want %d", healthRec.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(healthRec.Body.String(), `"unhealthy"`) {
		t.Fatalf("GET /health body = %q, want unhealthy payload", healthRec.Body.String())
	}

	openAPIRec := doTestRequest(t, h, http.MethodGet, "/openapi.json")
	if openAPIRec.Code != http.StatusOK {
		t.Fatalf("GET /openapi.json status = %d, want %d", openAPIRec.Code, http.StatusOK)
	}
	var spec map[string]any
	if err := json.Unmarshal(openAPIRec.Body.Bytes(), &spec); err != nil {
		t.Fatalf("invalid openapi.json payload: %v", err)
	}
	if _, ok := spec["paths"]; !ok {
		t.Fatal("openapi.json missing paths field")
	}

	demoRec := doTestRequest(t, h, http.MethodGet, "/demo")
	if demoRec.Code != http.StatusOK {
		t.Fatalf("GET /demo status = %d, want %d", demoRec.Code, http.StatusOK)
	}
	if ct := demoRec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("GET /demo Content-Type = %q, want text/html", ct)
	}
}

func TestServeWithGracefulShutdown_ContextCancel(t *testing.T) {
	t.Parallel()

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen ephemeral port: %v", err)
	}
	defer func() { _ = ln.Close() }()

	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	srv := buildServer(config.Config{
		Port:             "0",
		HTTPReadTimeout:  5 * time.Second,
		HTTPWriteTimeout: 5 * time.Second,
	}, h)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	errCh := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		errCh <- serveWithGracefulShutdown(ctx, slog.New(slog.DiscardHandler), srv, ln)
	}()

	client := &http.Client{Timeout: 2 * time.Second}
	defer client.CloseIdleConnections()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+ln.Addr().String()+"/ping", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("execute request: %v", err)
	}
	body, readErr := io.ReadAll(resp.Body)
	closeErr := resp.Body.Close()
	if readErr != nil {
		t.Fatalf("read response body: %v", readErr)
	}
	if closeErr != nil {
		t.Fatalf("close response body: %v", closeErr)
	}
	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("GET /ping = %d %q, want 200 ok", resp.StatusCode, string(body))
	}

	client.CloseIdleConnections()
	cancel()

	select {
	case serveErr := <-errCh:
		if serveErr != nil {
			t.Fatalf("serveWithGracefulShutdown returned error on cancel: %v", serveErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for graceful shutdown")
	}

	wg.Wait()
}

func TestServeWithGracefulShutdown_ListenerError(t *testing.T) {
	t.Parallel()

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen ephemeral port: %v", err)
	}
	if closeErr := ln.Close(); closeErr != nil {
		t.Fatalf("close listener: %v", closeErr)
	}

	srv := buildServer(config.Config{
		Port:             "0",
		HTTPReadTimeout:  5 * time.Second,
		HTTPWriteTimeout: 5 * time.Second,
	}, http.NewServeMux())

	serveErr := serveWithGracefulShutdown(t.Context(), nil, srv, ln)
	if serveErr == nil {
		t.Fatal("expected error for closed listener, got nil")
	}
}
