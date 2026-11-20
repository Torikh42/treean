package routes

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/torikh42/treean/internal/handler"
)

const paymentsTag = "Payments"

func registerWebhookRoutes(api huma.API, h *handler.WebhookHandler) {
	if h == nil {
		return
	}

	huma.Register(api, huma.Operation{
		OperationID: "payment-webhook",
		Method:      http.MethodPost,
		Path:        "/api/v1/webhooks/payment",
		Summary:     "Idempotent SHA-512 signed payment webhook (handles duplicates and post-expiry late captures)",
		Tags:        []string{paymentsTag},
	}, h.HandlePaymentWebhook)

	huma.Register(api, huma.Operation{
		OperationID:        "pay-order-demo",
		Method:             http.MethodPost,
		Path:               "/api/v1/orders/{id}/pay",
		Summary:            "Simulate immediate payment for an order (generates valid signature server-side)",
		Tags:               []string{paymentsTag},
		Security:           bearerSecurity,
		SkipValidateParams: true,
	}, h.PayOrderDemo)
}
