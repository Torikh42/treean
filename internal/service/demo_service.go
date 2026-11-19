package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/torikh42/treean/internal/domain"
	"github.com/torikh42/treean/internal/repository"
)

const (
	defaultFlashSaleRequests       = 200
	defaultFlashSaleQuota    int32 = 100
	maxFlashSaleRequests           = 500
	maxFlashSaleQuota        int32 = 500
	demoVIPQuota             int32 = 10
)

// FlashSaleReport captures concurrency verification metrics from a live flash-sale spike test.
type FlashSaleReport struct {
	EventID             uuid.UUID `json:"event_id"`
	TicketTypeID        uuid.UUID `json:"ticket_type_id"`
	ConcurrentRequests  int       `json:"concurrent_requests"`
	SucceededBookings   int32     `json:"succeeded_bookings"`
	RejectedQuotaEmpty  int32     `json:"rejected_quota_empty"`
	RejectedOther       int32     `json:"rejected_other"`
	RemainingQuotaAfter int32     `json:"remaining_quota_after"`
	ZeroOversell        bool      `json:"zero_oversell"`
	DurationMS          int64     `json:"duration_ms"`
}

// DemoService runs high-concurrency in-process flash sale simulations for the 5-minute demo.
type DemoService struct {
	users  UserRepository
	events EventRepository
	orders *OrderService
}

// NewDemoService constructs a DemoService.
func NewDemoService(users UserRepository, events EventRepository, orders *OrderService) *DemoService {
	return &DemoService{users: users, events: events, orders: orders}
}

// RunFlashSale provisions distinct synthetic buyers and fires concurrent orders behind a simultaneous start barrier.
func (s *DemoService) RunFlashSale(ctx context.Context, requests int, quota int32) (*FlashSaleReport, error) {
	ready := s != nil && s.users != nil && s.events != nil && s.orders != nil && s.orders.orders != nil
	if err := validateServiceCall(ctx, "running flash sale", ready); err != nil {
		return nil, err
	}

	reqCount, tierQuota, err := normalizeFlashSaleParams(requests, quota)
	if err != nil {
		return nil, err
	}

	ev, targetTierID, buyerIDs, err := s.provisionFlashSaleFixture(ctx, reqCount, tierQuota)
	if err != nil {
		return nil, err
	}

	succ, quotaRej, otherRej, elapsed := s.executeConcurrentBurst(ctx, ev.ID, targetTierID, buyerIDs)
	remAfter, err := s.lookupRemainingQuota(ctx, ev.ID, targetTierID)
	if err != nil {
		return nil, err
	}

	expectedSold := min(int64(reqCount), int64(tierQuota))
	expectedRem := int64(tierQuota) - expectedSold
	return &FlashSaleReport{
		EventID:             ev.ID,
		TicketTypeID:        targetTierID,
		ConcurrentRequests:  reqCount,
		SucceededBookings:   succ,
		RejectedQuotaEmpty:  quotaRej,
		RejectedOther:       otherRej,
		RemainingQuotaAfter: remAfter,
		ZeroOversell:        int64(succ) == expectedSold && int64(remAfter) == expectedRem && otherRej == 0,
		DurationMS:          elapsed.Milliseconds(),
	}, nil
}

func normalizeFlashSaleParams(requests int, quota int32) (int, int32, error) {
	if requests <= 0 {
		requests = defaultFlashSaleRequests
	}
	if quota <= 0 {
		quota = defaultFlashSaleQuota
	}
	if requests > maxFlashSaleRequests || quota > maxFlashSaleQuota {
		return 0, 0, domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeValidation, "requests and quota must not exceed 500")
	}
	return requests, quota, nil
}

func (s *DemoService) provisionFlashSaleFixture(
	ctx context.Context,
	requests int,
	quota int32,
) (*domain.Event, uuid.UUID, []uuid.UUID, error) {
	organizerID := uuid.New()
	if err := s.users.EnsureDemoUser(ctx, organizerID, domain.RoleOrganizer); err != nil {
		return nil, uuid.Nil, nil, err
	}

	ev := &domain.Event{
		ID:          uuid.New(),
		OrganizerID: organizerID,
		Title:       "Live Concurrency Stress Event",
		Description: "Auto-generated event for live concurrent buyer demo",
		Venue:       "Concurrency Arena",
		EventDate:   time.Now().UTC().Add(48 * time.Hour),
		TicketTypes: []domain.TicketType{
			{ID: uuid.New(), Name: "Reguler", PriceIDR: 100000, TotalQuota: quota, RemainingQuota: quota},
			{ID: uuid.New(), Name: "VIP", PriceIDR: 250000, TotalQuota: demoVIPQuota, RemainingQuota: demoVIPQuota},
		},
	}
	if err := s.events.CreateWithTiers(ctx, ev); err != nil {
		return nil, uuid.Nil, nil, err
	}

	buyerIDs := make([]uuid.UUID, requests)
	for i := range requests {
		bid := uuid.New()
		buyerIDs[i] = bid
		if err := s.users.EnsureDemoUser(ctx, bid, domain.RoleBuyer); err != nil {
			return nil, uuid.Nil, nil, err
		}
	}
	return ev, ev.TicketTypes[0].ID, buyerIDs, nil
}

func (s *DemoService) executeConcurrentBurst(
	ctx context.Context,
	eventID, tierID uuid.UUID,
	buyerIDs []uuid.UUID,
) (int32, int32, int32, time.Duration) {
	var succeeded atomic.Int32
	var rejectedQuota atomic.Int32
	var rejectedOther atomic.Int32
	gate := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(len(buyerIDs))

	for _, bid := range buyerIDs {
		go func(buyerID uuid.UUID) {
			defer wg.Done()
			<-gate
			actor := domain.Actor{ID: buyerID, Role: domain.RoleBuyer}
			_, err := s.orders.CreateOrder(ctx, actor, CreateOrderInput{
				EventID: eventID,
				Items:   []repository.CheckoutItemInput{{TicketTypeID: tierID, Quantity: 1}},
			})
			if err == nil {
				succeeded.Add(1)
				return
			}
			if domErr, ok := errors.AsType[*domain.DomainError](err); ok && domErr.Code == domain.ErrCodeQuotaExhausted {
				rejectedQuota.Add(1)
				return
			}
			rejectedOther.Add(1)
		}(bid)
	}

	start := time.Now()
	close(gate)
	wg.Wait()
	return succeeded.Load(), rejectedQuota.Load(), rejectedOther.Load(), time.Since(start)
}

func (s *DemoService) lookupRemainingQuota(ctx context.Context, eventID, tierID uuid.UUID) (int32, error) {
	updatedEv, err := s.events.FindByID(ctx, eventID)
	if err != nil {
		return -1, err
	}
	if updatedEv == nil {
		return -1, errors.New("looking up remaining quota: event not found")
	}
	for _, tt := range updatedEv.TicketTypes {
		if tt.ID == tierID {
			return tt.RemainingQuota, nil
		}
	}
	return -1, fmt.Errorf("looking up remaining quota: ticket type %s not found", tierID)
}
