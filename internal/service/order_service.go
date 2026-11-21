package service

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/torikh42/treean/internal/domain"
	"github.com/torikh42/treean/internal/repository"
)

const (
	errMsgInvalidOrderQty  = "order total_tickets must be between 1 and 4"
	maxIdempotencyKeyRunes = 128
)

// CreateOrderInput represents a buyer's multi-item checkout request.
type CreateOrderInput struct {
	EventID        uuid.UUID
	Items          []repository.CheckoutItemInput
	IdempotencyKey string
}

// OrderService orchestrates ticket booking, cancellation, and history lookup.
type OrderService struct {
	orders OrderRepository
	events EventRepository
	cache  EventCatalogCache
}

// NewOrderService constructs an OrderService.
func NewOrderService(orders OrderRepository, events EventRepository) *OrderService {
	return &OrderService{orders: orders, events: events}
}

// WithCache attaches an optional EventCatalogCache for invalidation on order mutations.
func (s *OrderService) WithCache(cache EventCatalogCache) *OrderService {
	if s != nil {
		s.cache = cache
	}
	return s
}

// CreateOrder validates total quantity (1..4) and delegates atomic dual-locked creation to repository.
func (s *OrderService) CreateOrder(ctx context.Context, actor domain.Actor, in CreateOrderInput) (*domain.Order, error) {
	res, err := s.CreateOrderWithResult(ctx, actor, in)
	if err != nil {
		return nil, err
	}
	return res.Order, nil
}

// CreateOrderWithResult validates checkout input, enforces Idempotency-Key deduplication, and invalidates event cache.
func (s *OrderService) CreateOrderWithResult(ctx context.Context, actor domain.Actor, in CreateOrderInput) (*repository.CreateOrderResult, error) {
	if err := validateServiceCall(ctx, "creating order", s != nil && s.orders != nil); err != nil {
		return nil, err
	}
	if err := validateAuthenticatedActor(actor); err != nil {
		return nil, err
	}
	if in.EventID == uuid.Nil {
		return nil, domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeValidation, "event_id is required")
	}

	cleanKey := strings.TrimSpace(in.IdempotencyKey)
	if utf8.RuneCountInString(cleanKey) > maxIdempotencyKeyRunes {
		return nil, domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeValidation, "Idempotency-Key must be at most 128 characters")
	}

	deduped, err := normalizeOrderItems(in.Items)
	if err != nil {
		return nil, err
	}

	nowUTC := time.Now().UTC()
	if idempCreator, ok := s.orders.(IdempotentOrderCreator); ok {
		res, createErr := idempCreator.CreateOrderIdempotent(ctx, in.EventID, actor.ID, deduped, cleanKey, nowUTC)
		if createErr != nil {
			return nil, createErr
		}
		if s.cache != nil && res != nil && !res.IdempotentReplay {
			s.cache.InvalidateEvents(ctx, in.EventID)
		}
		return res, nil
	}

	order, err := s.orders.CreateOrder(ctx, in.EventID, actor.ID, deduped, nowUTC)
	if err != nil {
		return nil, err
	}
	if s.cache != nil {
		s.cache.InvalidateEvents(ctx, in.EventID)
	}
	return &repository.CreateOrderResult{Order: order, IdempotentReplay: false}, nil
}

// CancelOrder cancels an unpaid order owned by the buyer and restores quota.
func (s *OrderService) CancelOrder(ctx context.Context, actor domain.Actor, orderID uuid.UUID) (*domain.Order, error) {
	if err := validateServiceCall(ctx, "cancelling order", s != nil && s.orders != nil); err != nil {
		return nil, err
	}
	if err := validateAuthenticatedActor(actor); err != nil {
		return nil, err
	}
	if orderID == uuid.Nil {
		return nil, domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeValidation, "order_id is required")
	}
	order, err := s.orders.CancelOrder(ctx, orderID, actor.ID, time.Now().UTC())
	if err != nil {
		s.invalidateOnExpiryError(ctx, orderID, err)
		return nil, err
	}
	if s.cache != nil && order != nil {
		s.cache.InvalidateEvents(ctx, order.EventID)
	}
	return order, nil
}

func (s *OrderService) invalidateOnExpiryError(ctx context.Context, orderID uuid.UUID, err error) {
	if s.cache == nil {
		return
	}
	if de, ok := errors.AsType[*domain.DomainError](err); ok && de.Code == domain.ErrCodeOrderExpired {
		if existing, findErr := s.orders.FindByID(ctx, orderID); findErr == nil && existing != nil {
			s.cache.InvalidateEvents(ctx, existing.EventID)
			return
		}
		s.cache.InvalidateEvents(ctx)
	}
}

// ListOrders returns order history and tickets belonging to the calling buyer.
func (s *OrderService) ListOrders(ctx context.Context, actor domain.Actor) ([]domain.Order, error) {
	if err := validateServiceCall(ctx, "listing orders", s != nil && s.orders != nil); err != nil {
		return nil, err
	}
	if err := validateAuthenticatedActor(actor); err != nil {
		return nil, err
	}
	if err := s.sweepAndInvalidate(ctx, nil); err != nil {
		return nil, err
	}
	return s.orders.ListByBuyer(ctx, actor.ID)
}

// GetOrder returns a single order with items, logs, and tickets after enforcing buyer or event-organizer ownership.
func (s *OrderService) GetOrder(ctx context.Context, actor domain.Actor, orderID uuid.UUID) (*domain.Order, error) {
	if err := validateServiceCall(ctx, "getting order", s != nil && s.orders != nil && s.events != nil); err != nil {
		return nil, err
	}
	if err := validateAuthenticatedActor(actor); err != nil {
		return nil, err
	}
	if orderID == uuid.Nil {
		return nil, domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeValidation, "order_id is required")
	}

	_ = s.sweepAndInvalidate(ctx, nil)

	o, err := s.orders.FindByID(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if o == nil {
		return nil, domain.NewDomainError(http.StatusNotFound, domain.ErrCodeOrderNotFound, "order not found")
	}
	if err := authorizeOrderAccess(ctx, s.events, actor, o, "cannot access this order"); err != nil {
		return nil, err
	}
	return o, nil
}

func (s *OrderService) sweepAndInvalidate(ctx context.Context, eventID *uuid.UUID) error {
	nowUTC := time.Now().UTC()
	if aware, ok := s.orders.(EventAwareExpiryStore); ok {
		count, touchedEvents, err := aware.ExpireDueOrdersWithEvents(ctx, eventID, lazyExpiryBatchLimit, lazyExpiryActorType, nowUTC)
		if err != nil {
			return err
		}
		if count > 0 && s.cache != nil {
			s.cache.InvalidateEvents(ctx, touchedEvents...)
		}
		return nil
	}
	count, err := s.orders.ExpireDueOrders(ctx, eventID, lazyExpiryBatchLimit, lazyExpiryActorType, nowUTC)
	if err != nil {
		return err
	}
	if count > 0 && s.cache != nil {
		if eventID != nil {
			s.cache.InvalidateEvents(ctx, *eventID)
		} else {
			s.cache.InvalidateEvents(ctx)
		}
	}
	return nil
}

func normalizeOrderItems(items []repository.CheckoutItemInput) ([]repository.CheckoutItemInput, error) {
	if len(items) == 0 {
		return nil, domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeInvalidQuantity, "at least one ticket item is required")
	}

	merged := make(map[uuid.UUID]int32, len(items))
	var totalQty int32
	for _, it := range items {
		if it.TicketTypeID == uuid.Nil {
			return nil, domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeValidation, "ticket_type_id is required")
		}
		if it.Quantity <= 0 || it.Quantity > domain.MaxTicketsPerBuyerPerEvent {
			return nil, domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeInvalidQuantity, errMsgInvalidOrderQty)
		}
		if totalQty > domain.MaxTicketsPerBuyerPerEvent-it.Quantity {
			return nil, domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeInvalidQuantity, errMsgInvalidOrderQty)
		}
		merged[it.TicketTypeID] += it.Quantity
		totalQty += it.Quantity
	}
	if totalQty < 1 || totalQty > domain.MaxTicketsPerBuyerPerEvent {
		return nil, domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeInvalidQuantity, errMsgInvalidOrderQty)
	}

	deduped := make([]repository.CheckoutItemInput, 0, len(merged))
	for tid, qty := range merged {
		deduped = append(deduped, repository.CheckoutItemInput{TicketTypeID: tid, Quantity: qty})
	}
	slices.SortFunc(deduped, func(a, b repository.CheckoutItemInput) int {
		return bytes.Compare(a.TicketTypeID[:], b.TicketTypeID[:])
	})
	return deduped, nil
}
