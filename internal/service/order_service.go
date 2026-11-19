package service

import (
	"bytes"
	"context"
	"net/http"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/torikh42/treean/internal/domain"
	"github.com/torikh42/treean/internal/repository"
)

const errMsgInvalidOrderQty = "order total_tickets must be between 1 and 4"

// CreateOrderInput represents a buyer's multi-item checkout request.
type CreateOrderInput struct {
	EventID uuid.UUID
	Items   []repository.CheckoutItemInput
}

// OrderService orchestrates ticket booking, cancellation, and history lookup.
type OrderService struct {
	orders OrderRepository
	events EventRepository
}

// NewOrderService constructs an OrderService.
func NewOrderService(orders OrderRepository, events EventRepository) *OrderService {
	return &OrderService{orders: orders, events: events}
}

// CreateOrder validates total quantity (1..4) and delegates atomic dual-locked creation to repository.
func (s *OrderService) CreateOrder(ctx context.Context, actor domain.Actor, in CreateOrderInput) (*domain.Order, error) {
	if err := validateServiceCall(ctx, "creating order", s != nil && s.orders != nil); err != nil {
		return nil, err
	}
	if err := validateAuthenticatedActor(actor); err != nil {
		return nil, err
	}
	if in.EventID == uuid.Nil {
		return nil, domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeValidation, "event_id is required")
	}

	deduped, err := normalizeOrderItems(in.Items)
	if err != nil {
		return nil, err
	}
	return s.orders.CreateOrder(ctx, in.EventID, actor.ID, deduped, time.Now().UTC())
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
	return s.orders.CancelOrder(ctx, orderID, actor.ID, time.Now().UTC())
}

// ListOrders returns order history and tickets belonging to the calling buyer.
func (s *OrderService) ListOrders(ctx context.Context, actor domain.Actor) ([]domain.Order, error) {
	if err := validateServiceCall(ctx, "listing orders", s != nil && s.orders != nil); err != nil {
		return nil, err
	}
	if err := validateAuthenticatedActor(actor); err != nil {
		return nil, err
	}
	if _, err := s.orders.ExpireDueOrders(ctx, nil, lazyExpiryBatchLimit, lazyExpiryActorType, time.Now().UTC()); err != nil {
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
