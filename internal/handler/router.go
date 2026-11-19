package handler

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/torikh42/treean/internal/middleware"
	"github.com/torikh42/treean/internal/service"
	"github.com/torikh42/treean/web"
)

const (
	maxRequestBodyBytes   int64 = 1 << 20
	defaultRequestTimeout       = 30 * time.Second
	healthPingTimeout           = 2 * time.Second
	mimeApplicationJSON         = "application/json"
)

// DBPinger checks underlying database connectivity for /health.
type DBPinger interface {
	Ping(ctx context.Context) error
}

// RouterDeps bundles handlers and dependencies wired by cmd/api/main.go.
type RouterDeps struct {
	Logger              *slog.Logger
	DB                  DBPinger
	AuthService         *service.AuthService
	EventService        *service.EventService
	OrderService        *service.OrderService
	PaymentService      *service.PaymentService
	DemoService         *service.DemoService
	AllowDemoHeaderAuth bool
	CORSAllowedOrigins  []string
}

// NewRouter constructs the Chi mux, configures Huma v2 + Scalar docs, and registers all endpoints.
func NewRouter(deps RouterDeps) http.Handler {
	logger := safeLogger(deps.Logger)
	configureHumaErrorFactory()

	allowedOrigins := deps.CORSAllowedOrigins
	if len(allowedOrigins) == 0 {
		allowedOrigins = defaultCORSAllowedOrigins()
	}

	r := chi.NewRouter()
	r.Use(chiMiddleware.RequestID)
	r.Use(chiMiddleware.RealIP)
	r.Use(middleware.RequestLogger(logger))
	r.Use(middleware.Recoverer(logger))
	r.Use(middleware.CORS(allowedOrigins))
	r.Use(middleware.MaxBodyBytes(maxRequestBodyBytes))
	r.Use(chiMiddleware.Timeout(defaultRequestTimeout))

	registerSystemRoutes(r, deps.DB, logger)

	cfg := huma.DefaultConfig("Treean API", "1.0.0")
	cfg.DocsRenderer = huma.DocsRendererScalar
	cfg.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		bearerScheme: {
			Type:         "http",
			Scheme:       bearerScheme,
			BearerFormat: "JWT",
		},
	}

	api := humachi.New(r, cfg)
	var authSvc middleware.Authenticator
	if deps.AuthService != nil {
		authSvc = deps.AuthService
	}
	api.UseMiddleware(middleware.NewHumaAuthMiddleware(api, authSvc, logger, deps.AllowDemoHeaderAuth))

	registerAuthRoutes(api, logger, deps.AuthService)
	registerEventRoutes(api, logger, deps.EventService)
	registerOrderRoutes(api, logger, deps.OrderService)
	registerWebhookRoutes(api, logger, deps.PaymentService)
	registerDemoRoutes(api, logger, deps.DemoService)

	return r
}

func registerSystemRoutes(r chi.Router, db DBPinger, logger *slog.Logger) {
	demoHTML, demoErr := web.DemoAssets.ReadFile("demo.html")
	if demoErr != nil {
		logger.Warn("failed to preload embedded demo page", slog.String("error", demoErr.Error()))
	}

	r.Get("/health", func(w http.ResponseWriter, req *http.Request) {
		if db == nil {
			writeRawResponse(w, http.StatusServiceUnavailable, mimeApplicationJSON, []byte(`{"status":"unhealthy"}`))
			return
		}
		ctx, cancel := context.WithTimeout(req.Context(), healthPingTimeout)
		defer cancel()
		if err := db.Ping(ctx); err != nil {
			writeRawResponse(w, http.StatusServiceUnavailable, mimeApplicationJSON, []byte(`{"status":"unhealthy"}`))
			return
		}
		writeRawResponse(w, http.StatusOK, mimeApplicationJSON, []byte(`{"status":"ok"}`))
	})

	r.Get("/demo", func(w http.ResponseWriter, _ *http.Request) {
		if demoErr != nil {
			http.Error(w, "demo page not found", http.StatusNotFound)
			return
		}
		writeRawResponse(w, http.StatusOK, "text/html; charset=utf-8", demoHTML)
	})
}

func writeRawResponse(w http.ResponseWriter, status int, contentType string, body []byte) {
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func defaultCORSAllowedOrigins() []string {
	return []string{
		"http://localhost:5173",
		"http://127.0.0.1:5173",
		"http://localhost:3000",
		"http://127.0.0.1:3000",
	}
}
