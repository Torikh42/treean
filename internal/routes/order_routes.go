package routes

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/torikh42/treean/internal/handler"
)

const ordersTag = "Orders"

func registerOrderRoutes(api huma.API, h *handler.OrderHandler) {
	if h == nil {
		return
	}

	huma.Register(api, huma.Operation{
		OperationID:   "create-order",
		Method:        http.MethodPost,
		Path:          "/api/v1/orders",
		Summary:       "Reserve 1-4 tickets for 15 minutes (WAITING_PAYMENT)",
		Tags:          []string{ordersTag},
		DefaultStatus: http.StatusCreated,
		Security:      bearerSecurity,
	}, h.Create)

	huma.Register(api, huma.Operation{
		OperationID:        "cancel-order",
		Method:             http.MethodPost,
		Path:               "/api/v1/orders/{id}/cancel",
		Summary:            "Cancel an unpaid order and immediately restore quota",
		Tags:               []string{ordersTag},
		Security:           bearerSecurity,
		SkipValidateParams: true,
	}, h.Cancel)

	huma.Register(api, huma.Operation{
		OperationID: "list-my-orders",
		Method:      http.MethodGet,
		Path:        "/api/v1/orders",
		Summary:     "List authenticated buyer's order history and issued tickets",
		Tags:        []string{ordersTag},
		Security:    bearerSecurity,
	}, h.List)

	huma.Register(api, huma.Operation{
		OperationID:        "get-order",
		Method:             http.MethodGet,
		Path:               "/api/v1/orders/{id}",
		Summary:            "Get single order detail with items, status transition logs, and ticket codes",
		Tags:               []string{ordersTag},
		Security:           bearerSecurity,
		SkipValidateParams: true,
	}, h.Get)
}
