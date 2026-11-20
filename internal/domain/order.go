package domain

import (
	"time"

	"github.com/google/uuid"
)

const (
	// MaxTicketsPerBuyerPerEvent caps cumulative tickets held or paid per buyer on a single event.
	MaxTicketsPerBuyerPerEvent int32 = 4

	// OrderHoldDuration sets the 15-minute reservation window before unpaid orders expire.
	OrderHoldDuration = 15 * time.Minute
)

// OrderStatus encodes valid order lifecycle states.
type OrderStatus string

// OrderStatusWaitingPayment and related constants enumerate valid order lifecycle states.
const (
	OrderStatusWaitingPayment OrderStatus = "WAITING_PAYMENT"
	OrderStatusPaid           OrderStatus = "PAID"
	OrderStatusCancelled      OrderStatus = "CANCELLED"
	OrderStatusExpired        OrderStatus = "EXPIRED"
)

// Order represents a ticket reservation or completed purchase.
type Order struct {
	ID              uuid.UUID   `json:"id"`
	InvoiceNumber   string      `json:"invoice_number"`
	EventID         uuid.UUID   `json:"event_id"`
	BuyerID         uuid.UUID   `json:"buyer_id"`
	IdempotencyKey  *string     `json:"idempotency_key,omitempty"`
	TotalTickets    int32       `json:"total_tickets"`
	TotalAmountIDR  int64       `json:"total_amount_idr"`
	Status          OrderStatus `json:"status"`
	ExpiresAt       time.Time   `json:"expires_at"`
	PaidAt          *time.Time  `json:"paid_at,omitempty"`
	CancelledAt     *time.Time  `json:"cancelled_at,omitempty"`
	ExpiredAt       *time.Time  `json:"expired_at,omitempty"`
	CancelledReason *string     `json:"cancelled_reason,omitempty"`
	CreatedAt       time.Time   `json:"created_at"`
	Items           []OrderItem `json:"items,omitempty"`
	Tickets         []Ticket    `json:"tickets,omitempty"`
	Logs            []OrderLog  `json:"logs,omitempty"`
}

// OrderItem captures a single ticket tier line inside an order.
type OrderItem struct {
	ID             uuid.UUID `json:"id"`
	OrderID        uuid.UUID `json:"order_id"`
	TicketTypeID   uuid.UUID `json:"ticket_type_id"`
	TicketTypeName string    `json:"ticket_type_name,omitempty"`
	Quantity       int32     `json:"quantity"`
	UnitPriceIDR   int64     `json:"unit_price_idr"`
	SubtotalIDR    int64     `json:"subtotal_idr"`
}

// OrderLog records an immutable state transition entry for auditability.
type OrderLog struct {
	ID         uuid.UUID  `json:"id"`
	OrderID    uuid.UUID  `json:"order_id"`
	FromStatus *string    `json:"from_status,omitempty"`
	ToStatus   string     `json:"to_status"`
	ActorID    *uuid.UUID `json:"actor_id,omitempty"`
	ActorType  string     `json:"actor_type"`
	Note       string     `json:"note"`
	CreatedAt  time.Time  `json:"created_at"`
}

// CanPay validates whether the order may transition to PAID or replay idempotently.
// An already-PAID order returns nil so callers handle it as an idempotent replay.
func (o *Order) CanPay(now time.Time) error {
	if o == nil {
		return NewInvalidOrderStateError("order cannot be nil")
	}
	switch o.Status {
	case OrderStatusPaid:
		return nil
	case OrderStatusExpired:
		return NewOrderExpiredError("order has already expired; payment cannot issue tickets")
	case OrderStatusCancelled:
		return NewInvalidOrderStateError("order was cancelled and cannot be paid")
	case OrderStatusWaitingPayment:
		return o.checkWaitingWindow(now, "order reservation window has expired")
	default:
		return NewInvalidOrderStateError("invalid order status")
	}
}

// CanCancel validates whether the order may be cancelled by the buyer before payment.
func (o *Order) CanCancel(now time.Time) error {
	if o == nil {
		return NewInvalidOrderStateError("order cannot be nil")
	}
	switch o.Status {
	case OrderStatusWaitingPayment:
		return o.checkWaitingWindow(now, "order has already expired")
	case OrderStatusPaid:
		return NewInvalidOrderStateError("paid orders cannot be cancelled")
	case OrderStatusCancelled:
		return NewInvalidOrderStateError("order is already cancelled")
	case OrderStatusExpired:
		return NewOrderExpiredError("order has already expired")
	default:
		return NewInvalidOrderStateError("invalid order status")
	}
}

func (o *Order) checkWaitingWindow(now time.Time, expiredMsg string) error {
	if now.IsZero() {
		return NewInvalidOrderStateError("evaluation time cannot be zero")
	}
	if o.ExpiresAt.IsZero() {
		return NewInvalidOrderStateError("order expires_at cannot be zero")
	}
	if now.After(o.ExpiresAt) {
		return NewOrderExpiredError(expiredMsg)
	}
	return nil
}
