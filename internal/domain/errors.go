// Package domain defines pure ticketing entities, state transitions, and business errors.
package domain

import "net/http"

// ErrCodeQuotaExhausted and related constants define machine-readable domain error codes shared across HTTP responses and tests.
const (
	ErrCodeQuotaExhausted        = "QUOTA_EXHAUSTED"
	ErrCodeBuyerLimitExceeded    = "BUYER_LIMIT_EXCEEDED"
	ErrCodeInvalidQuantity       = "INVALID_QUANTITY"
	ErrCodeInvalidOrderState     = "INVALID_ORDER_STATE"
	ErrCodeOrderExpired          = "ORDER_EXPIRED"
	ErrCodePaymentAmountMismatch = "PAYMENT_AMOUNT_MISMATCH"
	ErrCodeUnauthorizedWebhook   = "UNAUTHORIZED_WEBHOOK"
	ErrCodeEventNotFound         = "EVENT_NOT_FOUND"
	ErrCodeTicketTypeNotFound    = "TICKET_TYPE_NOT_FOUND"
	ErrCodeOrderNotFound         = "ORDER_NOT_FOUND"
	ErrCodeForbidden             = "FORBIDDEN"
	ErrCodeUnauthorized          = "UNAUTHORIZED"
	ErrCodeEmailAlreadyExists    = "EMAIL_ALREADY_EXISTS"
	ErrCodeInvalidCredentials    = "INVALID_CREDENTIALS" //nolint:gosec // G101 false positive: public machine error code constant, not a secret.
	ErrCodeValidation            = "VALIDATION_ERROR"
)

// DomainError carries an HTTP status code, stable machine code, and client-safe message.
type DomainError struct { //nolint:revive // Explicit DomainError name prevents shadowing Go's built-in error interface.
	Code       string
	HTTPStatus int
	Message    string
}

// Error implements the standard error interface.
func (e *DomainError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Code != "" {
		return e.Code
	}
	return "domain error"
}

// NewDomainError creates a structured DomainError.
func NewDomainError(status int, code, msg string) *DomainError {
	return &DomainError{
		Code:       code,
		HTTPStatus: status,
		Message:    msg,
	}
}

// NewQuotaExhaustedError creates a conflict error when ticket quota is insufficient.
func NewQuotaExhaustedError(msg string) *DomainError {
	return NewDomainError(http.StatusConflict, ErrCodeQuotaExhausted, msg)
}

// NewBuyerLimitExceededError creates a conflict error when a buyer exceeds the per-event ticket cap.
func NewBuyerLimitExceededError(msg string) *DomainError {
	return NewDomainError(http.StatusConflict, ErrCodeBuyerLimitExceeded, msg)
}

// NewOrderExpiredError creates a conflict error when an order has passed its payment deadline.
func NewOrderExpiredError(msg string) *DomainError {
	return NewDomainError(http.StatusConflict, ErrCodeOrderExpired, msg)
}

// NewInvalidOrderStateError creates a conflict error when an order state transition is disallowed.
func NewInvalidOrderStateError(msg string) *DomainError {
	return NewDomainError(http.StatusConflict, ErrCodeInvalidOrderState, msg)
}

// NewPaymentAmountMismatchError creates a bad request error when payment amount differs from order total.
func NewPaymentAmountMismatchError(msg string) *DomainError {
	return NewDomainError(http.StatusBadRequest, ErrCodePaymentAmountMismatch, msg)
}

// NewUnauthorizedWebhookError creates an unauthorized error when webhook authentication fails.
func NewUnauthorizedWebhookError(msg string) *DomainError {
	return NewDomainError(http.StatusUnauthorized, ErrCodeUnauthorizedWebhook, msg)
}
