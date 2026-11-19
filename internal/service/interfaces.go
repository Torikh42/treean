// Package service implements core business use cases over consumer-defined repository interfaces.
package service

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/torikh42/treean/internal/domain"
	"github.com/torikh42/treean/internal/repository"
)

const (
	lazyExpiryActorType  = "lazy-check"
	lazyExpiryBatchLimit = 50
)

// UserRepository defines persistence operations required by AuthService and auth middleware.
type UserRepository interface {
	Create(ctx context.Context, u *domain.User) error
	FindByEmail(ctx context.Context, email string) (*domain.User, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.User, error)
	EnsureDemoUser(ctx context.Context, id uuid.UUID, role domain.Role) error
}

// EventRepository defines persistence operations required by EventService, OrderService, PaymentService, and DemoService.
type EventRepository interface {
	CreateWithTiers(ctx context.Context, ev *domain.Event) error
	ListAll(ctx context.Context) ([]domain.Event, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Event, error)
}

// OrderRepository defines persistence operations required by OrderService, EventService, PaymentService, and ExpirySweeper.
type OrderRepository interface {
	CreateOrder(ctx context.Context, eventID, buyerID uuid.UUID, items []repository.CheckoutItemInput, now time.Time) (*domain.Order, error)
	CancelOrder(ctx context.Context, orderID, buyerID uuid.UUID, now time.Time) (*domain.Order, error)
	ConfirmPayment(ctx context.Context, orderID uuid.UUID, grossAmount int64, now time.Time) (*repository.PaymentConfirmResult, error)
	FailPayment(ctx context.Context, orderID uuid.UUID, reason string, now time.Time) (domain.WebhookOutcome, error)
	ExpireDueOrders(ctx context.Context, eventID *uuid.UUID, limit int, actorType string, now time.Time) (int, error)
	FindByID(ctx context.Context, orderID uuid.UUID) (*domain.Order, error)
	ListByBuyer(ctx context.Context, buyerID uuid.UUID) ([]domain.Order, error)
}

// WebhookRepository logs inbound payment callbacks outside main transactions.
type WebhookRepository interface {
	LogDelivery(ctx context.Context, w *domain.PaymentWebhook) error
}

func validateServiceCall(ctx context.Context, op string, ready bool) error {
	if !ready {
		return errors.New(op + ": service or repository is nil")
	}
	if ctx == nil {
		return errors.New(op + ": context is required")
	}
	return nil
}

func validateAuthenticatedActor(actor domain.Actor) error {
	if actor.ID == uuid.Nil {
		return domain.NewDomainError(http.StatusUnauthorized, domain.ErrCodeUnauthorized, "authentication is required")
	}
	switch actor.Role {
	case domain.RoleOrganizer, domain.RoleBuyer:
		return nil
	case "":
		return domain.NewDomainError(http.StatusUnauthorized, domain.ErrCodeUnauthorized, "actor role is required")
	default:
		return domain.NewDomainError(http.StatusForbidden, domain.ErrCodeForbidden, "invalid actor role")
	}
}

func validateUserRole(role domain.Role) error {
	if role != domain.RoleOrganizer && role != domain.RoleBuyer {
		return domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeValidation, "role must be ORGANIZER or BUYER")
	}
	return nil
}

func authorizeOrderAccess(
	ctx context.Context,
	events EventRepository,
	actor domain.Actor,
	order *domain.Order,
	forbiddenMsg string,
) error {
	if order == nil {
		return errors.New("authorizing order access: order is nil")
	}
	if events == nil {
		return errors.New("authorizing order access: event repository is nil")
	}
	if order.BuyerID == actor.ID {
		return nil
	}
	if actor.Role == domain.RoleOrganizer {
		ev, err := events.FindByID(ctx, order.EventID)
		if err != nil {
			return err
		}
		if ev != nil && ev.OrganizerID == actor.ID {
			return nil
		}
	}
	return domain.NewDomainError(http.StatusForbidden, domain.ErrCodeForbidden, forbiddenMsg)
}
