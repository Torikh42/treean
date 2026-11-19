package service

import (
	"context"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/torikh42/treean/internal/domain"
)

const (
	minEventTitleRunes = 3
	minEventVenueRunes = 2
	minEventTierCount  = 2
	maxEventTierCount  = 10
)

// CreateTierInput defines input for creating one ticket tier.
type CreateTierInput struct {
	Name       string
	PriceIDR   int64
	TotalQuota int32
}

// CreateEventInput defines input for creating an event with ticket tiers.
type CreateEventInput struct {
	Title       string
	Description string
	Venue       string
	EventDate   time.Time
	TicketTypes []CreateTierInput
}

// EventService handles event management and real-time quota views.
type EventService struct {
	events EventRepository
	orders OrderRepository
}

// NewEventService constructs an EventService.
func NewEventService(events EventRepository, orders OrderRepository) *EventService {
	return &EventService{events: events, orders: orders}
}

// CreateEvent enforces ORGANIZER role and minimum 2 distinct ticket tiers.
func (s *EventService) CreateEvent(ctx context.Context, actor domain.Actor, in CreateEventInput) (*domain.Event, error) {
	if err := validateServiceCall(ctx, "creating event", s != nil && s.events != nil); err != nil {
		return nil, err
	}
	if err := validateAuthenticatedActor(actor); err != nil {
		return nil, err
	}
	if actor.Role != domain.RoleOrganizer {
		return nil, domain.NewDomainError(http.StatusForbidden, domain.ErrCodeForbidden, "only organizers can create events")
	}

	ev, err := buildEventFromInput(actor.ID, in)
	if err != nil {
		return nil, err
	}
	if err := s.events.CreateWithTiers(ctx, ev); err != nil {
		return nil, err
	}
	return ev, nil
}

// ListEvents performs a lazy expiry pass via partial index and returns real-time remaining quotas.
func (s *EventService) ListEvents(ctx context.Context) ([]domain.Event, error) {
	if err := validateServiceCall(ctx, "listing events", s != nil && s.events != nil && s.orders != nil); err != nil {
		return nil, err
	}
	if _, err := s.orders.ExpireDueOrders(ctx, nil, lazyExpiryBatchLimit, lazyExpiryActorType, time.Now().UTC()); err != nil {
		return nil, err
	}
	return s.events.ListAll(ctx)
}

// GetEvent sweeps any due orders for the target event and returns live remaining quota.
func (s *EventService) GetEvent(ctx context.Context, eventID uuid.UUID) (*domain.Event, error) {
	if err := validateServiceCall(ctx, "getting event", s != nil && s.events != nil && s.orders != nil); err != nil {
		return nil, err
	}
	if eventID == uuid.Nil {
		return nil, domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeValidation, "event_id is required")
	}
	if _, err := s.orders.ExpireDueOrders(ctx, &eventID, lazyExpiryBatchLimit, lazyExpiryActorType, time.Now().UTC()); err != nil {
		return nil, err
	}
	return s.events.FindByID(ctx, eventID)
}

func buildEventFromInput(organizerID uuid.UUID, in CreateEventInput) (*domain.Event, error) {
	title := strings.TrimSpace(in.Title)
	if utf8.RuneCountInString(title) < minEventTitleRunes {
		return nil, domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeValidation, "title must be at least 3 characters")
	}
	venue := strings.TrimSpace(in.Venue)
	if utf8.RuneCountInString(venue) < minEventVenueRunes {
		return nil, domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeValidation, "venue must be at least 2 characters")
	}
	if in.EventDate.IsZero() {
		return nil, domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeValidation, "event_date is required")
	}
	if len(in.TicketTypes) < minEventTierCount || len(in.TicketTypes) > maxEventTierCount {
		return nil, domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeValidation, "an event must define between 2 and 10 ticket types")
	}

	ev := &domain.Event{
		ID:          uuid.New(),
		OrganizerID: organizerID,
		Title:       title,
		Description: strings.TrimSpace(in.Description),
		Venue:       venue,
		EventDate:   in.EventDate.UTC(),
		TicketTypes: make([]domain.TicketType, 0, len(in.TicketTypes)),
	}

	seenNames := make(map[string]struct{}, len(in.TicketTypes))
	for _, tier := range in.TicketTypes {
		name := strings.TrimSpace(tier.Name)
		key := strings.ToLower(name)
		if _, dup := seenNames[key]; dup {
			return nil, domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeValidation, "duplicate ticket type names are not allowed")
		}
		seenNames[key] = struct{}{}

		tt := domain.TicketType{
			ID:             uuid.New(),
			EventID:        ev.ID,
			Name:           name,
			PriceIDR:       tier.PriceIDR,
			TotalQuota:     tier.TotalQuota,
			RemainingQuota: tier.TotalQuota,
		}
		if err := tt.Validate(); err != nil {
			return nil, err
		}
		ev.TicketTypes = append(ev.TicketTypes, tt)
	}
	return ev, nil
}
