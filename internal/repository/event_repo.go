package repository

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/torikh42/treean/internal/domain"
)

const (
	pgForeignKeyViolationCode      = "23503"
	eventsOrganizerFKConstraint    = "events_organizer_id_fkey"
	ticketTypesEventNameConstraint = "ticket_types_event_id_name_key"
	ticketTypesLowerNameIndex      = "uidx_ticket_types_event_lower_name"
	errMsgEventNotFound            = "event not found"
)

// EventRepo implements PostgreSQL event and ticket tier queries.
type EventRepo struct {
	pool *pgxpool.Pool
}

// NewEventRepo constructs an EventRepo.
func NewEventRepo(pool *pgxpool.Pool) *EventRepo {
	return &EventRepo{pool: pool}
}

// CreateWithTiers atomically inserts an event and all of its ticket types.
func (r *EventRepo) CreateWithTiers(ctx context.Context, ev *domain.Event) error {
	if ev == nil {
		return errors.New("creating event: nil event")
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning create event tx: %w", err)
	}
	defer rollbackTx(tx)

	if ev.ID == uuid.Nil {
		ev.ID = uuid.New()
	}

	const insertEventSQL = `
		INSERT INTO events (id, organizer_id, title, description, venue, event_date)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING created_at`
	if err := tx.QueryRow(ctx, insertEventSQL,
		ev.ID, ev.OrganizerID, ev.Title, ev.Description, ev.Venue, ev.EventDate,
	).Scan(&ev.CreatedAt); err != nil {
		return mapEventCreateError(err)
	}

	const insertTierSQL = `
		INSERT INTO ticket_types (id, event_id, name, price_idr, total_quota, remaining_quota)
		VALUES ($1, $2, $3, $4, $5, $5)
		RETURNING created_at`
	for i := range ev.TicketTypes {
		if ev.TicketTypes[i].ID == uuid.Nil {
			ev.TicketTypes[i].ID = uuid.New()
		}
		ev.TicketTypes[i].EventID = ev.ID
		ev.TicketTypes[i].RemainingQuota = ev.TicketTypes[i].TotalQuota

		if err := tx.QueryRow(ctx, insertTierSQL,
			ev.TicketTypes[i].ID, ev.ID, ev.TicketTypes[i].Name,
			ev.TicketTypes[i].PriceIDR, ev.TicketTypes[i].TotalQuota,
		).Scan(&ev.TicketTypes[i].CreatedAt); err != nil {
			return mapTicketTypeCreateError(err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing create event tx: %w", err)
	}
	return nil
}

// ListAll retrieves all events along with their ticket tiers from a consistent repeatable-read snapshot.
func (r *EventRepo) ListAll(ctx context.Context) ([]domain.Event, error) {
	const queryEvents = `
		SELECT id, organizer_id, title, description, venue, event_date, created_at
		FROM events
		ORDER BY event_date ASC, created_at ASC`
	return r.listEventsWithQuery(ctx, "listing events", queryEvents)
}

// ListByOrganizer retrieves events owned by a specific organizer along with their ticket tiers.
func (r *EventRepo) ListByOrganizer(ctx context.Context, organizerID uuid.UUID) ([]domain.Event, error) {
	if organizerID == uuid.Nil {
		return []domain.Event{}, nil
	}
	const queryEvents = `
		SELECT id, organizer_id, title, description, venue, event_date, created_at
		FROM events
		WHERE organizer_id = $1
		ORDER BY event_date ASC, created_at ASC`
	return r.listEventsWithQuery(ctx, "listing organizer events", queryEvents, organizerID)
}

func (r *EventRepo) listEventsWithQuery(ctx context.Context, op, queryEvents string, args ...any) ([]domain.Event, error) {
	if r == nil || r.pool == nil {
		return nil, fmt.Errorf("%s: repository pool is nil", op)
	}

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return nil, fmt.Errorf("beginning %s tx: %w", op, err)
	}
	defer rollbackTx(tx)

	events, eventIDs, eventIdx, err := scanEventsInTx(ctx, tx, queryEvents, args...)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("committing %s tx: %w", op, err)
		}
		return []domain.Event{}, nil
	}

	if err := populateEventTiersInTx(ctx, tx, eventIDs, eventIdx, events); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing %s tx: %w", op, err)
	}
	return events, nil
}

func scanEventsInTx(
	ctx context.Context,
	tx pgx.Tx,
	queryEvents string,
	args ...any,
) ([]domain.Event, []uuid.UUID, map[uuid.UUID]int, error) {
	rows, err := tx.Query(ctx, queryEvents, args...)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("listing events: %w", err)
	}
	defer rows.Close()

	events := make([]domain.Event, 0)
	eventIDs := make([]uuid.UUID, 0)
	eventIdx := make(map[uuid.UUID]int)
	for rows.Next() {
		var ev domain.Event
		if err := rows.Scan(&ev.ID, &ev.OrganizerID, &ev.Title, &ev.Description, &ev.Venue, &ev.EventDate, &ev.CreatedAt); err != nil {
			return nil, nil, nil, fmt.Errorf("scanning event: %w", err)
		}
		ev.TicketTypes = []domain.TicketType{}
		eventIdx[ev.ID] = len(events)
		eventIDs = append(eventIDs, ev.ID)
		events = append(events, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, nil, fmt.Errorf("iterating events: %w", err)
	}
	// Close rows before querying tiers on the same transaction connection.
	rows.Close()
	return events, eventIDs, eventIdx, nil
}

func populateEventTiersInTx(
	ctx context.Context,
	tx pgx.Tx,
	eventIDs []uuid.UUID,
	eventIdx map[uuid.UUID]int,
	events []domain.Event,
) error {
	const queryTiers = `
		SELECT id, event_id, name, price_idr, total_quota, remaining_quota, created_at
		FROM ticket_types
		WHERE event_id = ANY($1)
		ORDER BY price_idr ASC, name ASC`
	tierRows, err := tx.Query(ctx, queryTiers, eventIDs)
	if err != nil {
		return fmt.Errorf("listing ticket types: %w", err)
	}
	defer tierRows.Close()

	for tierRows.Next() {
		tt, err := scanTicketType(tierRows)
		if err != nil {
			return fmt.Errorf("scanning ticket type: %w", err)
		}
		if idx, ok := eventIdx[tt.EventID]; ok {
			events[idx].TicketTypes = append(events[idx].TicketTypes, tt)
		}
	}
	if err := tierRows.Err(); err != nil {
		return fmt.Errorf("iterating ticket types: %w", err)
	}
	tierRows.Close()
	return nil
}

// FindByID retrieves a single event with its ticket tiers from a consistent repeatable-read snapshot.
func (r *EventRepo) FindByID(ctx context.Context, id uuid.UUID) (*domain.Event, error) {
	if r == nil || r.pool == nil {
		return nil, errors.New("finding event: repository pool is nil")
	}

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return nil, fmt.Errorf("beginning find event tx: %w", err)
	}
	defer rollbackTx(tx)

	ev, err := loadEventInTx(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing find event tx: %w", err)
	}
	return ev, nil
}

// UpdateWithTiers atomically updates an organizer-owned event and reconciles its ticket tiers under row locks.
func (r *EventRepo) UpdateWithTiers(ctx context.Context, organizerID uuid.UUID, ev *domain.Event) error {
	if r == nil || r.pool == nil {
		return errors.New("updating event: repository pool is nil")
	}
	if ev == nil {
		return errors.New("updating event: nil event")
	}
	if ev.ID == uuid.Nil {
		return domain.NewDomainError(http.StatusNotFound, domain.ErrCodeEventNotFound, errMsgEventNotFound)
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning update event tx: %w", err)
	}
	defer rollbackTx(tx)

	if err := lockEventForOrganizer(ctx, tx, ev.ID, organizerID, "cannot modify another organizer's event"); err != nil {
		return err
	}

	existingMap, err := loadLockedTiers(ctx, tx, ev.ID)
	if err != nil {
		return err
	}

	keptIDs, err := validateTierUpdates(existingMap, ev.TicketTypes)
	if err != nil {
		return err
	}
	if err := deleteRemovedTiers(ctx, tx, ev.ID, existingMap, keptIDs); err != nil {
		return err
	}

	const updateEventSQL = `
		UPDATE events
		SET title = $2, description = $3, venue = $4, event_date = $5
		WHERE id = $1`
	if _, err := tx.Exec(ctx, updateEventSQL, ev.ID, ev.Title, ev.Description, ev.Venue, ev.EventDate); err != nil {
		return fmt.Errorf("updating event: %w", err)
	}

	if err := applyTierChanges(ctx, tx, ev.ID, existingMap, ev.TicketTypes); err != nil {
		return err
	}

	refreshed, err := loadEventInTx(ctx, tx, ev.ID)
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing update event tx: %w", err)
	}
	*ev = *refreshed
	return nil
}

// DeleteUnused deletes an organizer-owned event only when no orders exist for it.
func (r *EventRepo) DeleteUnused(ctx context.Context, organizerID, eventID uuid.UUID) error {
	if r == nil || r.pool == nil {
		return errors.New("deleting event: repository pool is nil")
	}
	if eventID == uuid.Nil {
		return domain.NewDomainError(http.StatusNotFound, domain.ErrCodeEventNotFound, errMsgEventNotFound)
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning delete event tx: %w", err)
	}
	defer rollbackTx(tx)

	if err := lockEventForOrganizer(ctx, tx, eventID, organizerID, "cannot delete another organizer's event"); err != nil {
		return err
	}

	var hasOrders bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM orders WHERE event_id = $1)", eventID).Scan(&hasOrders); err != nil {
		return fmt.Errorf("checking event orders: %w", err)
	}
	if hasOrders {
		return domain.NewDomainError(http.StatusConflict, domain.ErrCodeEventHasOrders, "cannot delete event with existing orders")
	}

	if _, err := tx.Exec(ctx, "DELETE FROM events WHERE id = $1", eventID); err != nil {
		return fmt.Errorf("deleting event: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing delete event tx: %w", err)
	}
	return nil
}

func loadEventInTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.Event, error) {
	const queryEvent = `
		SELECT id, organizer_id, title, description, venue, event_date, created_at
		FROM events
		WHERE id = $1`
	var ev domain.Event
	err := tx.QueryRow(ctx, queryEvent, id).Scan(
		&ev.ID, &ev.OrganizerID, &ev.Title, &ev.Description, &ev.Venue, &ev.EventDate, &ev.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.NewDomainError(http.StatusNotFound, domain.ErrCodeEventNotFound, errMsgEventNotFound)
		}
		return nil, fmt.Errorf("querying event: %w", err)
	}
	ev.TicketTypes = []domain.TicketType{}

	const queryTiers = `
		SELECT id, event_id, name, price_idr, total_quota, remaining_quota, created_at
		FROM ticket_types
		WHERE event_id = $1
		ORDER BY price_idr ASC, name ASC`
	rows, err := tx.Query(ctx, queryTiers, id)
	if err != nil {
		return nil, fmt.Errorf("querying event ticket types: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		tt, err := scanTicketType(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning event ticket type: %w", err)
		}
		ev.TicketTypes = append(ev.TicketTypes, tt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating event ticket types: %w", err)
	}
	rows.Close()
	return &ev, nil
}

func lockEventForOrganizer(ctx context.Context, tx pgx.Tx, eventID, organizerID uuid.UUID, forbiddenMsg string) error {
	var dbOrganizerID uuid.UUID
	err := tx.QueryRow(ctx, "SELECT organizer_id FROM events WHERE id = $1 FOR UPDATE", eventID).Scan(&dbOrganizerID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.NewDomainError(http.StatusNotFound, domain.ErrCodeEventNotFound, errMsgEventNotFound)
		}
		return fmt.Errorf("locking event: %w", err)
	}
	if dbOrganizerID != organizerID {
		return domain.NewDomainError(http.StatusForbidden, domain.ErrCodeForbidden, forbiddenMsg)
	}
	return nil
}

func loadLockedTiers(ctx context.Context, tx pgx.Tx, eventID uuid.UUID) (map[uuid.UUID]domain.TicketType, error) {
	const queryLockedTiers = `
		SELECT id, event_id, name, price_idr, total_quota, remaining_quota, created_at
		FROM ticket_types
		WHERE event_id = $1
		ORDER BY id ASC
		FOR UPDATE`
	rows, err := tx.Query(ctx, queryLockedTiers, eventID)
	if err != nil {
		return nil, fmt.Errorf("locking ticket types: %w", err)
	}
	defer rows.Close()

	existing := make(map[uuid.UUID]domain.TicketType)
	for rows.Next() {
		tt, err := scanTicketType(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning locked ticket type: %w", err)
		}
		existing[tt.ID] = tt
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating locked ticket types: %w", err)
	}
	rows.Close()
	return existing, nil
}

func validateTierUpdates(existingMap map[uuid.UUID]domain.TicketType, tiers []domain.TicketType) (map[uuid.UUID]struct{}, error) {
	kept := make(map[uuid.UUID]struct{}, len(tiers))
	for _, tt := range tiers {
		if tt.ID == uuid.Nil {
			continue
		}
		existing, ok := existingMap[tt.ID]
		if !ok {
			return nil, domain.NewDomainError(http.StatusNotFound, domain.ErrCodeTicketTypeNotFound, "ticket type not found for event")
		}
		soldOrHeld := existing.TotalQuota - existing.RemainingQuota
		if tt.TotalQuota < soldOrHeld {
			return nil, domain.NewDomainError(
				http.StatusConflict,
				domain.ErrCodeQuotaConflict,
				fmt.Sprintf("total_quota for %q cannot be less than already reserved or sold tickets (%d)", existing.Name, soldOrHeld),
			)
		}
		kept[tt.ID] = struct{}{}
	}
	return kept, nil
}

func deleteRemovedTiers(
	ctx context.Context,
	tx pgx.Tx,
	eventID uuid.UUID,
	existingMap map[uuid.UUID]domain.TicketType,
	keptIDs map[uuid.UUID]struct{},
) error {
	removed := make([]uuid.UUID, 0)
	for id := range existingMap {
		if _, ok := keptIDs[id]; !ok {
			removed = append(removed, id)
		}
	}
	slices.SortFunc(removed, func(a, b uuid.UUID) int {
		return bytes.Compare(a[:], b[:])
	})

	for _, id := range removed {
		var hasOrders bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM order_items WHERE ticket_type_id = $1)", id).Scan(&hasOrders); err != nil {
			return fmt.Errorf("checking ticket tier orders: %w", err)
		}
		if hasOrders {
			return domain.NewDomainError(http.StatusConflict, domain.ErrCodeTierHasOrders, "cannot remove ticket tier with existing orders")
		}
		if _, err := tx.Exec(ctx, "DELETE FROM ticket_types WHERE id = $1 AND event_id = $2", id, eventID); err != nil {
			return fmt.Errorf("deleting ticket tier: %w", err)
		}
	}
	return nil
}

func applyTierChanges(
	ctx context.Context,
	tx pgx.Tx,
	eventID uuid.UUID,
	existingMap map[uuid.UUID]domain.TicketType,
	tiers []domain.TicketType,
) error {
	const stageTierSQL = `
		UPDATE ticket_types
		SET name = $2, price_idr = $3, total_quota = $4, remaining_quota = $5
		WHERE id = $1 AND event_id = $6`
	const finalizeTierNameSQL = `
		UPDATE ticket_types
		SET name = $2
		WHERE id = $1 AND event_id = $3`
	const insertTierSQL = `
		INSERT INTO ticket_types (id, event_id, name, price_idr, total_quota, remaining_quota)
		VALUES ($1, $2, $3, $4, $5, $5)`

	// Phase 1: move kept existing tiers to unique temporary names while updating price and quota
	// so swapping tier names or reusing an old tier name for a new tier never hits immediate unique checks.
	for _, tt := range tiers {
		if tt.ID == uuid.Nil {
			continue
		}
		existing := existingMap[tt.ID]
		soldOrHeld := existing.TotalQuota - existing.RemainingQuota
		newRemaining := tt.TotalQuota - soldOrHeld
		tmpName := "__tmp_" + uuid.NewString()
		if _, err := tx.Exec(ctx, stageTierSQL, tt.ID, tmpName, tt.PriceIDR, tt.TotalQuota, newRemaining, eventID); err != nil {
			return mapTicketTypeWriteError("staging ticket type", err)
		}
	}

	// Phase 2: apply final names for kept existing tiers.
	for _, tt := range tiers {
		if tt.ID == uuid.Nil {
			continue
		}
		if _, err := tx.Exec(ctx, finalizeTierNameSQL, tt.ID, tt.Name, eventID); err != nil {
			return mapTicketTypeWriteError("updating ticket type", err)
		}
	}

	// Phase 3: insert new tiers.
	for _, tt := range tiers {
		if tt.ID != uuid.Nil {
			continue
		}
		if _, err := tx.Exec(ctx, insertTierSQL, uuid.New(), eventID, tt.Name, tt.PriceIDR, tt.TotalQuota); err != nil {
			return mapTicketTypeWriteError("inserting ticket type", err)
		}
	}
	return nil
}

func scanTicketType(row pgx.Row) (domain.TicketType, error) {
	var tt domain.TicketType
	err := row.Scan(&tt.ID, &tt.EventID, &tt.Name, &tt.PriceIDR, &tt.TotalQuota, &tt.RemainingQuota, &tt.CreatedAt)
	return tt, err
}

// mapEventCreateError translates missing organizer foreign key violations into UNAUTHORIZED domain errors.
func mapEventCreateError(err error) error {
	if err == nil {
		return nil
	}
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok &&
		pgErr.Code == pgForeignKeyViolationCode &&
		pgErr.ConstraintName == eventsOrganizerFKConstraint {
		return domain.NewDomainError(http.StatusUnauthorized, domain.ErrCodeUnauthorized, "organizer not found")
	}
	return fmt.Errorf("inserting event: %w", err)
}

// mapTicketTypeCreateError translates duplicate tier name violations into a 422 validation domain error.
func mapTicketTypeCreateError(err error) error {
	return mapTicketTypeWriteError("inserting ticket type", err)
}

func mapTicketTypeWriteError(op string, err error) error {
	if err == nil {
		return nil
	}
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok &&
		pgErr.Code == pgUniqueViolationCode &&
		(pgErr.ConstraintName == ticketTypesEventNameConstraint || pgErr.ConstraintName == ticketTypesLowerNameIndex) {
		return domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeValidation, "duplicate ticket type name within event")
	}
	return fmt.Errorf("%s: %w", op, err)
}
