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

// UpdateTierInput defines input for creating or updating one ticket tier.
type UpdateTierInput struct {
	ID         *uuid.UUID
	Name       string
	PriceIDR   int64
	TotalQuota int32
}

// UpdateEventInput defines input for updating an event and its ticket tiers.
type UpdateEventInput struct {
	Title       string
	Description string
	Venue       string
	EventDate   time.Time
	TicketTypes []UpdateTierInput
}

// EventService handles event management and real-time quota views.
type EventService struct {
	events   EventRepository
	orders   OrderRepository
	cache    EventCatalogCache
	cacheTTL time.Duration
}

// NewEventService constructs an EventService.
func NewEventService(events EventRepository, orders OrderRepository) *EventService {
	return &EventService{events: events, orders: orders}
}

// WithCache attaches an optional fail-open EventCatalogCache for public catalog/detail queries.
func (s *EventService) WithCache(cache EventCatalogCache, ttl time.Duration) *EventService {
	if s != nil {
		s.cache = cache
		s.cacheTTL = ttl
	}
	return s
}

// CreateEvent enforces ORGANIZER role and minimum 2 distinct ticket tiers.
func (s *EventService) CreateEvent(ctx context.Context, actor domain.Actor, in CreateEventInput) (*domain.Event, error) {
	if err := validateServiceCall(ctx, "creating event", s != nil && s.events != nil); err != nil {
		return nil, err
	}
	if err := requireOrganizerActor(actor, "only organizers can create events"); err != nil {
		return nil, err
	}

	tiers := make([]UpdateTierInput, len(in.TicketTypes))
	for i, tier := range in.TicketTypes {
		tiers[i] = UpdateTierInput{
			Name:       tier.Name,
			PriceIDR:   tier.PriceIDR,
			TotalQuota: tier.TotalQuota,
		}
	}

	ev, err := buildEventFromFields(uuid.New(), actor.ID, in.Title, in.Description, in.Venue, in.EventDate, tiers, true)
	if err != nil {
		return nil, err
	}
	if err := s.events.CreateWithTiers(ctx, ev); err != nil {
		return nil, err
	}
	s.invalidateCache(ctx, ev.ID)
	return ev, nil
}

// ListEvents performs a lazy expiry pass via partial index and returns real-time remaining quotas.
func (s *EventService) ListEvents(ctx context.Context) ([]domain.Event, error) {
	if err := validateServiceCall(ctx, "listing events", s != nil && s.events != nil && s.orders != nil); err != nil {
		return nil, err
	}
	if s.cache != nil {
		if cached, hit := s.cache.GetEventsCatalog(ctx); hit {
			return cached, nil
		}
	}
	if err := s.expireAndInvalidate(ctx, nil); err != nil {
		return nil, err
	}
	evs, err := s.events.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	if s.cache != nil {
		s.cache.SetEventsCatalog(ctx, evs, s.cacheTTL)
	}
	return evs, nil
}

// ListOrganizerEvents sweeps due orders and returns events owned by the authenticated organizer.
func (s *EventService) ListOrganizerEvents(ctx context.Context, actor domain.Actor) ([]domain.Event, error) {
	if err := validateServiceCall(ctx, "listing organizer events", s != nil && s.events != nil && s.orders != nil); err != nil {
		return nil, err
	}
	if err := requireOrganizerActor(actor, "only organizers can list their events"); err != nil {
		return nil, err
	}
	if err := s.expireAndInvalidate(ctx, nil); err != nil {
		return nil, err
	}
	return s.events.ListByOrganizer(ctx, actor.ID)
}

// GetEvent sweeps any due orders for the target event and returns live remaining quota.
func (s *EventService) GetEvent(ctx context.Context, eventID uuid.UUID) (*domain.Event, error) {
	if err := validateServiceCall(ctx, "getting event", s != nil && s.events != nil && s.orders != nil); err != nil {
		return nil, err
	}
	if eventID == uuid.Nil {
		return nil, domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeValidation, "event_id is required")
	}
	if s.cache != nil {
		if cached, hit := s.cache.GetEventDetail(ctx, eventID); hit {
			return cached, nil
		}
	}
	if err := s.expireAndInvalidate(ctx, &eventID); err != nil {
		return nil, err
	}
	ev, err := s.events.FindByID(ctx, eventID)
	if err != nil {
		return nil, err
	}
	if s.cache != nil && ev != nil {
		s.cache.SetEventDetail(ctx, ev, s.cacheTTL)
	}
	return ev, nil
}

// UpdateEvent enforces organizer ownership, validates metadata and tiers, and atomically updates the event.
func (s *EventService) UpdateEvent(ctx context.Context, actor domain.Actor, eventID uuid.UUID, in UpdateEventInput) (*domain.Event, error) {
	if err := validateServiceCall(ctx, "updating event", s != nil && s.events != nil); err != nil {
		return nil, err
	}
	if err := requireOrganizerActor(actor, "only organizers can update events"); err != nil {
		return nil, err
	}
	if eventID == uuid.Nil {
		return nil, domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeValidation, "event_id is required")
	}

	ev, err := buildEventFromFields(eventID, actor.ID, in.Title, in.Description, in.Venue, in.EventDate, in.TicketTypes, false)
	if err != nil {
		return nil, err
	}
	if s.orders != nil {
		_ = s.expireAndInvalidate(ctx, &eventID)
	}
	if err := s.events.UpdateWithTiers(ctx, actor.ID, ev); err != nil {
		return nil, err
	}
	s.invalidateCache(ctx, eventID)
	return ev, nil
}

// DeleteEvent enforces organizer ownership and deletes an event only when no orders exist for it.
func (s *EventService) DeleteEvent(ctx context.Context, actor domain.Actor, eventID uuid.UUID) error {
	if err := validateServiceCall(ctx, "deleting event", s != nil && s.events != nil); err != nil {
		return err
	}
	if err := requireOrganizerActor(actor, "only organizers can delete events"); err != nil {
		return err
	}
	if eventID == uuid.Nil {
		return domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeValidation, "event_id is required")
	}
	if err := s.events.DeleteUnused(ctx, actor.ID, eventID); err != nil {
		return err
	}
	s.invalidateCache(ctx, eventID)
	return nil
}

func (s *EventService) expireAndInvalidate(ctx context.Context, eventID *uuid.UUID) error {
	nowUTC := time.Now().UTC()
	if aware, ok := s.orders.(EventAwareExpiryStore); ok {
		count, touchedEvents, err := aware.ExpireDueOrdersWithEvents(ctx, eventID, lazyExpiryBatchLimit, lazyExpiryActorType, nowUTC)
		if err != nil {
			return err
		}
		if count > 0 {
			s.invalidateCache(ctx, touchedEvents...)
		}
		return nil
	}
	count, err := s.orders.ExpireDueOrders(ctx, eventID, lazyExpiryBatchLimit, lazyExpiryActorType, nowUTC)
	if err != nil {
		return err
	}
	if count > 0 {
		if eventID != nil {
			s.invalidateCache(ctx, *eventID)
		} else {
			s.invalidateCache(ctx)
		}
	}
	return nil
}

func (s *EventService) invalidateCache(ctx context.Context, eventIDs ...uuid.UUID) {
	if s.cache != nil {
		s.cache.InvalidateEvents(ctx, eventIDs...)
	}
}

func requireOrganizerActor(actor domain.Actor, msg string) error {
	if err := validateAuthenticatedActor(actor); err != nil {
		return err
	}
	if actor.Role != domain.RoleOrganizer {
		return domain.NewDomainError(http.StatusForbidden, domain.ErrCodeForbidden, msg)
	}
	return nil
}

func buildEventFromFields(
	eventID uuid.UUID,
	organizerID uuid.UUID,
	rawTitle string,
	rawDescription string,
	rawVenue string,
	eventDate time.Time,
	tiers []UpdateTierInput,
	assignNewIDs bool,
) (*domain.Event, error) {
	title := strings.TrimSpace(rawTitle)
	if utf8.RuneCountInString(title) < minEventTitleRunes {
		return nil, domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeValidation, "title must be at least 3 characters")
	}
	venue := strings.TrimSpace(rawVenue)
	if utf8.RuneCountInString(venue) < minEventVenueRunes {
		return nil, domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeValidation, "venue must be at least 2 characters")
	}
	if eventDate.IsZero() {
		return nil, domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeValidation, "event_date is required")
	}
	if len(tiers) < minEventTierCount || len(tiers) > maxEventTierCount {
		return nil, domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeValidation, "an event must define between 2 and 10 ticket types")
	}

	builtTiers, err := buildValidatedTiers(eventID, tiers, assignNewIDs)
	if err != nil {
		return nil, err
	}

	return &domain.Event{
		ID:          eventID,
		OrganizerID: organizerID,
		Title:       title,
		Description: strings.TrimSpace(rawDescription),
		Venue:       venue,
		EventDate:   eventDate.UTC(),
		TicketTypes: builtTiers,
	}, nil
}

func buildValidatedTiers(eventID uuid.UUID, tiers []UpdateTierInput, assignNewIDs bool) ([]domain.TicketType, error) {
	built := make([]domain.TicketType, 0, len(tiers))
	seenNames := make(map[string]struct{}, len(tiers))
	seenIDs := make(map[uuid.UUID]struct{}, len(tiers))

	for _, tier := range tiers {
		tierID, err := resolveTierID(tier.ID, assignNewIDs, seenIDs)
		if err != nil {
			return nil, err
		}

		name := strings.TrimSpace(tier.Name)
		key := strings.ToLower(name)
		if _, dup := seenNames[key]; dup {
			return nil, domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeValidation, "duplicate ticket type names are not allowed")
		}
		seenNames[key] = struct{}{}

		tt := domain.TicketType{
			ID:             tierID,
			EventID:        eventID,
			Name:           name,
			PriceIDR:       tier.PriceIDR,
			TotalQuota:     tier.TotalQuota,
			RemainingQuota: tier.TotalQuota,
		}
		if err := tt.Validate(); err != nil {
			return nil, err
		}
		built = append(built, tt)
	}
	return built, nil
}

func resolveTierID(rawID *uuid.UUID, assignNewIDs bool, seenIDs map[uuid.UUID]struct{}) (uuid.UUID, error) {
	if rawID == nil {
		if assignNewIDs {
			return uuid.New(), nil
		}
		return uuid.Nil, nil
	}
	if *rawID == uuid.Nil {
		return uuid.Nil, domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeValidation, "ticket_type id must be a valid UUID")
	}
	if _, dup := seenIDs[*rawID]; dup {
		return uuid.Nil, domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeValidation, "duplicate ticket type IDs are not allowed")
	}
	seenIDs[*rawID] = struct{}{}
	return *rawID, nil
}
