package repository

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/torikh42/treean/internal/domain"
)

const (
	defaultExpiryBatchLimit  = 50
	maxIdempotencyKeyRunes   = 128
	pgUniqueViolationSQLCode = "23505"
	idempotencyIndexName     = "uidx_orders_buyer_idempotency_key"
	actorTypeBuyer           = "buyer"
	actorTypeWebhook         = "webhook"
	actorTypeScheduler       = "scheduler"
	actorTypeLazyCheck       = "lazy-check"
	errMsgOrderNotFound      = "order not found"
	errMsgBuyerIDRequired    = "buyer_id is required"
	buyerCancelReason        = "cancelled by buyer before payment"
	expiryReasonScheduler    = "payment deadline (15m) expired automatically"
	expiryReasonLazy         = "payment deadline (15m) expired"
	defaultFailureReason     = "payment failed by gateway"
	maxFailureReasonRunes    = 255
	orderSelectColumns       = "id, invoice_number, event_id, buyer_id, idempotency_key, total_tickets, total_amount_idr, status, expires_at, paid_at, cancelled_at, expired_at, cancelled_reason, created_at"
)

// CheckoutItemInput specifies one ticket tier and desired quantity in an order creation request.
type CheckoutItemInput struct {
	TicketTypeID uuid.UUID
	Quantity     int32
}

// CreateOrderResult captures a created or idempotently replayed order reservation.
type CreateOrderResult struct {
	Order            *domain.Order
	IdempotentReplay bool
}

// PaymentConfirmResult captures whether payment transitioned an order or replayed idempotently.
type PaymentConfirmResult struct {
	Order            *domain.Order
	IdempotentReplay bool
}

// OrderRepo implements high-concurrency order, quota locking, expiry, and ticket persistence.
type OrderRepo struct {
	pool *pgxpool.Pool
}

// NewOrderRepo constructs an OrderRepo.
func NewOrderRepo(pool *pgxpool.Pool) *OrderRepo {
	return &OrderRepo{pool: pool}
}

type lockedTier struct {
	id        uuid.UUID
	name      string
	priceIDR  int64
	remaining int32
}

type queryRunner interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// CreateOrder executes dual-locked multi-item ticket reservation with 15-minute expiry.
func (r *OrderRepo) CreateOrder(ctx context.Context, eventID, buyerID uuid.UUID, items []CheckoutItemInput, now time.Time) (*domain.Order, error) {
	res, err := r.CreateOrderIdempotent(ctx, eventID, buyerID, items, "", now)
	if err != nil {
		return nil, err
	}
	return res.Order, nil
}

// CreateOrderIdempotent reserves tickets atomically and replays an existing order if idempotencyKey matches.
func (r *OrderRepo) CreateOrderIdempotent(
	ctx context.Context,
	eventID, buyerID uuid.UUID,
	items []CheckoutItemInput,
	idempotencyKey string,
	now time.Time,
) (*CreateOrderResult, error) {
	cleanKey, err := normalizeIdempotencyKey(idempotencyKey)
	if err != nil {
		return nil, err
	}
	cleanItems, requestedQty, nowUTC, err := r.prepareCreateOrder(ctx, eventID, buyerID, items, now)
	if err != nil {
		return nil, err
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("beginning create order tx: %w", err)
	}
	defer rollbackTx(tx)

	res, err := createOrderInTx(ctx, tx, eventID, buyerID, cleanItems, requestedQty, cleanKey, nowUTC)
	if err != nil {
		return r.resolveOrderCreateConflict(ctx, tx, buyerID, cleanKey, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing create order tx: %w", err)
	}
	return res, nil
}

func (r *OrderRepo) resolveOrderCreateConflict(
	ctx context.Context,
	tx pgx.Tx,
	buyerID uuid.UUID,
	cleanKey string,
	createErr error,
) (*CreateOrderResult, error) {
	if cleanKey == "" || !isIdempotencyKeyConflict(createErr) {
		return nil, createErr
	}
	rollbackTx(tx)
	existing, lookupErr := r.findOrderByIdempotencyKey(ctx, buyerID, cleanKey)
	if lookupErr != nil {
		return nil, lookupErr
	}
	if existing == nil {
		return nil, createErr
	}
	return &CreateOrderResult{Order: existing, IdempotentReplay: true}, nil
}

func (r *OrderRepo) prepareCreateOrder(
	ctx context.Context,
	eventID, buyerID uuid.UUID,
	items []CheckoutItemInput,
	now time.Time,
) ([]CheckoutItemInput, int32, time.Time, error) {
	if err := validateRepoCall(ctx, r, "creating order"); err != nil {
		return nil, 0, time.Time{}, err
	}
	if err := requireTimestamp("creating order", now); err != nil {
		return nil, 0, time.Time{}, err
	}
	if err := validateCreateOrderIDs(eventID, buyerID); err != nil {
		return nil, 0, time.Time{}, err
	}
	cleanItems, requestedQty, err := aggregateCheckoutItems(items)
	if err != nil {
		return nil, 0, time.Time{}, err
	}
	if err := requireRepoPool(r, "creating order"); err != nil {
		return nil, 0, time.Time{}, err
	}

	nowUTC := now.UTC()
	// Run overdue sweeps outside the checkout tx so ticket_types rows are locked once in canonical ASC order.
	swept, err := r.ExpireDueOrders(ctx, &eventID, defaultExpiryBatchLimit, actorTypeLazyCheck, nowUTC)
	if err != nil {
		return nil, 0, time.Time{}, err
	}
	if swept == defaultExpiryBatchLimit {
		if err := r.expireBuyerDueOrdersForEvent(ctx, eventID, buyerID, nowUTC); err != nil {
			return nil, 0, time.Time{}, err
		}
	}
	return cleanItems, requestedQty, nowUTC, nil
}

func createOrderInTx(
	ctx context.Context,
	tx pgx.Tx,
	eventID, buyerID uuid.UUID,
	cleanItems []CheckoutItemInput,
	requestedQty int32,
	cleanKey string,
	nowUTC time.Time,
) (*CreateOrderResult, error) {
	if err := ensureEventExists(ctx, tx, eventID); err != nil {
		return nil, err
	}
	if err := acquireBuyerEventLock(ctx, tx, eventID, buyerID); err != nil {
		return nil, err
	}
	if cleanKey != "" {
		existing, err := findOrderByIdempotencyKeyInTx(ctx, tx, buyerID, cleanKey)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			return &CreateOrderResult{Order: existing, IdempotentReplay: true}, nil
		}
	}
	if err := checkBuyerAllowance(ctx, tx, eventID, buyerID, requestedQty, nowUTC); err != nil {
		return nil, err
	}

	tiersMap, err := lockTicketTypesForCheckout(ctx, tx, eventID, cleanItems)
	if err != nil {
		return nil, err
	}
	orderItems, totalAmount, err := computeCheckoutTotals(cleanItems, tiersMap)
	if err != nil {
		return nil, err
	}
	if err := decrementTicketQuotas(ctx, tx, cleanItems); err != nil {
		return nil, err
	}
	order, err := persistCreatedOrder(ctx, tx, eventID, buyerID, cleanKey, requestedQty, totalAmount, orderItems, nowUTC)
	if err != nil {
		return nil, err
	}
	return &CreateOrderResult{Order: order, IdempotentReplay: false}, nil
}

// CancelOrder cancels an unpaid order, restores quota atomically, and rejects expired or paid orders.
func (r *OrderRepo) CancelOrder(ctx context.Context, orderID, buyerID uuid.UUID, now time.Time) (*domain.Order, error) {
	if err := validateRepoCall(ctx, r, "cancelling order"); err != nil {
		return nil, err
	}
	if err := requireTimestamp("cancelling order", now); err != nil {
		return nil, err
	}
	if orderID == uuid.Nil {
		return nil, domain.NewDomainError(http.StatusNotFound, domain.ErrCodeOrderNotFound, errMsgOrderNotFound)
	}
	if buyerID == uuid.Nil {
		return nil, domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeValidation, errMsgBuyerIDRequired)
	}
	if err := requireRepoPool(r, "cancelling order"); err != nil {
		return nil, err
	}

	nowUTC := now.UTC()
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("beginning cancel order tx: %w", err)
	}
	defer rollbackTx(tx)

	order, err := lockOrderForUpdate(ctx, tx, orderID)
	if err != nil {
		return nil, err
	}
	if order.BuyerID != buyerID {
		return nil, domain.NewDomainError(http.StatusForbidden, domain.ErrCodeForbidden, "cannot cancel another buyer's order")
	}
	if err := order.CanCancel(nowUTC); err != nil {
		return nil, persistLazyExpiryIfNeeded(ctx, tx, order, err, "committing lazy expiry on cancel", nowUTC)
	}

	if err := restockAndTransitionOrder(ctx, tx, order, domain.OrderStatusCancelled, &buyerID, actorTypeBuyer, buyerCancelReason, nowUTC); err != nil {
		return nil, err
	}
	if err := loadOrderDetailsInTx(ctx, tx, order); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing cancel order tx: %w", err)
	}
	return order, nil
}

// ConfirmPayment validates order state inside row lock, transitions WAITING_PAYMENT to PAID, and issues unique tickets once.
func (r *OrderRepo) ConfirmPayment(ctx context.Context, orderID uuid.UUID, grossAmount int64, now time.Time) (*PaymentConfirmResult, error) {
	if err := validateRepoCall(ctx, r, "confirming payment"); err != nil {
		return nil, err
	}
	if err := requireTimestamp("confirming payment", now); err != nil {
		return nil, err
	}
	if orderID == uuid.Nil {
		return nil, domain.NewDomainError(http.StatusNotFound, domain.ErrCodeOrderNotFound, errMsgOrderNotFound)
	}
	if err := requireRepoPool(r, "confirming payment"); err != nil {
		return nil, err
	}

	nowUTC := now.UTC()
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("beginning confirm payment tx: %w", err)
	}
	defer rollbackTx(tx)

	order, err := lockOrderForUpdate(ctx, tx, orderID)
	if err != nil {
		return nil, err
	}
	if order.TotalAmountIDR != grossAmount {
		return nil, domain.NewPaymentAmountMismatchError(
			fmt.Sprintf("gross_amount %d does not match order total %d", grossAmount, order.TotalAmountIDR),
		)
	}
	if err := order.CanPay(nowUTC); err != nil {
		return nil, persistLazyExpiryIfNeeded(ctx, tx, order, err, "committing lazy expiry on late webhook", nowUTC)
	}
	if order.Status == domain.OrderStatusPaid {
		return replayConfirmedPayment(ctx, tx, order)
	}

	if err := markOrderPaidAndIssueTickets(ctx, tx, order, nowUTC); err != nil {
		return nil, err
	}
	if err := loadOrderDetailsInTx(ctx, tx, order); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing confirm payment tx: %w", err)
	}
	return &PaymentConfirmResult{Order: order, IdempotentReplay: false}, nil
}

// FailPayment transitions a WAITING_PAYMENT order to EXPIRED or CANCELLED and releases quota on gateway failure notification.
func (r *OrderRepo) FailPayment(ctx context.Context, orderID uuid.UUID, reason string, now time.Time) (domain.WebhookOutcome, error) {
	outcome, _, err := r.FailPaymentWithEvent(ctx, orderID, reason, now)
	return outcome, err
}

// FailPaymentWithEvent transitions an unpaid order on gateway failure and returns the affected event ID when transitioned.
func (r *OrderRepo) FailPaymentWithEvent(ctx context.Context, orderID uuid.UUID, reason string, now time.Time) (domain.WebhookOutcome, uuid.UUID, error) {
	if err := validateRepoCall(ctx, r, "failing payment"); err != nil {
		return "", uuid.Nil, err
	}
	if err := requireTimestamp("failing payment", now); err != nil {
		return "", uuid.Nil, err
	}
	if orderID == uuid.Nil {
		return "", uuid.Nil, domain.NewDomainError(http.StatusNotFound, domain.ErrCodeOrderNotFound, errMsgOrderNotFound)
	}
	if err := requireRepoPool(r, "failing payment"); err != nil {
		return "", uuid.Nil, err
	}

	nowUTC := now.UTC()
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", uuid.Nil, fmt.Errorf("beginning fail payment tx: %w", err)
	}
	defer rollbackTx(tx)

	order, err := lockOrderForUpdate(ctx, tx, orderID)
	if err != nil {
		return "", uuid.Nil, err
	}

	switch order.Status {
	case domain.OrderStatusPaid, domain.OrderStatusExpired, domain.OrderStatusCancelled:
		if err := tx.Commit(ctx); err != nil {
			return "", uuid.Nil, fmt.Errorf("committing terminal fail payment check: %w", err)
		}
		return domain.OutcomeIgnoredDuplicate, uuid.Nil, nil
	case domain.OrderStatusWaitingPayment:
		// Waiting payment order can transition below.
	default:
		return "", uuid.Nil, fmt.Errorf("failing payment: unexpected order status %q", order.Status)
	}

	cleanReason := normalizeFailureReason(reason)
	targetStatus, err := classifyFailureTransition(order, cleanReason, nowUTC)
	if err != nil {
		return "", uuid.Nil, err
	}
	if err := restockAndTransitionOrder(ctx, tx, order, targetStatus, nil, actorTypeWebhook, cleanReason, nowUTC); err != nil {
		return "", uuid.Nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", uuid.Nil, fmt.Errorf("committing fail payment tx: %w", err)
	}
	return domain.OutcomeProcessedFailed, order.EventID, nil
}

// ExpireDueOrders sweeps up to limit overdue WAITING_PAYMENT orders, optionally scoped to one event.
func (r *OrderRepo) ExpireDueOrders(ctx context.Context, eventID *uuid.UUID, limit int, actorType string, now time.Time) (int, error) {
	count, _, err := r.ExpireDueOrdersWithEvents(ctx, eventID, limit, actorType, now)
	return count, err
}

// ExpireDueOrdersWithEvents sweeps overdue WAITING_PAYMENT orders and returns the count and affected event IDs.
func (r *OrderRepo) ExpireDueOrdersWithEvents(ctx context.Context, eventID *uuid.UUID, limit int, actorType string, now time.Time) (int, []uuid.UUID, error) {
	if err := validateRepoCall(ctx, r, "expiring due orders"); err != nil {
		return 0, nil, err
	}
	if err := requireTimestamp("expiring due orders", now); err != nil {
		return 0, nil, err
	}
	if err := validateExpiryActor(actorType); err != nil {
		return 0, nil, err
	}
	if eventID != nil && *eventID == uuid.Nil {
		return 0, nil, errors.New("expiring due orders: event_id cannot be nil UUID")
	}
	if err := requireRepoPool(r, "expiring due orders"); err != nil {
		return 0, nil, err
	}
	if limit <= 0 {
		limit = defaultExpiryBatchLimit
	}

	nowUTC := now.UTC()
	hasDue, err := r.hasDueOrders(ctx, eventID, nowUTC)
	if err != nil {
		return 0, nil, err
	}
	if !hasDue {
		return 0, nil, nil
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, nil, fmt.Errorf("beginning expire due orders tx: %w", err)
	}
	defer rollbackTx(tx)

	count, touchedEvents, err := expireDueOrdersInTx(ctx, tx, eventID, limit, actorType, nowUTC)
	if err != nil {
		return 0, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, nil, fmt.Errorf("committing expire due orders tx: %w", err)
	}
	return count, touchedEvents, nil
}

// FindByID loads a full order including items, tickets, and transition logs in a repeatable-read snapshot.
func (r *OrderRepo) FindByID(ctx context.Context, orderID uuid.UUID) (*domain.Order, error) {
	if err := validateRepoCall(ctx, r, "finding order"); err != nil {
		return nil, err
	}
	if orderID == uuid.Nil {
		return nil, domain.NewDomainError(http.StatusNotFound, domain.ErrCodeOrderNotFound, errMsgOrderNotFound)
	}
	if err := requireRepoPool(r, "finding order"); err != nil {
		return nil, err
	}

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return nil, fmt.Errorf("beginning find order tx: %w", err)
	}
	defer rollbackTx(tx)

	const queryOrder = "SELECT " + orderSelectColumns + " FROM orders WHERE id = $1"
	o, err := scanOrder(tx.QueryRow(ctx, queryOrder, orderID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.NewDomainError(http.StatusNotFound, domain.ErrCodeOrderNotFound, errMsgOrderNotFound)
		}
		return nil, fmt.Errorf("querying order: %w", err)
	}
	if err := loadOrderDetailsInTx(ctx, tx, &o); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing find order tx: %w", err)
	}
	return &o, nil
}

// ListByBuyer loads all orders belonging to a specific buyer, most recent first, without N+1 queries.
func (r *OrderRepo) ListByBuyer(ctx context.Context, buyerID uuid.UUID) ([]domain.Order, error) {
	if err := validateRepoCall(ctx, r, "listing buyer orders"); err != nil {
		return nil, err
	}
	if buyerID == uuid.Nil {
		return nil, domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeValidation, errMsgBuyerIDRequired)
	}
	if err := requireRepoPool(r, "listing buyer orders"); err != nil {
		return nil, err
	}

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return nil, fmt.Errorf("beginning list buyer orders tx: %w", err)
	}
	defer rollbackTx(tx)

	orders, orderIDs, orderIdx, err := queryBuyerOrders(ctx, tx, buyerID)
	if err != nil {
		return nil, err
	}
	if len(orders) == 0 {
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("committing empty list buyer orders tx: %w", err)
		}
		return []domain.Order{}, nil
	}

	if err := populateBuyerOrderDetails(ctx, tx, orderIDs, orderIdx, orders); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing list buyer orders tx: %w", err)
	}
	return orders, nil
}

func populateBuyerOrderDetails(
	ctx context.Context,
	q queryRunner,
	orderIDs []uuid.UUID,
	orderIdx map[uuid.UUID]int,
	orders []domain.Order,
) error {
	itemsMap, err := loadOrderItemsByOrderIDs(ctx, q, orderIDs)
	if err != nil {
		return err
	}
	ticketsMap, err := loadOrderTicketsByOrderIDs(ctx, q, orderIDs)
	if err != nil {
		return err
	}
	for id, items := range itemsMap {
		if idx, ok := orderIdx[id]; ok {
			orders[idx].Items = items
		}
	}
	for id, tickets := range ticketsMap {
		if idx, ok := orderIdx[id]; ok {
			orders[idx].Tickets = tickets
		}
	}
	return nil
}

func validateRepoCall(ctx context.Context, r *OrderRepo, op string) error {
	if ctx == nil {
		return fmt.Errorf("%s: context is required", op)
	}
	if r == nil {
		return fmt.Errorf("%s: repository is nil", op)
	}
	return nil
}

func requireRepoPool(r *OrderRepo, op string) error {
	if r.pool == nil {
		return fmt.Errorf("%s: database pool is not initialized", op)
	}
	return nil
}

func requireTimestamp(op string, now time.Time) error {
	if now.IsZero() {
		return fmt.Errorf("%s: timestamp is required", op)
	}
	return nil
}

func validateCreateOrderIDs(eventID, buyerID uuid.UUID) error {
	if eventID == uuid.Nil {
		return domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeValidation, "event_id is required")
	}
	if buyerID == uuid.Nil {
		return domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeValidation, errMsgBuyerIDRequired)
	}
	return nil
}

func validateExpiryActor(actorType string) error {
	if actorType != actorTypeScheduler && actorType != actorTypeLazyCheck {
		return fmt.Errorf("expiring due orders: invalid actor type %q", actorType)
	}
	return nil
}

func aggregateCheckoutItems(items []CheckoutItemInput) ([]CheckoutItemInput, int32, error) {
	if len(items) == 0 {
		return nil, 0, domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeInvalidQuantity, "at least one ticket item is required")
	}

	merged := make(map[uuid.UUID]int32, len(items))
	var totalQty int32

	for _, it := range items {
		if it.TicketTypeID == uuid.Nil {
			return nil, 0, domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeValidation, "ticket_type_id is required")
		}
		if it.Quantity <= 0 {
			return nil, 0, domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeInvalidQuantity, "ticket item quantity must be positive")
		}
		if it.Quantity > domain.MaxTicketsPerBuyerPerEvent || totalQty > domain.MaxTicketsPerBuyerPerEvent-it.Quantity {
			return nil, 0, domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeInvalidQuantity, "order total_tickets must be between 1 and 4")
		}
		merged[it.TicketTypeID] += it.Quantity
		totalQty += it.Quantity
	}

	out := make([]CheckoutItemInput, 0, len(merged))
	for tierID, qty := range merged {
		out = append(out, CheckoutItemInput{
			TicketTypeID: tierID,
			Quantity:     qty,
		})
	}
	slices.SortFunc(out, func(a, b CheckoutItemInput) int {
		return bytes.Compare(a.TicketTypeID[:], b.TicketTypeID[:])
	})
	return out, totalQty, nil
}

func ensureEventExists(ctx context.Context, tx pgx.Tx, eventID uuid.UUID) error {
	var exists bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM events WHERE id = $1)", eventID).Scan(&exists); err != nil {
		return fmt.Errorf("checking event existence: %w", err)
	}
	if !exists {
		return domain.NewDomainError(http.StatusNotFound, domain.ErrCodeEventNotFound, "event not found")
	}
	return nil
}

func acquireBuyerEventLock(ctx context.Context, tx pgx.Tx, eventID, buyerID uuid.UUID) error {
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1), hashtext($2))", eventID.String(), buyerID.String()); err != nil {
		return fmt.Errorf("acquiring buyer-event advisory lock: %w", err)
	}
	return nil
}

func checkBuyerAllowance(ctx context.Context, tx pgx.Tx, eventID, buyerID uuid.UUID, requestedQty int32, nowUTC time.Time) error {
	const sumBuyerSQL = `
		SELECT COALESCE(SUM(total_tickets), 0)::int
		FROM orders
		WHERE event_id = $1 AND buyer_id = $2
		  AND (status = 'PAID' OR (status = 'WAITING_PAYMENT' AND expires_at >= $3))`
	var existingQty int32
	if err := tx.QueryRow(ctx, sumBuyerSQL, eventID, buyerID, nowUTC).Scan(&existingQty); err != nil {
		return fmt.Errorf("checking buyer ticket allowance: %w", err)
	}
	if int64(existingQty)+int64(requestedQty) > int64(domain.MaxTicketsPerBuyerPerEvent) {
		return domain.NewBuyerLimitExceededError(
			fmt.Sprintf("buyer ticket limit exceeded: active/paid=%d, requested=%d, max=%d", existingQty, requestedQty, domain.MaxTicketsPerBuyerPerEvent),
		)
	}
	return nil
}

func lockTicketTypesForCheckout(ctx context.Context, tx pgx.Tx, eventID uuid.UUID, items []CheckoutItemInput) (map[uuid.UUID]lockedTier, error) {
	tierIDs := make([]uuid.UUID, len(items))
	for i, it := range items {
		tierIDs[i] = it.TicketTypeID
	}

	const lockTiersSQL = `
		SELECT id, name, price_idr, remaining_quota
		FROM ticket_types
		WHERE id = ANY($1) AND event_id = $2
		ORDER BY id ASC
		FOR UPDATE`
	rows, err := tx.Query(ctx, lockTiersSQL, tierIDs, eventID)
	if err != nil {
		return nil, fmt.Errorf("locking ticket types: %w", err)
	}
	defer rows.Close()

	tiersMap := make(map[uuid.UUID]lockedTier, len(items))
	for rows.Next() {
		var lt lockedTier
		if err := rows.Scan(&lt.id, &lt.name, &lt.priceIDR, &lt.remaining); err != nil {
			return nil, fmt.Errorf("scanning locked ticket type: %w", err)
		}
		tiersMap[lt.id] = lt
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating locked ticket types: %w", err)
	}
	rows.Close()
	return tiersMap, nil
}

func computeCheckoutTotals(items []CheckoutItemInput, tiers map[uuid.UUID]lockedTier) ([]domain.OrderItem, int64, error) {
	if len(items) == 0 {
		return nil, 0, domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeInvalidQuantity, "at least one ticket item is required")
	}

	// Verify all requested tiers belong to the event before evaluating per-tier quota so order of UUIDs never changes 404 vs 409.
	for _, it := range items {
		if _, ok := tiers[it.TicketTypeID]; !ok {
			return nil, 0, domain.NewDomainError(http.StatusNotFound, domain.ErrCodeTicketTypeNotFound, "one or more ticket types do not belong to this event")
		}
	}

	orderItems := make([]domain.OrderItem, 0, len(items))
	var totalAmount int64

	for _, it := range items {
		lt := tiers[it.TicketTypeID]
		if lt.remaining < it.Quantity {
			return nil, 0, domain.NewQuotaExhaustedError(
				fmt.Sprintf("insufficient quota for tier %q: requested=%d, remaining=%d", lt.name, it.Quantity, lt.remaining),
			)
		}
		if lt.priceIDR < 0 || it.Quantity <= 0 {
			return nil, 0, domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeValidation, "invalid ticket price or quantity")
		}

		qty64 := int64(it.Quantity)
		if lt.priceIDR > math.MaxInt64/qty64 {
			return nil, 0, errors.New("computing order subtotal: amount exceeds int64 range")
		}
		subtotal := lt.priceIDR * qty64
		if totalAmount > math.MaxInt64-subtotal {
			return nil, 0, errors.New("computing order total: amount exceeds int64 range")
		}
		totalAmount += subtotal

		orderItems = append(orderItems, domain.OrderItem{
			TicketTypeID:   it.TicketTypeID,
			TicketTypeName: lt.name,
			Quantity:       it.Quantity,
			UnitPriceIDR:   lt.priceIDR,
			SubtotalIDR:    subtotal,
		})
	}
	return orderItems, totalAmount, nil
}

func decrementTicketQuotas(ctx context.Context, tx pgx.Tx, items []CheckoutItemInput) error {
	const updateSQL = `UPDATE ticket_types SET remaining_quota = remaining_quota - $1 WHERE id = $2`
	for _, it := range items {
		if _, err := tx.Exec(ctx, updateSQL, it.Quantity, it.TicketTypeID); err != nil {
			return fmt.Errorf("decrementing ticket quota: %w", err)
		}
	}
	return nil
}

func persistCreatedOrder(
	ctx context.Context,
	tx pgx.Tx,
	eventID uuid.UUID,
	buyerID uuid.UUID,
	cleanKey string,
	requestedQty int32,
	totalAmount int64,
	orderItems []domain.OrderItem,
	nowUTC time.Time,
) (*domain.Order, error) {
	var seq int64
	if err := tx.QueryRow(ctx, "SELECT nextval('invoice_seq')").Scan(&seq); err != nil {
		return nil, fmt.Errorf("generating invoice sequence: %w", err)
	}
	invoiceNumber := formatInvoiceNumber(nowUTC, seq)

	orderID := uuid.New()
	expiresAt := nowUTC.Add(domain.OrderHoldDuration)
	const insertOrderSQL = `
		INSERT INTO orders (id, invoice_number, event_id, buyer_id, idempotency_key, total_tickets, total_amount_idr, status, expires_at, created_at)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6, $7, $8, $9, $10)`
	if _, err := tx.Exec(ctx, insertOrderSQL,
		orderID, invoiceNumber, eventID, buyerID, cleanKey, requestedQty, totalAmount,
		string(domain.OrderStatusWaitingPayment), expiresAt, nowUTC,
	); err != nil {
		return nil, fmt.Errorf("inserting order: %w", err)
	}

	var keyPtr *string
	if cleanKey != "" {
		k := cleanKey
		keyPtr = &k
	}

	const insertItemSQL = `
		INSERT INTO order_items (id, order_id, ticket_type_id, quantity, unit_price_idr, subtotal_idr)
		VALUES ($1, $2, $3, $4, $5, $6)`
	for i := range orderItems {
		itemID := uuid.New()
		orderItems[i].ID = itemID
		orderItems[i].OrderID = orderID
		if _, err := tx.Exec(ctx, insertItemSQL,
			itemID, orderID, orderItems[i].TicketTypeID, orderItems[i].Quantity, orderItems[i].UnitPriceIDR, orderItems[i].SubtotalIDR,
		); err != nil {
			return nil, fmt.Errorf("inserting order item: %w", err)
		}
	}

	buyerCopy := buyerID
	initialLog := domain.OrderLog{
		ID:        uuid.New(),
		OrderID:   orderID,
		ToStatus:  string(domain.OrderStatusWaitingPayment),
		ActorID:   &buyerCopy,
		ActorType: actorTypeBuyer,
		Note:      "order created; quota reserved for 15 minutes",
		CreatedAt: nowUTC,
	}
	if err := insertOrderLogEntry(ctx, tx, initialLog); err != nil {
		return nil, err
	}

	return &domain.Order{
		ID:             orderID,
		InvoiceNumber:  invoiceNumber,
		EventID:        eventID,
		BuyerID:        buyerID,
		IdempotencyKey: keyPtr,
		TotalTickets:   requestedQty,
		TotalAmountIDR: totalAmount,
		Status:         domain.OrderStatusWaitingPayment,
		ExpiresAt:      expiresAt,
		CreatedAt:      nowUTC,
		Items:          orderItems,
		Tickets:        []domain.Ticket{},
		Logs:           []domain.OrderLog{initialLog},
	}, nil
}

func formatInvoiceNumber(now time.Time, seq int64) string {
	return fmt.Sprintf("INV-%s-%06d", now.UTC().Format("2006"), seq)
}

func persistLazyExpiryIfNeeded(
	ctx context.Context,
	tx pgx.Tx,
	order *domain.Order,
	stateErr error,
	commitOp string,
	nowUTC time.Time,
) error {
	if de, ok := errors.AsType[*domain.DomainError](stateErr); ok &&
		de.Code == domain.ErrCodeOrderExpired &&
		order.Status == domain.OrderStatusWaitingPayment {
		if expErr := markOrderExpiredAndRestock(ctx, tx, order, actorTypeLazyCheck, nowUTC); expErr != nil {
			return expErr
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return fmt.Errorf("%s: %w", commitOp, commitErr)
		}
	}
	return stateErr
}

func replayConfirmedPayment(ctx context.Context, tx pgx.Tx, order *domain.Order) (*PaymentConfirmResult, error) {
	if err := loadOrderDetailsInTx(ctx, tx, order); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing idempotent payment check: %w", err)
	}
	return &PaymentConfirmResult{Order: order, IdempotentReplay: true}, nil
}

func markOrderPaidAndIssueTickets(ctx context.Context, tx pgx.Tx, order *domain.Order, paidAt time.Time) error {
	if _, err := tx.Exec(ctx, "UPDATE orders SET status = 'PAID', paid_at = $2 WHERE id = $1", order.ID, paidAt); err != nil {
		return fmt.Errorf("updating order to PAID: %w", err)
	}
	paidAtCopy := paidAt
	order.Status = domain.OrderStatusPaid
	order.PaidAt = &paidAtCopy

	itemsMap, err := loadOrderItemsByOrderIDs(ctx, tx, []uuid.UUID{order.ID})
	if err != nil {
		return err
	}
	items := itemsMap[order.ID]

	const insertTicketSQL = `
		INSERT INTO tickets (id, ticket_code, order_id, order_item_id, event_id, ticket_type_id, owner_id, seat_sequence, issued_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`
	for _, it := range items {
		for idx := range it.Quantity {
			seat := idx + 1
			code, genErr := generateTicketCode(paidAt)
			if genErr != nil {
				return genErr
			}
			if _, err := tx.Exec(ctx, insertTicketSQL,
				uuid.New(), code, order.ID, it.ID, order.EventID, it.TicketTypeID, order.BuyerID, seat, paidAt,
			); err != nil {
				return fmt.Errorf("inserting ticket: %w", err)
			}
		}
	}

	prev := string(domain.OrderStatusWaitingPayment)
	return insertOrderLog(ctx, tx, order.ID, &prev, string(domain.OrderStatusPaid), nil, actorTypeWebhook, "payment confirmed and tickets issued", paidAt)
}

func (r *OrderRepo) hasDueOrders(ctx context.Context, eventID *uuid.UUID, nowUTC time.Time) (bool, error) {
	var hasDue bool
	var err error
	if eventID != nil {
		const sqlByEvent = `SELECT EXISTS(SELECT 1 FROM orders WHERE event_id = $1 AND status = 'WAITING_PAYMENT' AND expires_at < $2)`
		err = r.pool.QueryRow(ctx, sqlByEvent, *eventID, nowUTC).Scan(&hasDue)
	} else {
		const sqlGlobal = `SELECT EXISTS(SELECT 1 FROM orders WHERE status = 'WAITING_PAYMENT' AND expires_at < $1)`
		err = r.pool.QueryRow(ctx, sqlGlobal, nowUTC).Scan(&hasDue)
	}
	if err != nil {
		return false, fmt.Errorf("checking partial expiry index: %w", err)
	}
	return hasDue, nil
}

func (r *OrderRepo) expireBuyerDueOrdersForEvent(ctx context.Context, eventID, buyerID uuid.UUID, nowUTC time.Time) error {
	const existsBuyerDueSQL = `
		SELECT EXISTS(
			SELECT 1 FROM orders
			WHERE event_id = $1 AND buyer_id = $2 AND status = 'WAITING_PAYMENT' AND expires_at < $3
		)`
	var hasDue bool
	if err := r.pool.QueryRow(ctx, existsBuyerDueSQL, eventID, buyerID, nowUTC).Scan(&hasDue); err != nil {
		return fmt.Errorf("checking buyer overdue orders: %w", err)
	}
	if !hasDue {
		return nil
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning buyer overdue sweep tx: %w", err)
	}
	defer rollbackTx(tx)

	const selectBuyerDueSQL = `
		SELECT id
		FROM orders
		WHERE event_id = $1 AND buyer_id = $2 AND status = 'WAITING_PAYMENT' AND expires_at < $3
		ORDER BY id ASC
		FOR UPDATE SKIP LOCKED`
	rows, err := tx.Query(ctx, selectBuyerDueSQL, eventID, buyerID, nowUTC)
	if err != nil {
		return fmt.Errorf("selecting buyer overdue orders: %w", err)
	}
	dueIDs, err := scanUUIDRows(rows, "buyer overdue order id")
	if err != nil {
		return err
	}
	if err := expireOrderIDsInTx(ctx, tx, dueIDs, actorTypeLazyCheck, expiryReasonLazy, nowUTC); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing buyer overdue sweep tx: %w", err)
	}
	return nil
}

func lockOrderForUpdate(ctx context.Context, tx pgx.Tx, orderID uuid.UUID) (*domain.Order, error) {
	const query = "SELECT " + orderSelectColumns + " FROM orders WHERE id = $1 FOR UPDATE"
	o, err := scanOrder(tx.QueryRow(ctx, query, orderID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.NewDomainError(http.StatusNotFound, domain.ErrCodeOrderNotFound, errMsgOrderNotFound)
		}
		return nil, fmt.Errorf("locking order for update: %w", err)
	}
	return &o, nil
}

func expireDueOrdersInTx(ctx context.Context, tx pgx.Tx, eventID *uuid.UUID, limit int, actorType string, nowUTC time.Time) (int, []uuid.UUID, error) {
	dueIDs, touchedEvents, err := selectDueOrderIDsForUpdate(ctx, tx, eventID, limit, nowUTC)
	if err != nil {
		return 0, nil, err
	}
	if err := expireOrderIDsInTx(ctx, tx, dueIDs, actorType, expiryReasonScheduler, nowUTC); err != nil {
		return 0, nil, err
	}
	return len(dueIDs), touchedEvents, nil
}

func expireOrderIDsInTx(ctx context.Context, tx pgx.Tx, orderIDs []uuid.UUID, actorType, reason string, nowUTC time.Time) error {
	if len(orderIDs) == 0 {
		return nil
	}
	if err := restockOrderQuota(ctx, tx, orderIDs); err != nil {
		return err
	}

	const expireSQL = `
		UPDATE orders
		SET status = 'EXPIRED', expired_at = $2, cancelled_reason = $3
		WHERE id = ANY($1)`
	if _, err := tx.Exec(ctx, expireSQL, orderIDs, nowUTC, reason); err != nil {
		return fmt.Errorf("marking orders expired: %w", err)
	}

	prev := string(domain.OrderStatusWaitingPayment)
	for _, id := range orderIDs {
		if err := insertOrderLog(ctx, tx, id, &prev, string(domain.OrderStatusExpired), nil, actorType, reason, nowUTC); err != nil {
			return err
		}
	}
	return nil
}

func selectDueOrderIDsForUpdate(ctx context.Context, tx pgx.Tx, eventID *uuid.UUID, limit int, nowUTC time.Time) ([]uuid.UUID, []uuid.UUID, error) {
	var rows pgx.Rows
	var err error
	if eventID != nil {
		const sqlByEvent = `
			SELECT id, event_id FROM orders
			WHERE event_id = $1 AND status = 'WAITING_PAYMENT' AND expires_at < $2
			ORDER BY id ASC
			FOR UPDATE SKIP LOCKED
			LIMIT $3`
		rows, err = tx.Query(ctx, sqlByEvent, *eventID, nowUTC, limit)
	} else {
		const sqlGlobal = `
			SELECT id, event_id FROM orders
			WHERE status = 'WAITING_PAYMENT' AND expires_at < $1
			ORDER BY id ASC
			FOR UPDATE SKIP LOCKED
			LIMIT $2`
		rows, err = tx.Query(ctx, sqlGlobal, nowUTC, limit)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("selecting overdue orders: %w", err)
	}
	return scanDueOrderAndEventRows(rows)
}

func scanDueOrderAndEventRows(rows pgx.Rows) ([]uuid.UUID, []uuid.UUID, error) {
	defer rows.Close()
	var orderIDs []uuid.UUID
	var eventIDs []uuid.UUID
	seenEvents := make(map[uuid.UUID]struct{})
	for rows.Next() {
		var oid, eid uuid.UUID
		if err := rows.Scan(&oid, &eid); err != nil {
			return nil, nil, fmt.Errorf("scanning overdue order row: %w", err)
		}
		orderIDs = append(orderIDs, oid)
		if _, dup := seenEvents[eid]; !dup && eid != uuid.Nil {
			seenEvents[eid] = struct{}{}
			eventIDs = append(eventIDs, eid)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterating overdue order rows: %w", err)
	}
	rows.Close()
	return orderIDs, eventIDs, nil
}

func scanUUIDRows(rows pgx.Rows, label string) ([]uuid.UUID, error) {
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning %s: %w", label, err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating %s: %w", label, err)
	}
	rows.Close()
	return ids, nil
}

func restockAndTransitionOrder(
	ctx context.Context,
	tx pgx.Tx,
	order *domain.Order,
	targetStatus domain.OrderStatus,
	actorID *uuid.UUID,
	actorType, reason string,
	nowUTC time.Time,
) error {
	var updateSQL string
	switch targetStatus {
	case domain.OrderStatusExpired:
		updateSQL = `UPDATE orders SET status = 'EXPIRED', expired_at = $2, cancelled_reason = $3 WHERE id = $1`
	case domain.OrderStatusCancelled:
		updateSQL = `UPDATE orders SET status = 'CANCELLED', cancelled_at = $2, cancelled_reason = $3 WHERE id = $1`
	case domain.OrderStatusWaitingPayment, domain.OrderStatusPaid:
		return fmt.Errorf("transitioning order with restock: invalid target status %q", targetStatus)
	default:
		return fmt.Errorf("transitioning order with restock: unexpected target status %q", targetStatus)
	}

	if err := restockOrderQuota(ctx, tx, []uuid.UUID{order.ID}); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, updateSQL, order.ID, nowUTC, reason); err != nil {
		return fmt.Errorf("updating order to %s: %w", targetStatus, err)
	}

	prev := string(order.Status)
	reasonCopy := reason
	tsCopy := nowUTC
	order.Status = targetStatus
	order.CancelledReason = &reasonCopy
	if targetStatus == domain.OrderStatusExpired {
		order.ExpiredAt = &tsCopy
	} else {
		order.CancelledAt = &tsCopy
	}
	return insertOrderLog(ctx, tx, order.ID, &prev, string(targetStatus), actorID, actorType, reason, nowUTC)
}

func markOrderExpiredAndRestock(ctx context.Context, tx pgx.Tx, order *domain.Order, actorType string, nowUTC time.Time) error {
	return restockAndTransitionOrder(ctx, tx, order, domain.OrderStatusExpired, nil, actorType, expiryReasonLazy, nowUTC)
}

func normalizeFailureReason(reason string) string {
	clean := strings.ReplaceAll(reason, "\x00", "")
	clean = strings.ToValidUTF8(clean, "")
	clean = strings.TrimSpace(clean)
	if clean == "" {
		return defaultFailureReason
	}
	runes := 0
	for idx := range clean {
		if runes == maxFailureReasonRunes {
			return clean[:idx]
		}
		runes++
	}
	return clean
}

func classifyFailureTransition(order *domain.Order, reason string, nowUTC time.Time) (domain.OrderStatus, error) {
	if order == nil || nowUTC.IsZero() || order.ExpiresAt.IsZero() || order.Status != domain.OrderStatusWaitingPayment {
		return "", domain.NewInvalidOrderStateError("order cannot be marked failed in its current state")
	}
	if nowUTC.After(order.ExpiresAt) || isGatewayExpireReason(reason) {
		return domain.OrderStatusExpired, nil
	}
	return domain.OrderStatusCancelled, nil
}

func isGatewayExpireReason(reason string) bool {
	trimmed := strings.TrimSpace(reason)
	return strings.EqualFold(trimmed, "gateway status: expire") || strings.EqualFold(trimmed, "expire")
}

func restockOrderQuota(ctx context.Context, tx pgx.Tx, orderIDs []uuid.UUID) error {
	// Lock ticket_types in ascending ID order before mutating balances to prevent AB-BA deadlocks.
	const lockSQL = `
		SELECT tt.id
		FROM ticket_types AS tt
		WHERE tt.id IN (
			SELECT DISTINCT oi.ticket_type_id
			FROM order_items AS oi
			WHERE oi.order_id = ANY($1)
		)
		ORDER BY tt.id ASC
		FOR UPDATE`
	if _, err := tx.Exec(ctx, lockSQL, orderIDs); err != nil {
		return fmt.Errorf("locking ticket types for restock: %w", err)
	}

	const updateSQL = `
		UPDATE ticket_types AS tt
		SET remaining_quota = tt.remaining_quota + agg.qty
		FROM (
			SELECT oi.ticket_type_id, SUM(oi.quantity)::int AS qty
			FROM order_items AS oi
			WHERE oi.order_id = ANY($1)
			GROUP BY oi.ticket_type_id
			ORDER BY oi.ticket_type_id ASC
		) AS agg
		WHERE tt.id = agg.ticket_type_id`
	if _, err := tx.Exec(ctx, updateSQL, orderIDs); err != nil {
		return fmt.Errorf("restocking ticket quota: %w", err)
	}
	return nil
}

func insertOrderLog(ctx context.Context, tx pgx.Tx, orderID uuid.UUID, fromStatus *string, toStatus string, actorID *uuid.UUID, actorType, note string, nowUTC time.Time) error {
	return insertOrderLogEntry(ctx, tx, domain.OrderLog{
		ID:         uuid.New(),
		OrderID:    orderID,
		FromStatus: fromStatus,
		ToStatus:   toStatus,
		ActorID:    actorID,
		ActorType:  actorType,
		Note:       note,
		CreatedAt:  nowUTC,
	})
}

func insertOrderLogEntry(ctx context.Context, tx pgx.Tx, entry domain.OrderLog) error {
	const sqlLog = `
		INSERT INTO order_logs (id, order_id, from_status, to_status, actor_id, actor_type, note, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`
	if _, err := tx.Exec(ctx, sqlLog,
		entry.ID, entry.OrderID, entry.FromStatus, entry.ToStatus, entry.ActorID, entry.ActorType, entry.Note, entry.CreatedAt,
	); err != nil {
		return fmt.Errorf("inserting order log: %w", err)
	}
	return nil
}

func loadOrderDetailsInTx(ctx context.Context, q queryRunner, order *domain.Order) error {
	itemsMap, err := loadOrderItemsByOrderIDs(ctx, q, []uuid.UUID{order.ID})
	if err != nil {
		return err
	}
	ticketsMap, err := loadOrderTicketsByOrderIDs(ctx, q, []uuid.UUID{order.ID})
	if err != nil {
		return err
	}
	logs, err := loadOrderLogs(ctx, q, order.ID)
	if err != nil {
		return err
	}

	order.Items = nonNilSlice(itemsMap[order.ID])
	order.Tickets = nonNilSlice(ticketsMap[order.ID])
	order.Logs = nonNilSlice(logs)
	return nil
}

func queryBuyerOrders(ctx context.Context, q queryRunner, buyerID uuid.UUID) ([]domain.Order, []uuid.UUID, map[uuid.UUID]int, error) {
	const sqlOrders = "SELECT " + orderSelectColumns + " FROM orders WHERE buyer_id = $1 ORDER BY created_at DESC, id DESC"
	rows, err := q.Query(ctx, sqlOrders, buyerID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("listing buyer orders: %w", err)
	}
	defer rows.Close()

	orders := make([]domain.Order, 0)
	orderIDs := make([]uuid.UUID, 0)
	orderIdx := make(map[uuid.UUID]int)
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("scanning buyer order: %w", err)
		}
		o.Items = []domain.OrderItem{}
		o.Tickets = []domain.Ticket{}
		o.Logs = []domain.OrderLog{}
		orderIdx[o.ID] = len(orders)
		orderIDs = append(orderIDs, o.ID)
		orders = append(orders, o)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, nil, fmt.Errorf("iterating buyer orders: %w", err)
	}
	rows.Close()
	return orders, orderIDs, orderIdx, nil
}

func scanOrder(row pgx.Row) (domain.Order, error) {
	var o domain.Order
	var status string
	err := row.Scan(
		&o.ID, &o.InvoiceNumber, &o.EventID, &o.BuyerID, &o.IdempotencyKey, &o.TotalTickets, &o.TotalAmountIDR,
		&status, &o.ExpiresAt, &o.PaidAt, &o.CancelledAt, &o.ExpiredAt, &o.CancelledReason, &o.CreatedAt,
	)
	if err != nil {
		return domain.Order{}, err
	}
	o.Status = domain.OrderStatus(status)
	return o, nil
}

func normalizeIdempotencyKey(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	if utf8.RuneCountInString(trimmed) > maxIdempotencyKeyRunes {
		return "", domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeValidation, "Idempotency-Key must be at most 128 characters")
	}
	return trimmed, nil
}

func isIdempotencyKeyConflict(err error) bool {
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		return pgErr.Code == pgUniqueViolationSQLCode && pgErr.ConstraintName == idempotencyIndexName
	}
	return false
}

func findOrderByIdempotencyKeyInTx(ctx context.Context, q queryRunner, buyerID uuid.UUID, key string) (*domain.Order, error) {
	const sqlFind = "SELECT " + orderSelectColumns + " FROM orders WHERE buyer_id = $1 AND idempotency_key = $2"
	row := q.QueryRow(ctx, sqlFind, buyerID, key)
	o, err := scanOrder(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("looking up order by idempotency key: %w", err)
	}
	if err := loadOrderDetailsInTx(ctx, q, &o); err != nil {
		return nil, err
	}
	return &o, nil
}

func (r *OrderRepo) findOrderByIdempotencyKey(ctx context.Context, buyerID uuid.UUID, key string) (*domain.Order, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return nil, fmt.Errorf("beginning idempotency lookup tx: %w", err)
	}
	defer rollbackTx(tx)

	existing, err := findOrderByIdempotencyKeyInTx(ctx, tx, buyerID, key)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing idempotency lookup tx: %w", err)
	}
	return existing, nil
}

func loadOrderItemsByOrderIDs(ctx context.Context, q queryRunner, orderIDs []uuid.UUID) (map[uuid.UUID][]domain.OrderItem, error) {
	const sqlItems = `
		SELECT oi.id, oi.order_id, oi.ticket_type_id, tt.name, oi.quantity, oi.unit_price_idr, oi.subtotal_idr
		FROM order_items AS oi
		JOIN ticket_types AS tt ON tt.id = oi.ticket_type_id
		WHERE oi.order_id = ANY($1)
		ORDER BY tt.price_idr ASC, oi.ticket_type_id ASC`
	rows, err := q.Query(ctx, sqlItems, orderIDs)
	if err != nil {
		return nil, fmt.Errorf("querying order items: %w", err)
	}
	defer rows.Close()

	grouped := make(map[uuid.UUID][]domain.OrderItem, len(orderIDs))
	for rows.Next() {
		var it domain.OrderItem
		if err := rows.Scan(&it.ID, &it.OrderID, &it.TicketTypeID, &it.TicketTypeName, &it.Quantity, &it.UnitPriceIDR, &it.SubtotalIDR); err != nil {
			return nil, fmt.Errorf("scanning order item: %w", err)
		}
		grouped[it.OrderID] = append(grouped[it.OrderID], it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating order items: %w", err)
	}
	rows.Close()
	return grouped, nil
}

func loadOrderTicketsByOrderIDs(ctx context.Context, q queryRunner, orderIDs []uuid.UUID) (map[uuid.UUID][]domain.Ticket, error) {
	const sqlTickets = `
		SELECT id, ticket_code, order_id, order_item_id, event_id, ticket_type_id, owner_id, seat_sequence, issued_at
		FROM tickets
		WHERE order_id = ANY($1)
		ORDER BY issued_at ASC, order_item_id ASC, seat_sequence ASC`
	rows, err := q.Query(ctx, sqlTickets, orderIDs)
	if err != nil {
		return nil, fmt.Errorf("querying order tickets: %w", err)
	}
	defer rows.Close()

	grouped := make(map[uuid.UUID][]domain.Ticket, len(orderIDs))
	for rows.Next() {
		var tk domain.Ticket
		if err := rows.Scan(&tk.ID, &tk.TicketCode, &tk.OrderID, &tk.OrderItemID, &tk.EventID, &tk.TicketTypeID, &tk.OwnerID, &tk.SeatSequence, &tk.IssuedAt); err != nil {
			return nil, fmt.Errorf("scanning order ticket: %w", err)
		}
		grouped[tk.OrderID] = append(grouped[tk.OrderID], tk)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating order tickets: %w", err)
	}
	rows.Close()
	return grouped, nil
}

func loadOrderLogs(ctx context.Context, q queryRunner, orderID uuid.UUID) ([]domain.OrderLog, error) {
	const sqlLogs = `
		SELECT id, order_id, from_status, to_status, actor_id, actor_type, note, created_at
		FROM order_logs
		WHERE order_id = $1
		ORDER BY created_at ASC, id ASC`
	rows, err := q.Query(ctx, sqlLogs, orderID)
	if err != nil {
		return nil, fmt.Errorf("querying order logs: %w", err)
	}
	defer rows.Close()

	logs := make([]domain.OrderLog, 0)
	for rows.Next() {
		var lg domain.OrderLog
		if err := rows.Scan(&lg.ID, &lg.OrderID, &lg.FromStatus, &lg.ToStatus, &lg.ActorID, &lg.ActorType, &lg.Note, &lg.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning order log: %w", err)
		}
		logs = append(logs, lg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating order logs: %w", err)
	}
	rows.Close()
	return logs, nil
}

func nonNilSlice[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func generateTicketCode(now time.Time) (string, error) {
	buf := make([]byte, 5)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generating ticket code randomness: %w", err)
	}
	return "TKT-" + now.UTC().Format("060102") + "-" + strings.ToUpper(hex.EncodeToString(buf)), nil
}
