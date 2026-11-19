package domain

import (
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// WebhookOutcome classifies how a payment webhook delivery was handled.
type WebhookOutcome string

// OutcomeProcessedPaid and related constants classify inbound payment webhook results.
const (
	OutcomeProcessedPaid       WebhookOutcome = "PROCESSED_PAID"
	OutcomeIgnoredDuplicate    WebhookOutcome = "IGNORED_DUPLICATE"
	OutcomeRejectedLateExpired WebhookOutcome = "REJECTED_LATE_EXPIRED"
	OutcomeProcessedFailed     WebhookOutcome = "PROCESSED_FAILED"
	OutcomeAmountMismatch      WebhookOutcome = "AMOUNT_MISMATCH"
	OutcomeSignatureInvalid    WebhookOutcome = "SIGNATURE_INVALID"
	OutcomeOrderNotFound       WebhookOutcome = "ORDER_NOT_FOUND"
	OutcomeIgnoredUnknownEvent WebhookOutcome = "IGNORED_UNKNOWN_EVENT"
)

// Ticket represents one issued admission pass tied uniquely to an order item seat sequence.
type Ticket struct {
	ID           uuid.UUID `json:"id"`
	TicketCode   string    `json:"ticket_code"`
	OrderID      uuid.UUID `json:"order_id"`
	OrderItemID  uuid.UUID `json:"order_item_id"`
	EventID      uuid.UUID `json:"event_id"`
	TicketTypeID uuid.UUID `json:"ticket_type_id"`
	OwnerID      uuid.UUID `json:"owner_id"`
	SeatSequence int32     `json:"seat_sequence"`
	IssuedAt     time.Time `json:"issued_at"`
}

// PaymentWebhook records every webhook attempt outside transaction rollbacks.
type PaymentWebhook struct {
	ID                uuid.UUID      `json:"id"`
	TransactionID     string         `json:"transaction_id"`
	RawOrderID        string         `json:"raw_order_id"`
	ParsedOrderID     *uuid.UUID     `json:"parsed_order_id,omitempty"`
	StatusCode        string         `json:"status_code"`
	GrossAmount       int64          `json:"gross_amount"`
	TransactionStatus string         `json:"transaction_status"`
	Outcome           WebhookOutcome `json:"outcome"`
	RequiresRefund    bool           `json:"requires_refund"`
	RawPayload        []byte         `json:"raw_payload"`
	ReceivedAt        time.Time      `json:"received_at"`
}

// ComputeSHA512Signature returns lowercase hex SHA-512 of orderID + statusCode + grossAmount + secret.
func ComputeSHA512Signature(orderID, statusCode string, grossAmount int64, secret string) string {
	raw := orderID + statusCode + strconv.FormatInt(grossAmount, 10) + secret
	sum := sha512.Sum512([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// VerifySHA512Signature verifies webhook authenticity in constant time and fails closed on empty secret.
func VerifySHA512Signature(orderID, statusCode string, grossAmount int64, secret, providedSig string) bool {
	normalized := strings.ToLower(strings.TrimSpace(providedSig))
	if secret == "" || normalized == "" {
		return false
	}
	expected := ComputeSHA512Signature(orderID, statusCode, grossAmount, secret)
	return hmac.Equal([]byte(expected), []byte(normalized))
}
