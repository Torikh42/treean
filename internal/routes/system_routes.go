package routes

import (
	"github.com/go-chi/chi/v5"
	"github.com/torikh42/treean/internal/handler"
)

func registerSystemRoutes(r chi.Router, h *handler.SystemHandler) {
	if h == nil {
		return
	}
	r.Get("/health", h.Health)
	r.Get("/demo", h.DemoLanding)
}
