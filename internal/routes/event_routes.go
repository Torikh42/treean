package routes

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/torikh42/treean/internal/handler"
)

const eventsTag = "Events"

func registerEventRoutes(api huma.API, h *handler.EventHandler) {
	if h == nil {
		return
	}

	huma.Register(api, huma.Operation{
		OperationID:   "create-event",
		Method:        http.MethodPost,
		Path:          "/api/v1/events",
		Summary:       "Create a new event with 2+ ticket tiers (Organizer only)",
		Tags:          []string{eventsTag},
		DefaultStatus: http.StatusCreated,
		Security:      bearerSecurity,
	}, h.Create)

	huma.Register(api, huma.Operation{
		OperationID: "list-events",
		Method:      http.MethodGet,
		Path:        "/api/v1/events",
		Summary:     "List all events with live remaining ticket quotas",
		Tags:        []string{eventsTag},
	}, h.List)

	huma.Register(api, huma.Operation{
		OperationID: "list-organizer-events",
		Method:      http.MethodGet,
		Path:        "/api/v1/organizer/events",
		Summary:     "List events owned by the authenticated organizer",
		Tags:        []string{eventsTag},
		Security:    bearerSecurity,
	}, h.ListOrganizer)

	huma.Register(api, huma.Operation{
		OperationID:        "get-event",
		Method:             http.MethodGet,
		Path:               "/api/v1/events/{id}",
		Summary:            "Get event details and live remaining quota per ticket tier",
		Tags:               []string{eventsTag},
		SkipValidateParams: true,
	}, h.Get)

	huma.Register(api, huma.Operation{
		OperationID:        "update-event",
		Method:             http.MethodPut,
		Path:               "/api/v1/events/{id}",
		Summary:            "Update an event and its ticket tiers (Owner organizer only)",
		Tags:               []string{eventsTag},
		Security:           bearerSecurity,
		SkipValidateParams: true,
	}, h.Update)

	huma.Register(api, huma.Operation{
		OperationID:        "delete-event",
		Method:             http.MethodDelete,
		Path:               "/api/v1/events/{id}",
		Summary:            "Delete an unused event with no orders (Owner organizer only)",
		Tags:               []string{eventsTag},
		DefaultStatus:      http.StatusNoContent,
		Security:           bearerSecurity,
		SkipValidateParams: true,
	}, h.Delete)
}
