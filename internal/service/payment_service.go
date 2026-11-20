package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/torikh42/treean/internal/domain"
)

const (
	txStatusSettlement = "settlement"
	txStatusCapture    = "capture"
	demoStatusCodeOK   = "200"
)

// WebhookInput captures a Midtrans-style signed webhook payload.
type WebhookInput struct {
	TransactionID     string
	OrderID           string
	StatusCode        string
	GrossAmount       int64
	TransactionStatus string
	SignatureKey      string
	RawPayload        []byte
}

// WebhookResult describes the outcome returned to HTTP callers.
type WebhookResult struct {
	Outcome          domain.WebhookOutcome `json:"outcome"`
	IdempotentReplay bool                  `json:"idempotent_replay"`
	Order            *domain.Order         `json:"order,omitempty"`
}

// PaymentService handles signed payment webhooks and simulated demo checkout settlement.
type PaymentService struct {
	orders        OrderRepository
	webhooks      WebhookRepository
	events        EventRepository
	webhookSecret string
	cache         EventCatalogCache
}

// NewPaymentService constructs a PaymentService.
func NewPaymentService(orders OrderRepository, webhooks WebhookRepository, events EventRepository, webhookSecret string) *PaymentService {
	return &PaymentService{
		orders:        orders,
		webhooks:      webhooks,
		events:        events,
		webhookSecret: webhookSecret,
	}
}

// WithCache attaches an optional EventCatalogCache for invalidating event views on quota transitions.
func (s *PaymentService) WithCache(cache EventCatalogCache) *PaymentService {
	if s != nil {
		s.cache = cache
	}
	return s
}

// ProcessWebhook verifies signatures fail-closed, preserves audit logs outside rollbacks, and updates order state idempotently.
func (s *PaymentService) ProcessWebhook(ctx context.Context, in WebhookInput) (*WebhookResult, error) {
	if err := validateServiceCall(ctx, "processing webhook", s != nil && s.orders != nil && s.webhooks != nil); err != nil {
		return nil, err
	}

	audit, parsedOrderID, validUUID := buildWebhookAudit(in)
	if strings.TrimSpace(s.webhookSecret) == "" ||
		!domain.VerifySHA512Signature(in.OrderID, in.StatusCode, in.GrossAmount, s.webhookSecret, in.SignatureKey) {
		audit.Outcome = domain.OutcomeSignatureInvalid
		_ = s.webhooks.LogDelivery(ctx, audit)
		return nil, domain.NewUnauthorizedWebhookError("invalid or missing webhook signature")
	}

	if !validUUID {
		audit.Outcome = domain.OutcomeOrderNotFound
		_ = s.webhooks.LogDelivery(ctx, audit)
		return nil, domain.NewDomainError(http.StatusNotFound, domain.ErrCodeOrderNotFound, "order_id is not a valid UUID")
	}

	status := strings.ToLower(strings.TrimSpace(in.TransactionStatus))
	switch status {
	case txStatusSettlement, txStatusCapture:
		return s.handleSettlement(ctx, audit, parsedOrderID, in.GrossAmount)
	case "deny", "cancel", "expire":
		return s.handleFailureStatus(ctx, audit, parsedOrderID, status)
	default:
		audit.Outcome = domain.OutcomeIgnoredUnknownEvent
		if err := s.webhooks.LogDelivery(ctx, audit); err != nil {
			return nil, err
		}
		return &WebhookResult{Outcome: domain.OutcomeIgnoredUnknownEvent}, nil
	}
}

// PayOrderForDemo generates a valid signed payload for an unpaid order owned by actor and invokes ProcessWebhook.
func (s *PaymentService) PayOrderForDemo(ctx context.Context, actor domain.Actor, orderID uuid.UUID) (*WebhookResult, error) {
	order, err := s.loadAuthorizedDemoOrder(ctx, actor, orderID)
	if err != nil {
		return nil, err
	}

	in, err := buildDemoWebhookInput(order, s.webhookSecret)
	if err != nil {
		return nil, err
	}
	return s.ProcessWebhook(ctx, in)
}

func (s *PaymentService) loadAuthorizedDemoOrder(ctx context.Context, actor domain.Actor, orderID uuid.UUID) (*domain.Order, error) {
	if err := validateServiceCall(ctx, "paying order for demo", s != nil && s.orders != nil && s.webhooks != nil && s.events != nil); err != nil {
		return nil, err
	}
	if strings.TrimSpace(s.webhookSecret) == "" {
		return nil, domain.NewUnauthorizedWebhookError("webhook secret is not configured")
	}
	if err := validateAuthenticatedActor(actor); err != nil {
		return nil, err
	}
	if orderID == uuid.Nil {
		return nil, domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeValidation, "order_id is required")
	}

	order, err := s.orders.FindByID(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if order == nil {
		return nil, domain.NewDomainError(http.StatusNotFound, domain.ErrCodeOrderNotFound, "order not found")
	}
	if err := authorizeOrderAccess(ctx, s.events, actor, order, "cannot pay this order"); err != nil {
		return nil, err
	}
	return order, nil
}

func buildDemoWebhookInput(order *domain.Order, webhookSecret string) (WebhookInput, error) {
	txID := "demo-" + uuid.NewString()
	sig := domain.ComputeSHA512Signature(order.ID.String(), demoStatusCodeOK, order.TotalAmountIDR, webhookSecret)
	rawBytes, err := json.Marshal(map[string]any{
		"transaction_id":     txID,
		"order_id":           order.ID.String(),
		"status_code":        demoStatusCodeOK,
		"gross_amount":       order.TotalAmountIDR,
		"transaction_status": txStatusSettlement,
		"signature_key":      sig,
	})
	if err != nil {
		return WebhookInput{}, err
	}
	return WebhookInput{
		TransactionID:     txID,
		OrderID:           order.ID.String(),
		StatusCode:        demoStatusCodeOK,
		GrossAmount:       order.TotalAmountIDR,
		TransactionStatus: txStatusSettlement,
		SignatureKey:      sig,
		RawPayload:        rawBytes,
	}, nil
}

func (s *PaymentService) handleSettlement(
	ctx context.Context,
	audit *domain.PaymentWebhook,
	parsedOrderID uuid.UUID,
	grossAmount int64,
) (*WebhookResult, error) {
	if grossAmount < 0 {
		err := domain.NewPaymentAmountMismatchError("gross_amount must be non-negative")
		s.classifyAndLogFailure(ctx, audit, err, true)
		return nil, err
	}

	res, err := s.orders.ConfirmPayment(ctx, parsedOrderID, grossAmount, time.Now().UTC())
	if err != nil {
		s.classifyAndLogFailure(ctx, audit, err, true)
		s.invalidateOnLazyExpiry(ctx, parsedOrderID, err)
		return nil, err
	}
	if res == nil {
		nilErr := errors.New("confirming payment: repository returned nil result")
		s.classifyAndLogFailure(ctx, audit, nilErr, true)
		return nil, nilErr
	}

	if res.IdempotentReplay {
		audit.Outcome = domain.OutcomeIgnoredDuplicate
	} else {
		audit.Outcome = domain.OutcomeProcessedPaid
		if s.cache != nil && res.Order != nil {
			s.cache.InvalidateEvents(ctx, res.Order.EventID)
		}
	}
	if err := s.webhooks.LogDelivery(ctx, audit); err != nil {
		return nil, err
	}
	return &WebhookResult{
		Outcome:          audit.Outcome,
		IdempotentReplay: res.IdempotentReplay,
		Order:            res.Order,
	}, nil
}

func (s *PaymentService) handleFailureStatus(
	ctx context.Context,
	audit *domain.PaymentWebhook,
	parsedOrderID uuid.UUID,
	status string,
) (*WebhookResult, error) {
	nowUTC := time.Now().UTC()
	var outcome domain.WebhookOutcome
	var affectedEventID uuid.UUID
	var err error
	if aware, ok := s.orders.(EventAwareFailureRepository); ok {
		outcome, affectedEventID, err = aware.FailPaymentWithEvent(ctx, parsedOrderID, "gateway status: "+status, nowUTC)
	} else {
		outcome, err = s.orders.FailPayment(ctx, parsedOrderID, "gateway status: "+status, nowUTC)
	}
	if err != nil {
		s.classifyAndLogFailure(ctx, audit, err, false)
		return nil, err
	}
	if s.cache != nil && outcome == domain.OutcomeProcessedFailed {
		if affectedEventID != uuid.Nil {
			s.cache.InvalidateEvents(ctx, affectedEventID)
		} else {
			s.cache.InvalidateEvents(ctx)
		}
	}
	audit.Outcome = outcome
	if err := s.webhooks.LogDelivery(ctx, audit); err != nil {
		return nil, err
	}
	return &WebhookResult{Outcome: outcome}, nil
}

func (s *PaymentService) invalidateOnLazyExpiry(ctx context.Context, orderID uuid.UUID, err error) {
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

func (s *PaymentService) classifyAndLogFailure(
	ctx context.Context,
	audit *domain.PaymentWebhook,
	err error,
	isSettlement bool,
) {
	audit.Outcome = domain.OutcomeProcessedFailed
	if de, ok := errors.AsType[*domain.DomainError](err); ok {
		switch de.Code {
		case domain.ErrCodeOrderExpired, domain.ErrCodeInvalidOrderState:
			if isSettlement {
				audit.Outcome = domain.OutcomeRejectedLateExpired
				audit.RequiresRefund = true
			}
		case domain.ErrCodePaymentAmountMismatch:
			audit.Outcome = domain.OutcomeAmountMismatch
		case domain.ErrCodeOrderNotFound:
			audit.Outcome = domain.OutcomeOrderNotFound
		}
	}
	_ = s.webhooks.LogDelivery(ctx, audit)
}

func buildWebhookAudit(in WebhookInput) (*domain.PaymentWebhook, uuid.UUID, bool) {
	rawPayload := in.RawPayload
	if len(bytes.TrimSpace(rawPayload)) == 0 {
		rawPayload = []byte("{}")
	}

	parsedOrderID, parseErr := uuid.Parse(strings.TrimSpace(in.OrderID))
	validUUID := parseErr == nil && parsedOrderID != uuid.Nil
	var parsedPtr *uuid.UUID
	if validUUID {
		parsedPtr = &parsedOrderID
	}

	return &domain.PaymentWebhook{
		ID:                uuid.New(),
		TransactionID:     in.TransactionID,
		RawOrderID:        in.OrderID,
		ParsedOrderID:     parsedPtr,
		StatusCode:        in.StatusCode,
		GrossAmount:       in.GrossAmount,
		TransactionStatus: in.TransactionStatus,
		RawPayload:        rawPayload,
	}, parsedOrderID, validUUID
}
