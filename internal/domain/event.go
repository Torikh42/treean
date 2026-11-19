package domain

import (
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

const maxSafeTicketPriceIDR = math.MaxInt64 / int64(MaxTicketsPerBuyerPerEvent)

// Event represents an organizer-managed ticketing event.
type Event struct {
	ID          uuid.UUID    `json:"id"`
	OrganizerID uuid.UUID    `json:"organizer_id"`
	Title       string       `json:"title"`
	Description string       `json:"description"`
	Venue       string       `json:"venue"`
	EventDate   time.Time    `json:"event_date"`
	CreatedAt   time.Time    `json:"created_at"`
	TicketTypes []TicketType `json:"ticket_types"`
}

// TicketType represents a priced ticket tier (e.g. Reguler, VIP) with finite quota.
type TicketType struct {
	ID             uuid.UUID `json:"id"`
	EventID        uuid.UUID `json:"event_id"`
	Name           string    `json:"name"`
	PriceIDR       int64     `json:"price_idr"`
	TotalQuota     int32     `json:"total_quota"`
	RemainingQuota int32     `json:"remaining_quota"`
	CreatedAt      time.Time `json:"created_at"`
}

// Validate enforces invariants on a TicketType definition before persistence.
func (t TicketType) Validate() error {
	if strings.TrimSpace(t.Name) == "" {
		return NewDomainError(http.StatusUnprocessableEntity, ErrCodeValidation, "ticket type name is required")
	}
	if t.PriceIDR < 0 {
		return NewDomainError(http.StatusUnprocessableEntity, ErrCodeValidation, "price_idr must be non-negative")
	}
	if t.PriceIDR > maxSafeTicketPriceIDR {
		return NewDomainError(http.StatusUnprocessableEntity, ErrCodeValidation, "price_idr exceeds maximum safe order total")
	}
	if t.TotalQuota <= 0 {
		return NewDomainError(http.StatusUnprocessableEntity, ErrCodeValidation, "total_quota must be greater than zero")
	}
	if t.RemainingQuota < 0 || t.RemainingQuota > t.TotalQuota {
		return NewDomainError(http.StatusUnprocessableEntity, ErrCodeValidation, "remaining_quota must be between 0 and total_quota")
	}
	return nil
}
