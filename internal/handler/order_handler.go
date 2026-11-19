package handler

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/torikh42/treean/internal/domain"
	"github.com/torikh42/treean/internal/repository"
	"github.com/torikh42/treean/internal/service"
)

const ordersTag = "Orders"

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

func registerOrderRoutes(api huma.API, logger *slog.Logger, orderSvc *service.OrderService) {
	registerCreateOrderRoute(api, logger, orderSvc)
	registerCancelOrderRoute(api, logger, orderSvc)
	registerListOrdersRoute(api, logger, orderSvc)
	registerGetOrderRoute(api, logger, orderSvc)
}

func registerCreateOrderRoute(api huma.API, logger *slog.Logger, orderSvc *service.OrderService) {
	huma.Register(api, huma.Operation{
		OperationID:   "create-order",
		Method:        http.MethodPost,
		Path:          "/api/v1/orders",
		Summary:       "Reserve 1-4 tickets for 15 minutes (WAITING_PAYMENT)",
		Tags:          []string{ordersTag},
		DefaultStatus: http.StatusCreated,
		Security:      bearerSecurity,
	}, func(ctx context.Context, in *struct{ Body CreateOrderBody }) (*struct{ Body *domain.Order }, error) {
		actor, err := requireActor(ctx)
		if err != nil {
			return nil, mapServiceError(ctx, logger, err)
		}
		eventID, err := parseNonNilUUID(in.Body.EventID, "event_id")
		if err != nil {
			return nil, mapServiceError(ctx, logger, err)
		}

		items := make([]repository.CheckoutItemInput, len(in.Body.Items))
		for i, it := range in.Body.Items {
			tid, parseErr := parseNonNilUUID(it.TicketTypeID, "ticket_type_id")
			if parseErr != nil {
				return nil, mapServiceError(ctx, logger, parseErr)
			}
			items[i] = repository.CheckoutItemInput{TicketTypeID: tid, Quantity: it.Quantity}
		}

		order, err := orderSvc.CreateOrder(ctx, actor, service.CreateOrderInput{EventID: eventID, Items: items})
		if err != nil {
			return nil, mapServiceError(ctx, logger, err)
		}
		return &struct{ Body *domain.Order }{Body: order}, nil
	})
}

func registerCancelOrderRoute(api huma.API, logger *slog.Logger, orderSvc *service.OrderService) {
	huma.Register(api, huma.Operation{
		OperationID:        "cancel-order",
		Method:             http.MethodPost,
		Path:               "/api/v1/orders/{id}/cancel",
		Summary:            "Cancel an unpaid order and immediately restore quota",
		Tags:               []string{ordersTag},
		Security:           bearerSecurity,
		SkipValidateParams: true,
	}, func(ctx context.Context, in *struct {
		ID string `path:"id" format:"uuid"`
	}) (*struct{ Body *domain.Order }, error) {
		actor, err := requireActor(ctx)
		if err != nil {
			return nil, mapServiceError(ctx, logger, err)
		}
		oid, err := parseNonNilUUID(in.ID, fieldOrder)
		if err != nil {
			return nil, mapServiceError(ctx, logger, err)
		}
		order, err := orderSvc.CancelOrder(ctx, actor, oid)
		if err != nil {
			return nil, mapServiceError(ctx, logger, err)
		}
		return &struct{ Body *domain.Order }{Body: order}, nil
	})
}

func registerListOrdersRoute(api huma.API, logger *slog.Logger, orderSvc *service.OrderService) {
	huma.Register(api, huma.Operation{
		OperationID: "list-my-orders",
		Method:      http.MethodGet,
		Path:        "/api/v1/orders",
		Summary:     "List authenticated buyer's order history and issued tickets",
		Tags:        []string{ordersTag},
		Security:    bearerSecurity,
	}, func(ctx context.Context, _ *struct{}) (*struct{ Body []domain.Order }, error) {
		actor, err := requireActor(ctx)
		if err != nil {
			return nil, mapServiceError(ctx, logger, err)
		}
		orders, err := orderSvc.ListOrders(ctx, actor)
		if err != nil {
			return nil, mapServiceError(ctx, logger, err)
		}
		return &struct{ Body []domain.Order }{Body: orders}, nil
	})
}

func registerGetOrderRoute(api huma.API, logger *slog.Logger, orderSvc *service.OrderService) {
	huma.Register(api, huma.Operation{
		OperationID:        "get-order",
		Method:             http.MethodGet,
		Path:               "/api/v1/orders/{id}",
		Summary:            "Get single order detail with items, status transition logs, and ticket codes",
		Tags:               []string{ordersTag},
		Security:           bearerSecurity,
		SkipValidateParams: true,
	}, func(ctx context.Context, in *struct {
		ID string `path:"id" format:"uuid"`
	}) (*struct{ Body *domain.Order }, error) {
		actor, err := requireActor(ctx)
		if err != nil {
			return nil, mapServiceError(ctx, logger, err)
		}
		oid, err := parseNonNilUUID(in.ID, fieldOrder)
		if err != nil {
			return nil, mapServiceError(ctx, logger, err)
		}
		order, err := orderSvc.GetOrder(ctx, actor, oid)
		if err != nil {
			return nil, mapServiceError(ctx, logger, err)
		}
		return &struct{ Body *domain.Order }{Body: order}, nil
	})
}
