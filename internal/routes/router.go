// Package routes mounts HTTP middleware, Huma v2 OpenAPI docs, and domain endpoint routes.
package routes

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/torikh42/treean/internal/handler"
	"github.com/torikh42/treean/internal/middleware"
)

const (
	bearerScheme          = "bearer"
	maxRequestBodyBytes   = int64(1 << 20)
	defaultRequestTimeout = 30 * time.Second
)

var bearerSecurity = []map[string][]string{{bearerScheme: {}}}

// RouterDeps bundles handlers and middleware dependencies wired by cmd/api/main.go.
type RouterDeps struct {
	Logger              *slog.Logger
	Authenticator       middleware.Authenticator
	System              *handler.SystemHandler
	Auth                *handler.AuthHandler
	Events              *handler.EventHandler
	Orders              *handler.OrderHandler
	Webhooks            *handler.WebhookHandler
	Demo                *handler.DemoHandler
	AllowDemoHeaderAuth bool
	CORSAllowedOrigins  []string
}

// NewRouter constructs the Chi mux, configures Huma v2 + Scalar docs, and registers all routes.
func NewRouter(deps RouterDeps) http.Handler {
	logger := handler.SafeLogger(deps.Logger)
	handler.ConfigureHumaErrorFactory()

	allowedOrigins := deps.CORSAllowedOrigins
	if len(allowedOrigins) == 0 {
		allowedOrigins = defaultCORSAllowedOrigins()
	}

	r := chi.NewRouter()
	r.Use(chiMiddleware.RequestID)
	r.Use(middleware.RequestLogger(logger))
	r.Use(middleware.Recoverer(logger))
	r.Use(middleware.CORS(allowedOrigins))
	r.Use(middleware.MaxBodyBytes(maxRequestBodyBytes))
	r.Use(chiMiddleware.Timeout(defaultRequestTimeout))

	sysHandler := deps.System
	if sysHandler == nil {
		sysHandler = handler.NewSystemHandler(nil, logger)
	}
	registerSystemRoutes(r, sysHandler)

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
	api.UseMiddleware(middleware.NewHumaAuthMiddleware(api, deps.Authenticator, logger, deps.AllowDemoHeaderAuth))

	registerAuthRoutes(api, deps.Auth)
	registerEventRoutes(api, deps.Events)
	registerOrderRoutes(api, deps.Orders)
	registerWebhookRoutes(api, deps.Webhooks)
	registerDemoRoutes(api, deps.Demo)

	return r
}

func defaultCORSAllowedOrigins() []string {
	return []string{
		"http://localhost:5173",
		"http://127.0.0.1:5173",
		"http://localhost:3000",
		"http://127.0.0.1:3000",
	}
}
