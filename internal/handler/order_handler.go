package handler

import (
	"context"
	"log/slog"

	"github.com/torikh42/treean/internal/domain"
	"github.com/torikh42/treean/internal/repository"
	"github.com/torikh42/treean/internal/service"
)

// OrderItemBody specifies one ticket tier and quantity inside an order creation payload.
type OrderItemBody struct {
	TicketTypeID string `json:"ticket_type_id" format:"uuid"`
	Quantity     int32  `json:"quantity" minimum:"1" maximum:"4"`
}

// CreateOrderBody defines the JSON request body for creating a ticket reservation.
type CreateOrderBody struct {
	EventID string          `json:"event_id" format:"uuid"`
	Items   []OrderItemBody `json:"items" minItems:"1" maxItems:"4" doc:"1 to 4 total tickets across tiers"`
}

// CreateOrderInput captures checkout body plus optional Idempotency-Key header.
type CreateOrderInput struct {
	IdempotencyKey string `header:"Idempotency-Key" maxLength:"128" doc:"Optional unique key preventing duplicate order creation on retries"`
	Body           CreateOrderBody
}

// CreateOrderOutput wraps the reserved order response with optional replay indicator header.
type CreateOrderOutput struct {
	IdempotentReplay string `header:"Idempotent-Replay" doc:"Set to true when returning a previously created order for a matching Idempotency-Key"`
	Body             *domain.Order
}

// OrderHandler handles ticket reservation, cancellation, and order lookup endpoints.
type OrderHandler struct {
	svc    *service.OrderService
	logger *slog.Logger
}

// NewOrderHandler constructs an OrderHandler.
func NewOrderHandler(svc *service.OrderService, logger *slog.Logger) *OrderHandler {
	return &OrderHandler{svc: svc, logger: SafeLogger(logger)}
}

// Create reserves 1 to 4 tickets for 15 minutes in WAITING_PAYMENT status.
func (h *OrderHandler) Create(ctx context.Context, in *CreateOrderInput) (*CreateOrderOutput, error) {
	actor, err := RequireActor(ctx)
	if err != nil {
		return nil, MapServiceError(ctx, h.logger, err)
	}
	eventID, err := ParseNonNilUUID(in.Body.EventID, "event_id")
	if err != nil {
		return nil, MapServiceError(ctx, h.logger, err)
	}

	items := make([]repository.CheckoutItemInput, len(in.Body.Items))
	for i, it := range in.Body.Items {
		tid, parseErr := ParseNonNilUUID(it.TicketTypeID, "ticket_type_id")
		if parseErr != nil {
			return nil, MapServiceError(ctx, h.logger, parseErr)
		}
		items[i] = repository.CheckoutItemInput{TicketTypeID: tid, Quantity: it.Quantity}
	}

	res, err := h.svc.CreateOrderWithResult(ctx, actor, service.CreateOrderInput{
		EventID:        eventID,
		Items:          items,
		IdempotencyKey: in.IdempotencyKey,
	})
	if err != nil {
		return nil, MapServiceError(ctx, h.logger, err)
	}
	out := &CreateOrderOutput{Body: res.Order}
	if res.IdempotentReplay {
		out.IdempotentReplay = "true"
	}
	return out, nil
}

// Cancel cancels an unpaid order and immediately restores reserved ticket quota.
func (h *OrderHandler) Cancel(ctx context.Context, in *struct {
	ID string `path:"id" format:"uuid"`
}) (*struct{ Body *domain.Order }, error) {
	actor, err := RequireActor(ctx)
	if err != nil {
		return nil, MapServiceError(ctx, h.logger, err)
	}
	oid, err := ParseNonNilUUID(in.ID, fieldOrder)
	if err != nil {
		return nil, MapServiceError(ctx, h.logger, err)
	}
	order, err := h.svc.CancelOrder(ctx, actor, oid)
	if err != nil {
		return nil, MapServiceError(ctx, h.logger, err)
	}
	return &struct{ Body *domain.Order }{Body: order}, nil
}

// List returns the authenticated buyer's order history and issued tickets.
func (h *OrderHandler) List(ctx context.Context, _ *struct{}) (*struct{ Body []domain.Order }, error) {
	actor, err := RequireActor(ctx)
	if err != nil {
		return nil, MapServiceError(ctx, h.logger, err)
	}
	orders, err := h.svc.ListOrders(ctx, actor)
	if err != nil {
		return nil, MapServiceError(ctx, h.logger, err)
	}
	return &struct{ Body []domain.Order }{Body: orders}, nil
}

// Get returns a single order with items, state transition logs, and issued ticket codes.
func (h *OrderHandler) Get(ctx context.Context, in *struct {
	ID string `path:"id" format:"uuid"`
}) (*struct{ Body *domain.Order }, error) {
	actor, err := RequireActor(ctx)
	if err != nil {
		return nil, MapServiceError(ctx, h.logger, err)
	}
	oid, err := ParseNonNilUUID(in.ID, fieldOrder)
	if err != nil {
		return nil, MapServiceError(ctx, h.logger, err)
	}
	order, err := h.svc.GetOrder(ctx, actor, oid)
	if err != nil {
		return nil, MapServiceError(ctx, h.logger, err)
	}
	return &struct{ Body *domain.Order }{Body: order}, nil
}
