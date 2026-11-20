package handler

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/torikh42/treean/web"
)

const (
	healthPingTimeout   = 2 * time.Second
	mimeApplicationJSON = "application/json"
)

// DBPinger checks underlying database connectivity for /health.
type DBPinger interface {
	Ping(ctx context.Context) error
}

// SystemHandler serves infrastructure health probes and the embedded backend landing stub.
type SystemHandler struct {
	db       DBPinger
	logger   *slog.Logger
	demoHTML []byte
	demoErr  error
}

// NewSystemHandler constructs a SystemHandler and preloads the embedded demo landing HTML.
func NewSystemHandler(db DBPinger, logger *slog.Logger) *SystemHandler {
	log := SafeLogger(logger)
	demoHTML, demoErr := web.DemoAssets.ReadFile("demo.html")
	if demoErr != nil {
		log.Warn("failed to preload embedded demo page", slog.String("error", demoErr.Error()))
	}
	return &SystemHandler{
		db:       db,
		logger:   log,
		demoHTML: demoHTML,
		demoErr:  demoErr,
	}
}

// Health verifies PostgreSQL reachability and responds 200 ok or 503 unhealthy.
func (h *SystemHandler) Health(w http.ResponseWriter, req *http.Request) {
	if h == nil || h.db == nil {
		WriteRawResponse(w, http.StatusServiceUnavailable, mimeApplicationJSON, []byte(`{"status":"unhealthy"}`))
		return
	}
	ctx, cancel := context.WithTimeout(req.Context(), healthPingTimeout)
	defer cancel()
	if err := h.db.Ping(ctx); err != nil {
		WriteRawResponse(w, http.StatusServiceUnavailable, mimeApplicationJSON, []byte(`{"status":"unhealthy"}`))
		return
	}
	WriteRawResponse(w, http.StatusOK, mimeApplicationJSON, []byte(`{"status":"ok"}`))
}

// DemoLanding renders the embedded minimal backend landing stub for /demo.
func (h *SystemHandler) DemoLanding(w http.ResponseWriter, _ *http.Request) {
	if h == nil || h.demoErr != nil {
		http.Error(w, "demo page not found", http.StatusNotFound)
		return
	}
	WriteRawResponse(w, http.StatusOK, "text/html; charset=utf-8", h.demoHTML)
}
