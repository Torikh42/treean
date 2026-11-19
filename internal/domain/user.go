package domain

import (
	"log/slog"
	"time"

	"github.com/google/uuid"
)

const redactedValue = "[REDACTED]"

// Role enumerates system authorization roles.
type Role string

// RoleOrganizer and RoleBuyer define valid authorization roles for system users.
const (
	RoleOrganizer Role = "ORGANIZER"
	RoleBuyer     Role = "BUYER"
)

// User represents a persisted organizer or ticket buyer account.
type User struct {
	ID           uuid.UUID `json:"id"`
	Email        string    `json:"email"`
	Name         string    `json:"name"`
	PasswordHash string    `json:"-"`
	Role         Role      `json:"role"`
	CreatedAt    time.Time `json:"created_at"`
}

// LogValue implements slog.LogValuer and redacts PasswordHash from structured logs.
func (u User) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("id", u.ID.String()),
		slog.String("email", u.Email),
		slog.String("name", u.Name),
		slog.String("password_hash", redactedValue),
		slog.String("role", string(u.Role)),
		slog.Time("created_at", u.CreatedAt),
	)
}

// Actor identifies the authenticated caller attached to request context.
type Actor struct {
	ID    uuid.UUID
	Email string
	Role  Role
}
