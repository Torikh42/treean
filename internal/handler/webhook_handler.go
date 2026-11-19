package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/torikh42/treean/internal/service"
)

const paymentsTag = "Payments"

// PaymentWebhookBody defines the Midtrans-style signed webhook payload.
// All fields stay schema-optional so empty, tampered, or negative payloads reach
// PaymentService.ProcessWebhook for fail-closed verification and audit persistence.
type PaymentWebhookBody struct {
	TransactionID     string `json:"transaction_id" required:"false"`
	OrderID           string `json:"order_id" required:"false"`
	StatusCode        string `json:"status_code" required:"false" example:"200"`
	GrossAmount       int64  `json:"gross_amount" required:"false" example:"300000"`
	TransactionStatus string `json:"transaction_status" required:"false" example:"settlement"`
	SignatureKey      string `json:"signature_key" required:"false"`
}

func registerWebhookRoutes(api huma.API, logger *slog.Logger, paymentSvc *service.PaymentService) {
	registerPaymentWebhookRoute(api, logger, paymentSvc)
	registerDemoPayRoute(api, logger, paymentSvc)
}

func registerPaymentWebhookRoute(api huma.API, logger *slog.Logger, paymentSvc *service.PaymentService) {
	huma.Register(api, huma.Operation{
		OperationID: "payment-webhook",
		Method:      http.MethodPost,
		Path:        "/api/v1/webhooks/payment",
		Summary:     "Idempotent SHA-512 signed payment webhook (handles duplicates and post-expiry late captures)",
		Tags:        []string{paymentsTag},
	}, func(ctx context.Context, in *struct{ Body *PaymentWebhookBody }) (*struct{ Body *service.WebhookResult }, error) {
		var body PaymentWebhookBody
		if in != nil && in.Body != nil {
			body = *in.Body
		}
		raw, marshalErr := json.Marshal(body)
		if marshalErr != nil {
			return nil, mapServiceError(ctx, logger, fmt.Errorf("marshaling webhook payload: %w", marshalErr))
		}
		res, err := paymentSvc.ProcessWebhook(ctx, service.WebhookInput{
			TransactionID:     body.TransactionID,
			OrderID:           body.OrderID,
			StatusCode:        body.StatusCode,
			GrossAmount:       body.GrossAmount,
			TransactionStatus: body.TransactionStatus,
			SignatureKey:      body.SignatureKey,
			RawPayload:        raw,
		})
		if err != nil {
			return nil, mapServiceError(ctx, logger, err)
		}
		return &struct{ Body *service.WebhookResult }{Body: res}, nil
	})
}

func registerDemoPayRoute(api huma.API, logger *slog.Logger, paymentSvc *service.PaymentService) {
	huma.Register(api, huma.Operation{
		OperationID:        "pay-order-demo",
		Method:             http.MethodPost,
		Path:               "/api/v1/orders/{id}/pay",
		Summary:            "Simulate immediate payment for an order (generates valid signature server-side)",
		Tags:               []string{paymentsTag},
		Security:           bearerSecurity,
		SkipValidateParams: true,
	}, func(ctx context.Context, in *struct {
		ID string `path:"id" format:"uuid"`
	}) (*struct{ Body *service.WebhookResult }, error) {
		actor, err := requireActor(ctx)
		if err != nil {
			return nil, mapServiceError(ctx, logger, err)
		}
		oid, err := parseNonNilUUID(in.ID, fieldOrder)
		if err != nil {
			return nil, mapServiceError(ctx, logger, err)
		}
		res, err := paymentSvc.PayOrderForDemo(ctx, actor, oid)
		if err != nil {
			return nil, mapServiceError(ctx, logger, err)
		}
		return &struct{ Body *service.WebhookResult }{Body: res}, nil
	})
}
