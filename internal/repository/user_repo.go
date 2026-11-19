package repository

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/torikh42/treean/internal/domain"
)

const (
	pgUniqueViolationCode = "23505"
	usersEmailConstraint  = "users_email_key"
)

// UserRepo implements PostgreSQL user operations.
type UserRepo struct {
	pool *pgxpool.Pool
}

// NewUserRepo constructs a UserRepo.
func NewUserRepo(pool *pgxpool.Pool) *UserRepo {
	return &UserRepo{pool: pool}
}

// Create inserts a new registered user and translates unique email violations to EMAIL_ALREADY_EXISTS.
func (r *UserRepo) Create(ctx context.Context, u *domain.User) error {
	if u == nil {
		return errors.New("inserting user: nil user")
	}

	u.Email = normalizeEmail(u.Email)
	if u.ID == uuid.Nil {
		u.ID = uuid.New()
	}

	const query = `
		INSERT INTO users (id, email, name, password_hash, role)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING created_at`

	err := r.pool.QueryRow(ctx, query, u.ID, u.Email, u.Name, u.PasswordHash, string(u.Role)).Scan(&u.CreatedAt)
	if err != nil {
		return mapUserCreateError(err)
	}
	return nil
}

// FindByEmail retrieves a user by normalized email address.
func (r *UserRepo) FindByEmail(ctx context.Context, email string) (*domain.User, error) {
	const query = `SELECT id, email, name, password_hash, role, created_at FROM users WHERE email = $1`
	return r.findOne(
		ctx,
		query,
		normalizeEmail(email),
		domain.NewDomainError(http.StatusUnauthorized, domain.ErrCodeInvalidCredentials, "invalid email or password"),
		"querying user by email",
	)
}

// FindByID retrieves a user by UUID.
func (r *UserRepo) FindByID(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	const query = `SELECT id, email, name, password_hash, role, created_at FROM users WHERE id = $1`
	return r.findOne(
		ctx,
		query,
		id,
		domain.NewDomainError(http.StatusUnauthorized, domain.ErrCodeUnauthorized, "user not found"),
		"querying user by id",
	)
}

// EnsureDemoUser idempotently ensures a synthetic header-auth user exists so FK constraints stay satisfied.
func (r *UserRepo) EnsureDemoUser(ctx context.Context, id uuid.UUID, role domain.Role) error {
	if err := validateDemoUserInput(id, role); err != nil {
		return err
	}

	const query = `
		INSERT INTO users (id, email, name, password_hash, role)
		VALUES ($1, $2, $3, '', $4)
		ON CONFLICT DO NOTHING`

	email := "demo-" + id.String() + "@event.local"
	name := "Demo " + string(role)
	if _, err := r.pool.Exec(ctx, query, id, email, name, string(role)); err != nil {
		return fmt.Errorf("ensuring demo user: %w", err)
	}
	return nil
}

func (r *UserRepo) findOne(ctx context.Context, query string, arg any, notFoundErr error, op string) (*domain.User, error) {
	var u domain.User
	var role string
	err := r.pool.QueryRow(ctx, query, arg).Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash, &role, &u.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFoundErr
		}
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	u.Role = domain.Role(role)
	return &u, nil
}

// normalizeEmail trims whitespace and lowercases email addresses for case-insensitive matching.
func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// validateDemoUserInput verifies required synthetic demo user attributes before issuing SQL.
func validateDemoUserInput(id uuid.UUID, role domain.Role) error {
	if id == uuid.Nil {
		return errors.New("ensuring demo user: id is required")
	}
	if role != domain.RoleOrganizer && role != domain.RoleBuyer {
		return fmt.Errorf("ensuring demo user: invalid role %q", role)
	}
	return nil
}

// mapUserCreateError translates unique email violations into EMAIL_ALREADY_EXISTS domain errors.
func mapUserCreateError(err error) error {
	if err == nil {
		return nil
	}
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok &&
		pgErr.Code == pgUniqueViolationCode &&
		pgErr.ConstraintName == usersEmailConstraint {
		return domain.NewDomainError(http.StatusConflict, domain.ErrCodeEmailAlreadyExists, "email is already registered")
	}
	return fmt.Errorf("inserting user: %w", err)
}
