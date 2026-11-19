package repository

import (
	"context"
	"errors"
	"fmt"
	"net/http"

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
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return nil, fmt.Errorf("beginning list events tx: %w", err)
	}
	defer rollbackTx(tx)

	const queryEvents = `
		SELECT id, organizer_id, title, description, venue, event_date, created_at
		FROM events
		ORDER BY event_date ASC, created_at ASC`
	rows, err := tx.Query(ctx, queryEvents)
	if err != nil {
		return nil, fmt.Errorf("listing events: %w", err)
	}
	defer rows.Close()

	events := make([]domain.Event, 0)
	eventIDs := make([]uuid.UUID, 0)
	eventIdx := make(map[uuid.UUID]int)
	for rows.Next() {
		var ev domain.Event
		if err := rows.Scan(&ev.ID, &ev.OrganizerID, &ev.Title, &ev.Description, &ev.Venue, &ev.EventDate, &ev.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning event: %w", err)
		}
		ev.TicketTypes = []domain.TicketType{}
		eventIdx[ev.ID] = len(events)
		eventIDs = append(eventIDs, ev.ID)
		events = append(events, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating events: %w", err)
	}
	// Close rows before querying tiers on the same transaction connection.
	rows.Close()

	if len(events) == 0 {
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("committing list events tx: %w", err)
		}
		return []domain.Event{}, nil
	}

	const queryTiers = `
		SELECT id, event_id, name, price_idr, total_quota, remaining_quota, created_at
		FROM ticket_types
		WHERE event_id = ANY($1)
		ORDER BY price_idr ASC, name ASC`
	tierRows, err := tx.Query(ctx, queryTiers, eventIDs)
	if err != nil {
		return nil, fmt.Errorf("listing ticket types: %w", err)
	}
	defer tierRows.Close()

	for tierRows.Next() {
		tt, err := scanTicketType(tierRows)
		if err != nil {
			return nil, fmt.Errorf("scanning ticket type: %w", err)
		}
		if idx, ok := eventIdx[tt.EventID]; ok {
			events[idx].TicketTypes = append(events[idx].TicketTypes, tt)
		}
	}
	if err := tierRows.Err(); err != nil {
		return nil, fmt.Errorf("iterating ticket types: %w", err)
	}
	tierRows.Close()

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing list events tx: %w", err)
	}
	return events, nil
}

// FindByID retrieves a single event with its ticket tiers from a consistent repeatable-read snapshot.
func (r *EventRepo) FindByID(ctx context.Context, id uuid.UUID) (*domain.Event, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return nil, fmt.Errorf("beginning find event tx: %w", err)
	}
	defer rollbackTx(tx)

	const queryEvent = `
		SELECT id, organizer_id, title, description, venue, event_date, created_at
		FROM events
		WHERE id = $1`
	var ev domain.Event
	err = tx.QueryRow(ctx, queryEvent, id).Scan(
		&ev.ID, &ev.OrganizerID, &ev.Title, &ev.Description, &ev.Venue, &ev.EventDate, &ev.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.NewDomainError(http.StatusNotFound, domain.ErrCodeEventNotFound, "event not found")
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

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing find event tx: %w", err)
	}
	return &ev, nil
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
	if err == nil {
		return nil
	}
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok &&
		pgErr.Code == pgUniqueViolationCode &&
		(pgErr.ConstraintName == ticketTypesEventNameConstraint || pgErr.ConstraintName == ticketTypesLowerNameIndex) {
		return domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeValidation, "duplicate ticket type name within event")
	}
	return fmt.Errorf("inserting ticket type: %w", err)
}
