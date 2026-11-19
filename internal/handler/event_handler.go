package handler

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/torikh42/treean/internal/domain"
	"github.com/torikh42/treean/internal/service"
)

const eventsTag = "Events"

// CreateTierBody defines one ticket tier inside an event creation request.
type CreateTierBody struct {
	Name       string `json:"name" minLength:"1" maxLength:"60" example:"Reguler"`
	PriceIDR   int64  `json:"price_idr" minimum:"0" example:"150000"`
	TotalQuota int32  `json:"total_quota" minimum:"1" maximum:"100000" example:"80"`
}

// CreateEventBody defines the JSON payload for creating a ticketing event.
type CreateEventBody struct {
	Title       string           `json:"title" minLength:"3" maxLength:"150" example:"Indie Music Fest"`
	Description string           `json:"description,omitempty" maxLength:"1000"`
	Venue       string           `json:"venue" minLength:"2" maxLength:"150" example:"Senayan Hall"`
	EventDate   time.Time        `json:"event_date"`
	TicketTypes []CreateTierBody `json:"ticket_types" minItems:"2" maxItems:"10" doc:"At least 2 ticket types (e.g. Reguler, VIP)"`
}

func registerEventRoutes(api huma.API, logger *slog.Logger, eventSvc *service.EventService) {
	registerCreateEventRoute(api, logger, eventSvc)
	registerListEventsRoute(api, logger, eventSvc)
	registerGetEventRoute(api, logger, eventSvc)
}

func registerCreateEventRoute(api huma.API, logger *slog.Logger, eventSvc *service.EventService) {
	huma.Register(api, huma.Operation{
		OperationID:   "create-event",
		Method:        http.MethodPost,
		Path:          "/api/v1/events",
		Summary:       "Create a new event with 2+ ticket tiers (Organizer only)",
		Tags:          []string{eventsTag},
		DefaultStatus: http.StatusCreated,
		Security:      bearerSecurity,
	}, func(ctx context.Context, in *struct{ Body CreateEventBody }) (*struct{ Body *domain.Event }, error) {
		actor, err := requireActor(ctx)
		if err != nil {
			return nil, mapServiceError(ctx, logger, err)
		}
		tiers := make([]service.CreateTierInput, len(in.Body.TicketTypes))
		for i, tt := range in.Body.TicketTypes {
			tiers[i] = service.CreateTierInput{Name: tt.Name, PriceIDR: tt.PriceIDR, TotalQuota: tt.TotalQuota}
		}
		ev, err := eventSvc.CreateEvent(ctx, actor, service.CreateEventInput{
			Title:       in.Body.Title,
			Description: in.Body.Description,
			Venue:       in.Body.Venue,
			EventDate:   in.Body.EventDate,
			TicketTypes: tiers,
		})
		if err != nil {
			return nil, mapServiceError(ctx, logger, err)
		}
		return &struct{ Body *domain.Event }{Body: ev}, nil
	})
}

func registerListEventsRoute(api huma.API, logger *slog.Logger, eventSvc *service.EventService) {
	huma.Register(api, huma.Operation{
		OperationID: "list-events",
		Method:      http.MethodGet,
		Path:        "/api/v1/events",
		Summary:     "List all events with live remaining ticket quotas",
		Tags:        []string{eventsTag},
	}, func(ctx context.Context, _ *struct{}) (*struct{ Body []domain.Event }, error) {
		evs, err := eventSvc.ListEvents(ctx)
		if err != nil {
			return nil, mapServiceError(ctx, logger, err)
		}
		return &struct{ Body []domain.Event }{Body: evs}, nil
	})
}

func registerGetEventRoute(api huma.API, logger *slog.Logger, eventSvc *service.EventService) {
	huma.Register(api, huma.Operation{
		OperationID:        "get-event",
		Method:             http.MethodGet,
		Path:               "/api/v1/events/{id}",
		Summary:            "Get event details and live remaining quota per ticket tier",
		Tags:               []string{eventsTag},
		SkipValidateParams: true,
	}, func(ctx context.Context, in *struct {
		ID string `path:"id" format:"uuid"`
	}) (*struct{ Body *domain.Event }, error) {
		uid, err := parseNonNilUUID(in.ID, "event")
		if err != nil {
			return nil, mapServiceError(ctx, logger, err)
		}
		ev, err := eventSvc.GetEvent(ctx, uid)
		if err != nil {
			return nil, mapServiceError(ctx, logger, err)
		}
		return &struct{ Body *domain.Event }{Body: ev}, nil
	})
}
