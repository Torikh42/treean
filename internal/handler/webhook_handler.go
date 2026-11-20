package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/torikh42/treean/internal/service"
)

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

// WebhookHandler handles inbound SHA-512 payment callbacks and demo payment settlement.
type WebhookHandler struct {
	svc    *service.PaymentService
	logger *slog.Logger
}

// NewWebhookHandler constructs a WebhookHandler.
func NewWebhookHandler(svc *service.PaymentService, logger *slog.Logger) *WebhookHandler {
	return &WebhookHandler{svc: svc, logger: SafeLogger(logger)}
}

// HandlePaymentWebhook verifies payment signatures, issues tickets idempotently, and audits callbacks.
func (h *WebhookHandler) HandlePaymentWebhook(ctx context.Context, in *struct{ Body *PaymentWebhookBody }) (*struct{ Body *service.WebhookResult }, error) {
	var body PaymentWebhookBody
	if in != nil && in.Body != nil {
		body = *in.Body
	}
	raw, marshalErr := json.Marshal(body)
	if marshalErr != nil {
		return nil, MapServiceError(ctx, h.logger, fmt.Errorf("marshaling webhook payload: %w", marshalErr))
	}
	res, err := h.svc.ProcessWebhook(ctx, service.WebhookInput{
		TransactionID:     body.TransactionID,
		OrderID:           body.OrderID,
		StatusCode:        body.StatusCode,
		GrossAmount:       body.GrossAmount,
		TransactionStatus: body.TransactionStatus,
		SignatureKey:      body.SignatureKey,
		RawPayload:        raw,
	})
	if err != nil {
		return nil, MapServiceError(ctx, h.logger, err)
	}
	return &struct{ Body *service.WebhookResult }{Body: res}, nil
}

// PayOrderDemo simulates immediate payment for an order using a server-signed webhook payload.
func (h *WebhookHandler) PayOrderDemo(ctx context.Context, in *struct {
	ID string `path:"id" format:"uuid"`
}) (*struct{ Body *service.WebhookResult }, error) {
	actor, err := RequireActor(ctx)
	if err != nil {
		return nil, MapServiceError(ctx, h.logger, err)
	}
	oid, err := ParseNonNilUUID(in.ID, fieldOrder)
	if err != nil {
		return nil, MapServiceError(ctx, h.logger, err)
	}
	res, err := h.svc.PayOrderForDemo(ctx, actor, oid)
	if err != nil {
		return nil, MapServiceError(ctx, h.logger, err)
	}
	return &struct{ Body *service.WebhookResult }{Body: res}, nil
}
