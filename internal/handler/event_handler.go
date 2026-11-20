package handler

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/torikh42/treean/internal/domain"
	"github.com/torikh42/treean/internal/service"
)

const fieldEvent = "event"

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

// UpdateTierBody defines one ticket tier inside an event update request.
type UpdateTierBody struct {
	ID         *string `json:"id,omitempty" format:"uuid" doc:"Existing tier ID (omit to create a new tier)"`
	Name       string  `json:"name" minLength:"1" maxLength:"60" example:"Reguler"`
	PriceIDR   int64   `json:"price_idr" minimum:"0" example:"150000"`
	TotalQuota int32   `json:"total_quota" minimum:"1" maximum:"100000" example:"80"`
}

// UpdateEventBody defines the JSON payload for updating an existing ticketing event.
type UpdateEventBody struct {
	Title       string           `json:"title" minLength:"3" maxLength:"150" example:"Indie Music Fest"`
	Description string           `json:"description,omitempty" maxLength:"1000"`
	Venue       string           `json:"venue" minLength:"2" maxLength:"150" example:"Senayan Hall"`
	EventDate   time.Time        `json:"event_date"`
	TicketTypes []UpdateTierBody `json:"ticket_types" minItems:"2" maxItems:"10" doc:"Between 2 and 10 ticket types"`
}

// EventHandler handles public event catalog queries and organizer event CRUD.
type EventHandler struct {
	svc    *service.EventService
	logger *slog.Logger
}

// NewEventHandler constructs an EventHandler.
func NewEventHandler(svc *service.EventService, logger *slog.Logger) *EventHandler {
	return &EventHandler{svc: svc, logger: SafeLogger(logger)}
}

// Create creates a new event with 2+ ticket tiers for the authenticated organizer.
func (h *EventHandler) Create(ctx context.Context, in *struct{ Body CreateEventBody }) (*struct{ Body *domain.Event }, error) {
	actor, err := RequireActor(ctx)
	if err != nil {
		return nil, MapServiceError(ctx, h.logger, err)
	}
	tiers := make([]service.CreateTierInput, len(in.Body.TicketTypes))
	for i, tt := range in.Body.TicketTypes {
		tiers[i] = service.CreateTierInput{Name: tt.Name, PriceIDR: tt.PriceIDR, TotalQuota: tt.TotalQuota}
	}
	ev, err := h.svc.CreateEvent(ctx, actor, service.CreateEventInput{
		Title:       in.Body.Title,
		Description: in.Body.Description,
		Venue:       in.Body.Venue,
		EventDate:   in.Body.EventDate,
		TicketTypes: tiers,
	})
	if err != nil {
		return nil, MapServiceError(ctx, h.logger, err)
	}
	return &struct{ Body *domain.Event }{Body: ev}, nil
}

// List returns all events with live remaining ticket quotas.
func (h *EventHandler) List(ctx context.Context, _ *struct{}) (*struct{ Body []domain.Event }, error) {
	evs, err := h.svc.ListEvents(ctx)
	if err != nil {
		return nil, MapServiceError(ctx, h.logger, err)
	}
	return &struct{ Body []domain.Event }{Body: evs}, nil
}

// Get returns a single event with live remaining quota per ticket tier.
func (h *EventHandler) Get(ctx context.Context, in *struct {
	ID string `path:"id" format:"uuid"`
}) (*struct{ Body *domain.Event }, error) {
	uid, err := ParseNonNilUUID(in.ID, fieldEvent)
	if err != nil {
		return nil, MapServiceError(ctx, h.logger, err)
	}
	ev, err := h.svc.GetEvent(ctx, uid)
	if err != nil {
		return nil, MapServiceError(ctx, h.logger, err)
	}
	return &struct{ Body *domain.Event }{Body: ev}, nil
}

// ListOrganizer returns events owned by the authenticated organizer.
func (h *EventHandler) ListOrganizer(ctx context.Context, _ *struct{}) (*struct{ Body []domain.Event }, error) {
	actor, err := RequireActor(ctx)
	if err != nil {
		return nil, MapServiceError(ctx, h.logger, err)
	}
	evs, err := h.svc.ListOrganizerEvents(ctx, actor)
	if err != nil {
		return nil, MapServiceError(ctx, h.logger, err)
	}
	return &struct{ Body []domain.Event }{Body: evs}, nil
}

// Update modifies an existing event and its ticket tiers owned by the authenticated organizer.
func (h *EventHandler) Update(ctx context.Context, in *struct {
	ID   string `path:"id" format:"uuid"`
	Body UpdateEventBody
}) (*struct{ Body *domain.Event }, error) {
	actor, err := RequireActor(ctx)
	if err != nil {
		return nil, MapServiceError(ctx, h.logger, err)
	}
	uid, err := ParseNonNilUUID(in.ID, fieldEvent)
	if err != nil {
		return nil, MapServiceError(ctx, h.logger, err)
	}

	tiers, err := parseUpdateTierInputs(in.Body.TicketTypes)
	if err != nil {
		return nil, MapServiceError(ctx, h.logger, err)
	}

	ev, err := h.svc.UpdateEvent(ctx, actor, uid, service.UpdateEventInput{
		Title:       in.Body.Title,
		Description: in.Body.Description,
		Venue:       in.Body.Venue,
		EventDate:   in.Body.EventDate,
		TicketTypes: tiers,
	})
	if err != nil {
		return nil, MapServiceError(ctx, h.logger, err)
	}
	return &struct{ Body *domain.Event }{Body: ev}, nil
}

// Delete removes an unused event with zero buyer orders owned by the authenticated organizer.
func (h *EventHandler) Delete(ctx context.Context, in *struct {
	ID string `path:"id" format:"uuid"`
}) (*struct{}, error) {
	actor, err := RequireActor(ctx)
	if err != nil {
		return nil, MapServiceError(ctx, h.logger, err)
	}
	uid, err := ParseNonNilUUID(in.ID, fieldEvent)
	if err != nil {
		return nil, MapServiceError(ctx, h.logger, err)
	}
	if err := h.svc.DeleteEvent(ctx, actor, uid); err != nil {
		return nil, MapServiceError(ctx, h.logger, err)
	}
	return nil, nil
}

func parseUpdateTierInputs(raw []UpdateTierBody) ([]service.UpdateTierInput, error) {
	tiers := make([]service.UpdateTierInput, len(raw))
	for i, tt := range raw {
		var tierID *uuid.UUID
		if tt.ID != nil {
			parsed, err := ParseNonNilUUID(*tt.ID, "ticket_type_id")
			if err != nil {
				return nil, err
			}
			tierID = &parsed
		}
		tiers[i] = service.UpdateTierInput{
			ID:         tierID,
			Name:       tt.Name,
			PriceIDR:   tt.PriceIDR,
			TotalQuota: tt.TotalQuota,
		}
	}
	return tiers, nil
}
