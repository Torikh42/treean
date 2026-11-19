# High-Concurrency Event Ticketing API Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a PostgreSQL-backed high-concurrency Event Ticketing REST API in Go featuring 2+ ticket tiers per event, 1 to 4 ticket checkout with 15-minute quota hold, per-buyer 4-ticket event caps, manual/scheduled/lazy quota recovery, idempotent SHA-512 payment webhooks, unique ticket code issuance, OpenAPI 3.1 + Scalar docs (`/docs`), single-command Docker startup, and automated CI/CD pushing commit-SHA tagged images.

**Architecture:** Clean Layered + DDD-Lite (`handler -> service -> domain <- repository`, with `worker` driving background expiry). Business state machines and error codes live dependency-free in `internal/domain`; PostgreSQL transactions (`pgx/v5`) enforce deterministic lock ordering (`pg_advisory_xact_lock` -> `orders ASC` -> `ticket_types ASC`) and batch expiry (`FOR UPDATE SKIP LOCKED`) with storage-level `CHECK` and `UNIQUE` constraints.

**Tech Stack:** Go `1.26.2`, `github.com/go-chi/chi/v5`, `github.com/danielgtaylor/huma/v2`, `github.com/jackc/pgx/v5`, `github.com/golang-jwt/jwt/v5`, `github.com/google/uuid`, `golang.org/x/crypto/bcrypt`, PostgreSQL 17, Docker Compose, GitHub Actions (`ghcr.io`).

**Companion Docs:**
- Product Spec: `specs/event-ticketing/PRODUCT.md`
- Technical Spec: `specs/event-ticketing/TECH.md`
- Decision Records: `DECISIONS.md`

---

## Target File Structure

| Path | Responsibility |
| :--- | :--- |
| `go.mod`, `.golangci.yml`, `.gitignore`, `.env.example`, `Makefile` | Go module (`github.com/torikh42/event-ticketing`), linter rules, env defaults, developer tasks |
| `internal/config/config.go` | Typed environment configuration loading & fail-closed validation |
| `internal/domain/errors.go`, `user.go`, `event.go`, `order.go`, `ticket.go` | Pure entities, `Order.CanCancel`/`CanPay`, constants (`MaxTicketsPerBuyerPerEvent = 4`), SHA-512 signature helpers, machine-readable `DomainError` codes |
| `migrations/embed.go`, `001_init_schema.sql`, `002_seed_demo.sql` | Embedded SQL DDL, `invoice_seq`, `CHECK`/`UNIQUE` constraints, partial expiry index, idempotent demo seed |
| `internal/repository/postgres.go`, `migrator.go`, `user_repo.go`, `event_repo.go` | `pgxpool` configuration, advisory-locked SQL migrator, user CRUD/demo ensuring, atomic event+tier queries |
| `internal/repository/order_repo.go`, `webhook_repo.go` | Dual-lock checkout, cancellation, batch `SKIP LOCKED` expiry sweeper, idempotent payment & ticket issuance, standalone webhook audit log |
| `internal/service/interfaces.go`, `auth_service.go`, `event_service.go`, `order_service.go`, `payment_service.go`, `demo_service.go` | Consumer-defined repository interfaces, role checks, lazy-expiry read coordination, fail-closed webhook processing, 200-goroutine flash sale simulator |
| `internal/worker/expiry_sweeper.go` | Context-aware periodic ticker draining overdue `WAITING_PAYMENT` orders cleanly on shutdown |
| `internal/middleware/auth.go`, `logging.go` | Hybrid JWT + opt-in demo header auth (`ALLOW_DEMO_HEADER_AUTH`), structured `slog` HTTP logs, panic JSON recoverer |
| `internal/handler/error_mapper.go`, `auth_handler.go`, `event_handler.go`, `order_handler.go`, `webhook_handler.go`, `demo_handler.go`, `router.go` | Huma v2 DTO validation schemas, OpenAPI 3.1 + Scalar UI (`/docs`), `/health`, `/demo` route wiring |
| `web/embed.go`, `web/demo.html` | Embedded Live Demo Console (visual HTML/CSS styling gated on antislop mode + `DESIGN.md` confirmation in Task 11) |
| `cmd/api/main.go` | Application wiring, migration startup, HTTP server + graceful shutdown |
| `Dockerfile`, `docker-compose.yml`, `.github/workflows/ci.yml` | Multi-stage non-root image, 1-command Postgres + API stack, CI test + commit-SHA registry push |
| `tests/integration_test.go` | `//go:build integration` suite covering `B1..B10` including 200 concurrent buyers and concurrent webhook replays |
| `README.md` | Quickstart (`docker compose up`), test instructions, payment webhook/demo usage, measured flash-sale evidence |

---

### Task 1: Project Scaffold, Tooling, and Configuration

**Files:**
- Create: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\go.mod`
- Create: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\.golangci.yml`
- Create: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\.gitignore`
- Create: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\.env.example`
- Create: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\Makefile`
- Create: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\internal\config\config.go`
- Test: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\internal\config\config_test.go`

- [ ] **Step 1: Initialize Go module and dependencies**

Run from `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing`:
```bash
go mod init github.com/torikh42/event-ticketing
go get github.com/go-chi/chi/v5@latest \
  github.com/danielgtaylor/huma/v2@latest \
  github.com/jackc/pgx/v5@latest \
  github.com/golang-jwt/jwt/v5@latest \
  github.com/google/uuid@latest \
  golang.org/x/crypto@latest
```
Expected: `go.mod` and `go.sum` created targeting Go 1.26.2 with no errors.

- [ ] **Step 2: Write `.gitignore`, `.env.example`, `Makefile`, and `.golangci.yml`**

Create `.gitignore`:
```gitignore
bin/
.env
coverage.out
*.exe
.DS_Store
```

Create `.env.example`:
```dotenv
PORT=8080
DATABASE_URL=postgres://ticketing:ticketing_secret@localhost:5432/event_ticketing?sslmode=disable
JWT_SECRET=dev-jwt-secret-change-in-production
WEBHOOK_SECRET=dev-webhook-secret-change-in-production
ALLOW_DEMO_HEADER_AUTH=true
EXPIRY_SWEEP_INTERVAL=5s
HTTP_READ_TIMEOUT=15s
HTTP_WRITE_TIMEOUT=30s
DB_MAX_CONNS=50
DB_MIN_CONNS=5
```

Create `Makefile`:
```makefile
.PHONY: up down logs test test-integration lint fmt build

up:
	docker compose up --build

down:
	docker compose down -v

logs:
	docker compose logs -f api

test:
	go test -v -race ./internal/...

test-integration:
	go test -v -race -tags=integration ./tests/...

lint:
	golangci-lint run ./...

fmt:
	gofmt -s -w .

build:
	go build -o bin/api ./cmd/api
```

Create `.golangci.yml` (golangci-lint v2):
```yaml
version: "2"
run:
  concurrency: 4
  timeout: 5m
  tests: true

linters:
  enable:
    - govet
    - staticcheck
    - unused
    - errcheck
    - errorlint
    - nilerr
    - forcetypeassert
    - copyloopvar
    - durationcheck
    - reassign
    - gocritic
    - revive
    - whitespace
    - godot
    - misspell
    - dupword
    - predeclared
    - errname
    - asciicheck
    - gocyclo
    - nestif
    - funlen
    - perfsprint
    - unconvert
    - ineffassign
    - goconst
    - gosec
    - bidichk
    - bodyclose
    - noctx
    - containedctx
    - fatcontext
    - sloglint
    - usestdlibvars
    - intrange
    - exhaustive
    - nolintlint
  settings:
    errcheck:
      check-type-assertions: true
    funlen:
      lines: 120
      statements: 80
    goconst:
      min-len: 3
      min-occurrences: 4
    gocyclo:
      min-complexity: 15
    nolintlint:
      require-explanation: true
      require-specific: true

formatters:
  enable:
    - gofmt
```

- [ ] **Step 3: Write the failing unit test for config loading**

Create `internal/config/config_test.go`:
```go
package config

import (
	"testing"
	"time"
)

func TestLoadFromEnv(t *testing.T) {
	t.Run("requires database url", func(t *testing.T) {
		t.Setenv("DATABASE_URL", "")
		t.Setenv("JWT_SECRET", "jwt-secret")
		t.Setenv("WEBHOOK_SECRET", "webhook-secret")

		if _, err := Load(); err == nil {
			t.Fatal("expected error when DATABASE_URL is empty")
		}
	})

	t.Run("parses defaults and overrides", func(t *testing.T) {
		t.Setenv("DATABASE_URL", "postgres://u:p@localhost:5432/db?sslmode=disable")
		t.Setenv("JWT_SECRET", "jwt-secret")
		t.Setenv("WEBHOOK_SECRET", "webhook-secret")
		t.Setenv("PORT", "9090")
		t.Setenv("ALLOW_DEMO_HEADER_AUTH", "true")
		t.Setenv("EXPIRY_SWEEP_INTERVAL", "2s")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Port != "9090" {
			t.Fatalf("Port = %q, want 9090", cfg.Port)
		}
		if !cfg.AllowDemoHeaderAuth {
			t.Fatal("expected AllowDemoHeaderAuth to be true")
		}
		if cfg.ExpirySweepInterval != 2*time.Second {
			t.Fatalf("ExpirySweepInterval = %v, want 2s", cfg.ExpirySweepInterval)
		}
	})
}
```

- [ ] **Step 4: Run test to verify it fails**

Run: `go test -v ./internal/config/...`
Expected: FAIL (`undefined: Load`).

- [ ] **Step 5: Implement `internal/config/config.go`**

```go
// Package config loads and validates runtime configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds validated runtime configuration for the API server and workers.
type Config struct {
	Port                string
	DatabaseURL         string
	JWTSecret           string
	WebhookSecret       string
	AllowDemoHeaderAuth bool
	ExpirySweepInterval time.Duration
	HTTPReadTimeout     time.Duration
	HTTPWriteTimeout    time.Duration
	DBMaxConns          int32
	DBMinConns          int32
}

// Load reads configuration from environment variables and validates required fields.
func Load() (Config, error) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}

	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		return Config{}, errors.New("JWT_SECRET is required")
	}

	webhookSecret := os.Getenv("WEBHOOK_SECRET")
	if webhookSecret == "" {
		return Config{}, errors.New("WEBHOOK_SECRET is required")
	}

	sweepInterval, err := parseDuration("EXPIRY_SWEEP_INTERVAL", 5*time.Second)
	if err != nil {
		return Config{}, err
	}

	readTimeout, err := parseDuration("HTTP_READ_TIMEOUT", 15*time.Second)
	if err != nil {
		return Config{}, err
	}

	writeTimeout, err := parseDuration("HTTP_WRITE_TIMEOUT", 30*time.Second)
	if err != nil {
		return Config{}, err
	}

	maxConns, err := parseInt32("DB_MAX_CONNS", 50)
	if err != nil {
		return Config{}, err
	}

	minConns, err := parseInt32("DB_MIN_CONNS", 5)
	if err != nil {
		return Config{}, err
	}

	return Config{
		Port:                getEnvOrDefault("PORT", "8080"),
		DatabaseURL:         dbURL,
		JWTSecret:           jwtSecret,
		WebhookSecret:       webhookSecret,
		AllowDemoHeaderAuth: os.Getenv("ALLOW_DEMO_HEADER_AUTH") == "true",
		ExpirySweepInterval: sweepInterval,
		HTTPReadTimeout:     readTimeout,
		HTTPWriteTimeout:    writeTimeout,
		DBMaxConns:          maxConns,
		DBMinConns:          minConns,
	}, nil
}

func getEnvOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseDuration(key string, fallback time.Duration) (time.Duration, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid duration for %s: %w", key, err)
	}
	return d, nil
}

func parseInt32(key string, fallback int32) (int32, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid positive integer for %s: %q", key, raw)
	}
	return int32(n), nil
}
```

- [ ] **Step 6: Run test to verify it passes**

Run: `go test -v -race ./internal/config/...`
Expected: PASS (`TestLoadFromEnv`).

- [ ] **Step 7: Commit scaffold & config**

```bash
git init -b main
git add go.mod go.sum .golangci.yml .gitignore .env.example Makefile internal/config/
git commit -m "chore: initialize event-ticketing module, linter config, and env config loader"
```

---

### Task 2: Pure Domain Core (`internal/domain/`)

**Files:**
- Create: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\internal\domain\errors.go`
- Create: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\internal\domain\user.go`
- Create: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\internal\domain\event.go`
- Create: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\internal\domain\order.go`
- Create: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\internal\domain\ticket.go`
- Test: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\internal\domain\domain_test.go`

- [ ] **Step 1: Write failing unit tests for state transitions, constants, and webhook signatures**

Create `internal/domain/domain_test.go`:
```go
package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/torikh42/event-ticketing/internal/domain"
)

func TestOrder_CanPay(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		status   domain.OrderStatus
		expires  time.Time
		wantCode string
	}{
		{
			name:     "waiting payment before deadline can pay",
			status:   domain.OrderStatusWaitingPayment,
			expires:  now.Add(10 * time.Minute),
			wantCode: "",
		},
		{
			name:     "already paid triggers idempotent conflict code handled by service",
			status:   domain.OrderStatusPaid,
			expires:  now.Add(-5 * time.Minute),
			wantCode: "",
		},
		{
			name:     "waiting payment past expires_at returns ORDER_EXPIRED",
			status:   domain.OrderStatusWaitingPayment,
			expires:  now.Add(-1 * time.Second),
			wantCode: domain.ErrCodeOrderExpired,
		},
		{
			name:     "already expired returns ORDER_EXPIRED",
			status:   domain.OrderStatusExpired,
			expires:  now.Add(-20 * time.Minute),
			wantCode: domain.ErrCodeOrderExpired,
		},
		{
			name:     "cancelled order cannot be paid",
			status:   domain.OrderStatusCancelled,
			expires:  now.Add(5 * time.Minute),
			wantCode: domain.ErrCodeInvalidOrderState,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			o := &domain.Order{
				ID:        uuid.New(),
				Status:    tt.status,
				ExpiresAt: tt.expires,
			}

			err := o.CanPay(now)
			if tt.wantCode == "" {
				if err != nil {
					t.Fatalf("expected nil error, got %v", err)
				}
				return
			}

			de, ok := errors.AsType[*domain.DomainError](err)
			if !ok {
				t.Fatalf("expected *DomainError, got %T (%v)", err, err)
			}
			if de.Code != tt.wantCode {
				t.Fatalf("Code = %q, want %q", de.Code, tt.wantCode)
			}
		})
	}
}

func TestOrder_CanCancel(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		status   domain.OrderStatus
		expires  time.Time
		wantCode string
	}{
		{
			name:     "waiting payment before expiry can cancel",
			status:   domain.OrderStatusWaitingPayment,
			expires:  now.Add(5 * time.Minute),
			wantCode: "",
		},
		{
			name:     "waiting payment past expiry returns ORDER_EXPIRED",
			status:   domain.OrderStatusWaitingPayment,
			expires:  now.Add(-1 * time.Second),
			wantCode: domain.ErrCodeOrderExpired,
		},
		{
			name:     "paid order cannot be cancelled",
			status:   domain.OrderStatusPaid,
			expires:  now.Add(5 * time.Minute),
			wantCode: domain.ErrCodeInvalidOrderState,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			o := &domain.Order{
				ID:        uuid.New(),
				Status:    tt.status,
				ExpiresAt: tt.expires,
			}
			err := o.CanCancel(now)
			if tt.wantCode == "" {
				if err != nil {
					t.Fatalf("expected nil error, got %v", err)
				}
				return
			}
			de, ok := errors.AsType[*domain.DomainError](err)
			if !ok || de.Code != tt.wantCode {
				t.Fatalf("got err=%v, want code %q", err, tt.wantCode)
			}
		})
	}
}

func TestVerifySHA512Signature(t *testing.T) {
	t.Parallel()

	orderID := "550e8400-e29b-41d4-a716-446655440000"
	secret := "test-webhook-secret"
	sig := domain.ComputeSHA512Signature(orderID, "200", 300000, secret)

	if !domain.VerifySHA512Signature(orderID, "200", 300000, secret, sig) {
		t.Fatal("expected valid SHA-512 signature to verify true")
	}
	if domain.VerifySHA512Signature(orderID, "200", 300001, secret, sig) {
		t.Fatal("expected mismatched gross_amount to fail verification")
	}
	if domain.VerifySHA512Signature(orderID, "200", 300000, "", sig) {
		t.Fatal("expected empty secret to fail closed")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/domain/...`
Expected: FAIL (package `internal/domain` does not exist yet).

- [ ] **Step 3: Create domain files (`errors.go`, `user.go`, `event.go`, `order.go`, `ticket.go`)**

Create `internal/domain/errors.go`:
```go
// Package domain defines pure ticketing entities, state transitions, and business errors.
package domain

import "net/http"

// Machine-readable domain error codes shared across HTTP responses and tests.
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
	ErrCodeInvalidCredentials    = "INVALID_CREDENTIALS"
	ErrCodeValidation            = "VALIDATION_ERROR"
)

// DomainError carries an HTTP status code, stable machine code, and client-safe message.
type DomainError struct {
	Code       string
	HTTPStatus int
	Message    string
}

// Error implements the standard error interface.
func (e *DomainError) Error() string {
	return e.Message
}

// NewDomainError creates a structured DomainError.
func NewDomainError(status int, code, msg string) *DomainError {
	return &DomainError{
		Code:       code,
		HTTPStatus: status,
		Message:    msg,
	}
}

func NewQuotaExhaustedError(msg string) *DomainError {
	return NewDomainError(http.StatusConflict, ErrCodeQuotaExhausted, msg)
}

func NewBuyerLimitExceededError(msg string) *DomainError {
	return NewDomainError(http.StatusConflict, ErrCodeBuyerLimitExceeded, msg)
}

func NewOrderExpiredError(msg string) *DomainError {
	return NewDomainError(http.StatusConflict, ErrCodeOrderExpired, msg)
}

func NewInvalidOrderStateError(msg string) *DomainError {
	return NewDomainError(http.StatusConflict, ErrCodeInvalidOrderState, msg)
}

func NewPaymentAmountMismatchError(msg string) *DomainError {
	return NewDomainError(http.StatusBadRequest, ErrCodePaymentAmountMismatch, msg)
}

func NewUnauthorizedWebhookError(msg string) *DomainError {
	return NewDomainError(http.StatusUnauthorized, ErrCodeUnauthorizedWebhook, msg)
}
```

Create `internal/domain/user.go`:
```go
package domain

import (
	"time"

	"github.com/google/uuid"
)

// Role enumerates system authorization roles.
type Role string

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

// Actor identifies the authenticated caller attached to request context.
type Actor struct {
	ID    uuid.UUID
	Email string
	Role  Role
}
```

Create `internal/domain/event.go`:
```go
package domain

import (
	"net/http"
	"time"

	"github.com/google/uuid"
)

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
	if t.Name == "" {
		return NewDomainError(http.StatusUnprocessableEntity, ErrCodeValidation, "ticket type name is required")
	}
	if t.PriceIDR < 0 {
		return NewDomainError(http.StatusUnprocessableEntity, ErrCodeValidation, "price_idr must be non-negative")
	}
	if t.TotalQuota <= 0 {
		return NewDomainError(http.StatusUnprocessableEntity, ErrCodeValidation, "total_quota must be greater than zero")
	}
	return nil
}
```

Create `internal/domain/order.go`:
```go
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
	switch o.Status {
	case OrderStatusPaid:
		return nil
	case OrderStatusExpired:
		return NewOrderExpiredError("order has already expired; payment cannot issue tickets")
	case OrderStatusCancelled:
		return NewInvalidOrderStateError("order was cancelled and cannot be paid")
	case OrderStatusWaitingPayment:
		if now.After(o.ExpiresAt) {
			return NewOrderExpiredError("order reservation window has expired")
		}
		return nil
	default:
		return NewInvalidOrderStateError("invalid order status")
	}
}

// CanCancel validates whether the order may be cancelled by the buyer before payment.
func (o *Order) CanCancel(now time.Time) error {
	switch o.Status {
	case OrderStatusWaitingPayment:
		if now.After(o.ExpiresAt) {
			return NewOrderExpiredError("order has already expired")
		}
		return nil
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
```

Create `internal/domain/ticket.go`:
```go
package domain

import (
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// WebhookOutcome classifies how a payment webhook delivery was handled.
type WebhookOutcome string

const (
	OutcomeProcessedPaid       WebhookOutcome = "PROCESSED_PAID"
	OutcomeIgnoredDuplicate    WebhookOutcome = "IGNORED_DUPLICATE"
	OutcomeRejectedLateExpired WebhookOutcome = "REJECTED_LATE_EXPIRED"
	OutcomeProcessedFailed     WebhookOutcome = "PROCESSED_FAILED"
	OutcomeAmountMismatch      WebhookOutcome = "AMOUNT_MISMATCH"
	OutcomeSignatureInvalid    WebhookOutcome = "SIGNATURE_INVALID"
	OutcomeOrderNotFound       WebhookOutcome = "ORDER_NOT_FOUND"
	OutcomeIgnoredUnknownEvent WebhookOutcome = "IGNORED_UNKNOWN_EVENT"
)

// Ticket represents one issued admission pass tied uniquely to an order item seat sequence.
type Ticket struct {
	ID           uuid.UUID `json:"id"`
	TicketCode   string    `json:"ticket_code"`
	OrderID      uuid.UUID `json:"order_id"`
	OrderItemID  uuid.UUID `json:"order_item_id"`
	EventID      uuid.UUID `json:"event_id"`
	TicketTypeID uuid.UUID `json:"ticket_type_id"`
	OwnerID      uuid.UUID `json:"owner_id"`
	SeatSequence int32     `json:"seat_sequence"`
	IssuedAt     time.Time `json:"issued_at"`
}

// PaymentWebhook records every webhook attempt outside transaction rollbacks.
type PaymentWebhook struct {
	ID                uuid.UUID      `json:"id"`
	TransactionID     string         `json:"transaction_id"`
	RawOrderID        string         `json:"raw_order_id"`
	ParsedOrderID     *uuid.UUID     `json:"parsed_order_id,omitempty"`
	StatusCode        string         `json:"status_code"`
	GrossAmount       int64          `json:"gross_amount"`
	TransactionStatus string         `json:"transaction_status"`
	Outcome           WebhookOutcome `json:"outcome"`
	RequiresRefund    bool           `json:"requires_refund"`
	RawPayload        []byte         `json:"raw_payload"`
	ReceivedAt        time.Time      `json:"received_at"`
}

// ComputeSHA512Signature returns lowercase hex SHA-512 of orderID + statusCode + grossAmount + secret.
func ComputeSHA512Signature(orderID, statusCode string, grossAmount int64, secret string) string {
	raw := fmt.Sprintf("%s%s%d%s", orderID, statusCode, grossAmount, secret)
	sum := sha512.Sum512([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// VerifySHA512Signature verifies webhook authenticity in constant time and fails closed on empty secret.
func VerifySHA512Signature(orderID, statusCode string, grossAmount int64, secret, providedSig string) bool {
	if secret == "" || providedSig == "" {
		return false
	}
	expected := ComputeSHA512Signature(orderID, statusCode, grossAmount, secret)
	normalized := strings.ToLower(strings.TrimSpace(providedSig))
	return hmac.Equal([]byte(expected), []byte(normalized))
}
```

- [ ] **Step 4: Run tests and verify domain package passes**

Run: `go test -v -race ./internal/domain/...`
Expected: PASS (`TestOrder_CanPay`, `TestOrder_CanCancel`, `TestVerifySHA512Signature`).

- [ ] **Step 5: Commit domain layer**

```bash
git add internal/domain/
git commit -m "feat(domain): add ticketing entities, order state guards, error codes, and webhook signature verification"
```

---

### Task 3: PostgreSQL Versioned Schema, Constraints, & Demo Seed (`migrations/`)

**Files:**
- Create: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\migrations\embed.go`
- Create: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\migrations\001_init_schema.sql`
- Create: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\migrations\002_seed_demo.sql`

- [ ] **Step 1: Create embedded SQL migration filesystem**

Create `migrations/embed.go`:
```go
// Package migrations embeds ordered PostgreSQL DDL and seed SQL files.
package migrations

import "embed"

// Files holds versioned *.sql files applied sequentially on startup.
//
//go:embed *.sql
var Files embed.FS
```

- [ ] **Step 2: Author `migrations/001_init_schema.sql` with DB invariants and partial expiry index**

Create `migrations/001_init_schema.sql`:
```sql
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE SEQUENCE IF NOT EXISTS invoice_seq START 1;

CREATE TABLE IF NOT EXISTS users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    password_hash TEXT NOT NULL DEFAULT '',
    role TEXT NOT NULL CHECK (role IN ('ORGANIZER', 'BUYER')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organizer_id UUID NOT NULL REFERENCES users(id),
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    venue TEXT NOT NULL,
    event_date TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS ticket_types (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    event_id UUID NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    price_idr BIGINT NOT NULL CHECK (price_idr >= 0),
    total_quota INTEGER NOT NULL CHECK (total_quota > 0),
    remaining_quota INTEGER NOT NULL CHECK (remaining_quota >= 0 AND remaining_quota <= total_quota),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (event_id, name)
);

CREATE TABLE IF NOT EXISTS orders (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    invoice_number TEXT NOT NULL UNIQUE,
    event_id UUID NOT NULL REFERENCES events(id),
    buyer_id UUID NOT NULL REFERENCES users(id),
    total_tickets INTEGER NOT NULL CHECK (total_tickets BETWEEN 1 AND 4),
    total_amount_idr BIGINT NOT NULL CHECK (total_amount_idr >= 0),
    status TEXT NOT NULL CHECK (status IN ('WAITING_PAYMENT', 'PAID', 'CANCELLED', 'EXPIRED')),
    expires_at TIMESTAMPTZ NOT NULL,
    paid_at TIMESTAMPTZ NULL,
    cancelled_at TIMESTAMPTZ NULL,
    expired_at TIMESTAMPTZ NULL,
    cancelled_reason TEXT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_orders_buyer_event_status
    ON orders (event_id, buyer_id, status);

CREATE INDEX IF NOT EXISTS idx_orders_active_expiry
    ON orders (event_id, expires_at)
    WHERE status = 'WAITING_PAYMENT';

CREATE INDEX IF NOT EXISTS idx_orders_global_expiry
    ON orders (expires_at)
    WHERE status = 'WAITING_PAYMENT';

CREATE TABLE IF NOT EXISTS order_items (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id UUID NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    ticket_type_id UUID NOT NULL REFERENCES ticket_types(id),
    quantity INTEGER NOT NULL CHECK (quantity > 0),
    unit_price_idr BIGINT NOT NULL CHECK (unit_price_idr >= 0),
    subtotal_idr BIGINT NOT NULL CHECK (subtotal_idr >= 0),
    UNIQUE (order_id, ticket_type_id)
);

CREATE TABLE IF NOT EXISTS order_logs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id UUID NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    from_status TEXT NULL,
    to_status TEXT NOT NULL,
    actor_id UUID NULL,
    actor_type TEXT NOT NULL,
    note TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_order_logs_order_id
    ON order_logs (order_id, created_at);

CREATE TABLE IF NOT EXISTS tickets (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    ticket_code TEXT NOT NULL UNIQUE,
    order_id UUID NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    order_item_id UUID NOT NULL REFERENCES order_items(id) ON DELETE CASCADE,
    event_id UUID NOT NULL REFERENCES events(id),
    ticket_type_id UUID NOT NULL REFERENCES ticket_types(id),
    owner_id UUID NOT NULL REFERENCES users(id),
    seat_sequence INTEGER NOT NULL CHECK (seat_sequence > 0),
    issued_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (order_item_id, seat_sequence)
);

CREATE INDEX IF NOT EXISTS idx_tickets_order_id
    ON tickets (order_id);

CREATE TABLE IF NOT EXISTS payment_webhooks (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    transaction_id TEXT NOT NULL,
    raw_order_id TEXT NOT NULL,
    parsed_order_id UUID NULL,
    status_code TEXT NOT NULL,
    gross_amount BIGINT NOT NULL,
    transaction_status TEXT NOT NULL,
    outcome TEXT NOT NULL,
    requires_refund BOOLEAN NOT NULL DEFAULT FALSE,
    raw_payload JSONB NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_payment_webhooks_tx_id
    ON payment_webhooks (transaction_id);

CREATE INDEX IF NOT EXISTS idx_payment_webhooks_parsed_order_id
    ON payment_webhooks (parsed_order_id);
```

- [ ] **Step 3: Author idempotent `migrations/002_seed_demo.sql`**

Create `migrations/002_seed_demo.sql` (password hashes use verified bcrypt cost 10 for `"password123"` for demo logins):
```sql
INSERT INTO users (id, email, name, password_hash, role)
VALUES
    ('11111111-1111-1111-1111-111111111111', 'organizer@event.local', 'Demo Organizer', '$2a$10$FkmASOgfpqp5X9fpThx5tObnKeOAaEyTP.dM2S84ycPV6Fc.JKnsG', 'ORGANIZER'),
    ('22222222-2222-2222-2222-222222222221', 'buyer1@event.local', 'Demo Buyer 1', '$2a$10$FkmASOgfpqp5X9fpThx5tObnKeOAaEyTP.dM2S84ycPV6Fc.JKnsG', 'BUYER'),
    ('22222222-2222-2222-2222-222222222222', 'buyer2@event.local', 'Demo Buyer 2', '$2a$10$FkmASOgfpqp5X9fpThx5tObnKeOAaEyTP.dM2S84ycPV6Fc.JKnsG', 'BUYER')
ON CONFLICT (id) DO NOTHING;

INSERT INTO events (id, organizer_id, title, description, venue, event_date)
VALUES (
    'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaa1',
    '11111111-1111-1111-1111-111111111111',
    'Tech Flash Concert 2026',
    'Seeded 100-quota flash sale event with Reguler and VIP tiers',
    'Jakarta Convention Hall',
    '2026-12-20T19:00:00Z'
)
ON CONFLICT (id) DO NOTHING;

INSERT INTO ticket_types (id, event_id, name, price_idr, total_quota, remaining_quota)
VALUES
    ('bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbb1', 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaa1', 'Reguler', 150000, 80, 80),
    ('bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbb2', 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaa1', 'VIP', 350000, 20, 20)
ON CONFLICT (id) DO NOTHING;
```

- [ ] **Step 4: Commit migrations**

```bash
git add migrations/
git commit -m "feat(db): add PostgreSQL schema, check/unique invariants, partial expiry index, and demo seed"
```

---

### Task 4: Database Pool, Advisory-Locked Migrator, User & Event Repositories

**Files:**
- Create: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\internal\repository\postgres.go`
- Create: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\internal\repository\migrator.go`
- Create: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\internal\repository\user_repo.go`
- Create: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\internal\repository\event_repo.go`

- [ ] **Step 1: Implement `internal/repository/postgres.go` & `migrator.go`**

Create `internal/repository/postgres.go`:
```go
// Package repository implements PostgreSQL persistence using pgxpool and explicit transactions.
package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/torikh42/event-ticketing/internal/config"
)

// NewPool configures and validates a PostgreSQL connection pool.
func NewPool(ctx context.Context, cfg config.Config) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("parsing database url: %w", err)
	}

	poolCfg.MaxConns = cfg.DBMaxConns
	poolCfg.MinConns = cfg.DBMinConns
	poolCfg.MaxConnLifetime = 30 * time.Minute
	poolCfg.MaxConnIdleTime = 5 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("creating pgxpool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pinging database: %w", err)
	}

	return pool, nil
}
```

Create `internal/repository/migrator.go`:
```go
package repository

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/torikh42/event-ticketing/migrations"
)

const migrationLockID int64 = 74839201

// ApplyMigrations acquires a PostgreSQL advisory lock and applies embedded SQL migrations once in lexical order.
func ApplyMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquiring migration connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		return fmt.Errorf("acquiring migration advisory lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", migrationLockID)
	}()

	const createTableSQL = `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);`
	if _, err := conn.Exec(ctx, createTableSQL); err != nil {
		return fmt.Errorf("creating schema_migrations table: %w", err)
	}

	entries, err := fs.ReadDir(migrations.Files, ".")
	if err != nil {
		return fmt.Errorf("reading embedded migrations: %w", err)
	}

	var files []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			files = append(files, entry.Name())
		}
	}
	sort.Strings(files)

	for _, name := range files {
		var alreadyApplied bool
		err := conn.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = $1)", name).Scan(&alreadyApplied)
		if err != nil {
			return fmt.Errorf("checking migration %s: %w", name, err)
		}
		if alreadyApplied {
			continue
		}

		sqlBytes, err := fs.ReadFile(migrations.Files, name)
		if err != nil {
			return fmt.Errorf("reading migration %s: %w", name, err)
		}

		tx, err := conn.Begin(ctx)
		if err != nil {
			return fmt.Errorf("beginning tx for migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, string(sqlBytes)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("executing migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", name); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("recording migration %s: %w", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("committing migration %s: %w", name, err)
		}
	}

	return nil
}
```

- [ ] **Step 2: Implement `internal/repository/user_repo.go`**

```go
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
	"github.com/torikh42/event-ticketing/internal/domain"
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
	const query = `
		INSERT INTO users (id, email, name, password_hash, role)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING created_at`

	if u.ID == uuid.Nil {
		u.ID = uuid.New()
	}

	err := r.pool.QueryRow(ctx, query, u.ID, u.Email, u.Name, u.PasswordHash, string(u.Role)).Scan(&u.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return domain.NewDomainError(http.StatusConflict, domain.ErrCodeEmailAlreadyExists, "email is already registered")
		}
		return fmt.Errorf("inserting user: %w", err)
	}
	return nil
}

// FindByEmail retrieves a user by email address.
func (r *UserRepo) FindByEmail(ctx context.Context, email string) (*domain.User, error) {
	const query = `SELECT id, email, name, password_hash, role, created_at FROM users WHERE email = $1`
	var u domain.User
	var role string
	err := r.pool.QueryRow(ctx, query, email).Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash, &role, &u.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.NewDomainError(http.StatusUnauthorized, domain.ErrCodeInvalidCredentials, "invalid email or password")
		}
		return nil, fmt.Errorf("querying user by email: %w", err)
	}
	u.Role = domain.Role(role)
	return &u, nil
}

// FindByID retrieves a user by UUID.
func (r *UserRepo) FindByID(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	const query = `SELECT id, email, name, password_hash, role, created_at FROM users WHERE id = $1`
	var u domain.User
	var role string
	err := r.pool.QueryRow(ctx, query, id).Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash, &role, &u.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.NewDomainError(http.StatusUnauthorized, domain.ErrCodeUnauthorized, "user not found")
		}
		return nil, fmt.Errorf("querying user by id: %w", err)
	}
	u.Role = domain.Role(role)
	return &u, nil
}

// EnsureDemoUser idempotently ensures a synthetic header-auth user exists so FK constraints stay satisfied.
func (r *UserRepo) EnsureDemoUser(ctx context.Context, id uuid.UUID, role domain.Role) error {
	const query = `
		INSERT INTO users (id, email, name, password_hash, role)
		VALUES ($1, $2, $3, '', $4)
		ON CONFLICT (id) DO NOTHING`
	email := fmt.Sprintf("demo-%s@event.local", id.String())
	name := fmt.Sprintf("Demo %s", string(role))
	if _, err := r.pool.Exec(ctx, query, id, email, name, string(role)); err != nil {
		return fmt.Errorf("ensuring demo user: %w", err)
	}
	return nil
}
```

- [ ] **Step 3: Implement `internal/repository/event_repo.go`**

```go
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
	"github.com/torikh42/event-ticketing/internal/domain"
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
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning create event tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

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
		return fmt.Errorf("inserting event: %w", err)
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
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeValidation, "duplicate ticket type name within event")
			}
			return fmt.Errorf("inserting ticket type %q: %w", ev.TicketTypes[i].Name, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing create event tx: %w", err)
	}
	return nil
}

// ListAll retrieves all events along with their ticket tiers.
func (r *EventRepo) ListAll(ctx context.Context) ([]domain.Event, error) {
	const queryEvents = `
		SELECT id, organizer_id, title, description, venue, event_date, created_at
		FROM events
		ORDER BY event_date ASC, created_at ASC`
	rows, err := r.pool.Query(ctx, queryEvents)
	if err != nil {
		return nil, fmt.Errorf("listing events: %w", err)
	}
	defer rows.Close()

	var events []domain.Event
	eventIdx := make(map[uuid.UUID]int)
	for rows.Next() {
		var ev domain.Event
		if err := rows.Scan(&ev.ID, &ev.OrganizerID, &ev.Title, &ev.Description, &ev.Venue, &ev.EventDate, &ev.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning event: %w", err)
		}
		ev.TicketTypes = []domain.TicketType{}
		eventIdx[ev.ID] = len(events)
		events = append(events, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating events: %w", err)
	}
	if len(events) == 0 {
		return []domain.Event{}, nil
	}

	const queryTiers = `
		SELECT id, event_id, name, price_idr, total_quota, remaining_quota, created_at
		FROM ticket_types
		ORDER BY price_idr ASC, name ASC`
	tierRows, err := r.pool.Query(ctx, queryTiers)
	if err != nil {
		return nil, fmt.Errorf("listing ticket types: %w", err)
	}
	defer tierRows.Close()

	for tierRows.Next() {
		var tt domain.TicketType
		if err := tierRows.Scan(&tt.ID, &tt.EventID, &tt.Name, &tt.PriceIDR, &tt.TotalQuota, &tt.RemainingQuota, &tt.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning ticket type: %w", err)
		}
		if idx, ok := eventIdx[tt.EventID]; ok {
			events[idx].TicketTypes = append(events[idx].TicketTypes, tt)
		}
	}
	if err := tierRows.Err(); err != nil {
		return nil, fmt.Errorf("iterating ticket types: %w", err)
	}
	return events, nil
}

// FindByID retrieves a single event with its ticket tiers.
func (r *EventRepo) FindByID(ctx context.Context, id uuid.UUID) (*domain.Event, error) {
	const queryEvent = `
		SELECT id, organizer_id, title, description, venue, event_date, created_at
		FROM events
		WHERE id = $1`
	var ev domain.Event
	err := r.pool.QueryRow(ctx, queryEvent, id).Scan(
		&ev.ID, &ev.OrganizerID, &ev.Title, &ev.Description, &ev.Venue, &ev.EventDate, &ev.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.NewDomainError(http.StatusNotFound, domain.ErrCodeEventNotFound, "event not found")
		}
		return nil, fmt.Errorf("querying event: %w", err)
	}

	const queryTiers = `
		SELECT id, event_id, name, price_idr, total_quota, remaining_quota, created_at
		FROM ticket_types
		WHERE event_id = $1
		ORDER BY price_idr ASC, name ASC`
	rows, err := r.pool.Query(ctx, queryTiers, id)
	if err != nil {
		return nil, fmt.Errorf("querying event ticket types: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var tt domain.TicketType
		if err := rows.Scan(&tt.ID, &tt.EventID, &tt.Name, &tt.PriceIDR, &tt.TotalQuota, &tt.RemainingQuota, &tt.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning event ticket type: %w", err)
		}
		ev.TicketTypes = append(ev.TicketTypes, tt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating event ticket types: %w", err)
	}
	return &ev, nil
}
```

- [ ] **Step 4: Compile check repository package**

Run: `go build ./internal/repository/...`
Expected: PASS with exit code 0.

- [ ] **Step 5: Commit pool, migrator, user & event repositories**

```bash
git add internal/repository/postgres.go internal/repository/migrator.go internal/repository/user_repo.go internal/repository/event_repo.go
git commit -m "feat(repo): add pgxpool setup, advisory migrator, user and event repositories"
```

---

### Task 5: Order & Webhook Repositories with Dual Lock & Audit Logging

**Files:**
- Create: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\internal\repository\order_repo.go`
- Create: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\internal\repository\webhook_repo.go`

- [ ] **Step 1: Implement `internal/repository/webhook_repo.go`**

```go
package repository

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/torikh42/event-ticketing/internal/domain"
)

// WebhookRepo records payment callback audit entries outside business rollback transactions.
type WebhookRepo struct {
	pool *pgxpool.Pool
}

// NewWebhookRepo constructs a WebhookRepo.
func NewWebhookRepo(pool *pgxpool.Pool) *WebhookRepo {
	return &WebhookRepo{pool: pool}
}

// LogDelivery inserts an audit record directly on the pool so rollbacks never erase failed/late attempts.
func (r *WebhookRepo) LogDelivery(ctx context.Context, w *domain.PaymentWebhook) error {
	if w.ID == uuid.Nil {
		w.ID = uuid.New()
	}
	const query = `
		INSERT INTO payment_webhooks (
			id, transaction_id, raw_order_id, parsed_order_id, status_code,
			gross_amount, transaction_status, outcome, requires_refund, raw_payload
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING received_at`

	if err := r.pool.QueryRow(ctx, query,
		w.ID, w.TransactionID, w.RawOrderID, w.ParsedOrderID, w.StatusCode,
		w.GrossAmount, w.TransactionStatus, string(w.Outcome), w.RequiresRefund, w.RawPayload,
	).Scan(&w.ReceivedAt); err != nil {
		return fmt.Errorf("inserting payment webhook audit log: %w", err)
	}
	return nil
}
```

- [ ] **Step 2: Implement `internal/repository/order_repo.go`**

Create `internal/repository/order_repo.go` (strict lock ordering: `advisory_xact_lock` -> `orders ASC` -> `ticket_types ASC`, `invoice_seq` nextval, atomic multi-item checkout, cancel, batch `SKIP LOCKED` expiry, and idempotent payment confirmation with `crypto/rand` ticket codes):
```go
package repository

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/torikh42/event-ticketing/internal/domain"
)

// CheckoutItemInput specifies one ticket tier and desired quantity in an order creation request.
type CheckoutItemInput struct {
	TicketTypeID uuid.UUID
	Quantity     int32
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

// CreateOrder executes dual-locked multi-item ticket reservation with 15-minute expiry.
func (r *OrderRepo) CreateOrder(ctx context.Context, eventID, buyerID uuid.UUID, items []CheckoutItemInput, now time.Time) (*domain.Order, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("beginning create order tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 1. Verify event exists.
	var exists bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM events WHERE id = $1)", eventID).Scan(&exists); err != nil {
		return nil, fmt.Errorf("checking event existence: %w", err)
	}
	if !exists {
		return nil, domain.NewDomainError(http.StatusNotFound, domain.ErrCodeEventNotFound, "event not found")
	}

	// 2. Serialize concurrent bookings from the same buyer on the same event.
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1), hashtext($2))", eventID.String(), buyerID.String()); err != nil {
		return nil, fmt.Errorf("acquiring buyer-event advisory lock: %w", err)
	}

	// 3. Lazy-expire overdue orders for this event via SKIP LOCKED so freed quota is immediately available.
	if _, err := expireDueOrdersInTx(ctx, tx, &eventID, 50, "lazy-check", now); err != nil {
		return nil, err
	}

	// 4. Enforce max 4 active/paid tickets per buyer per event.
	var existingQty int32
	const sumBuyerSQL = `
		SELECT COALESCE(SUM(total_tickets), 0)::int
		FROM orders
		WHERE event_id = $1 AND buyer_id = $2 AND status IN ('WAITING_PAYMENT', 'PAID')`
	if err := tx.QueryRow(ctx, sumBuyerSQL, eventID, buyerID).Scan(&existingQty); err != nil {
		return nil, fmt.Errorf("checking buyer ticket allowance: %w", err)
	}

	var requestedQty int32
	tierIDs := make([]uuid.UUID, 0, len(items))
	reqByTier := make(map[uuid.UUID]int32, len(items))
	for _, it := range items {
		requestedQty += it.Quantity
		tierIDs = append(tierIDs, it.TicketTypeID)
		reqByTier[it.TicketTypeID] = it.Quantity
	}
	if existingQty+requestedQty > domain.MaxTicketsPerBuyerPerEvent {
		return nil, domain.NewBuyerLimitExceededError(
			fmt.Sprintf("buyer ticket limit exceeded: active/paid=%d, requested=%d, max=%d", existingQty, requestedQty, domain.MaxTicketsPerBuyerPerEvent),
		)
	}

	// 5. Lock requested ticket types in ascending ID order to eliminate deadlocks.
	type lockedTier struct {
		id        uuid.UUID
		name      string
		priceIDR  int64
		remaining int32
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
	tiersMap := make(map[uuid.UUID]lockedTier, len(tierIDs))
	for rows.Next() {
		var lt lockedTier
		if err := rows.Scan(&lt.id, &lt.name, &lt.priceIDR, &lt.remaining); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scanning locked ticket type: %w", err)
		}
		tiersMap[lt.id] = lt
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterating locked ticket types: %w", err)
	}
	rows.Close()

	if len(tiersMap) != len(tierIDs) {
		return nil, domain.NewDomainError(http.StatusNotFound, domain.ErrCodeTicketTypeNotFound, "one or more ticket types do not belong to this event")
	}

	var totalAmount int64
	for _, it := range items {
		lt := tiersMap[it.TicketTypeID]
		if lt.remaining < it.Quantity {
			return nil, domain.NewQuotaExhaustedError(
				fmt.Sprintf("insufficient quota for tier %q: requested=%d, remaining=%d", lt.name, it.Quantity, lt.remaining),
			)
		}
		totalAmount += lt.priceIDR * int64(it.Quantity)
	}

	// 6. Deduct quota using already-locked tier order.
	for _, id := range tierIDs {
		qty := reqByTier[id]
		if _, err := tx.Exec(ctx, "UPDATE ticket_types SET remaining_quota = remaining_quota - $1 WHERE id = $2", qty, id); err != nil {
			return nil, fmt.Errorf("decrementing ticket quota: %w", err)
		}
	}

	var seq int64
	if err := tx.QueryRow(ctx, "SELECT nextval('invoice_seq')").Scan(&seq); err != nil {
		return nil, fmt.Errorf("generating invoice sequence: %w", err)
	}
	invoiceNumber := fmt.Sprintf("INV-%s-%06d", now.UTC().Format("2006"), seq)

	orderID := uuid.New()
	expiresAt := now.UTC().Add(domain.OrderHoldDuration)
	const insertOrderSQL = `
		INSERT INTO orders (id, invoice_number, event_id, buyer_id, total_tickets, total_amount_idr, status, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`
	if _, err := tx.Exec(ctx, insertOrderSQL,
		orderID, invoiceNumber, eventID, buyerID, requestedQty, totalAmount,
		string(domain.OrderStatusWaitingPayment), expiresAt, now.UTC(),
	); err != nil {
		return nil, fmt.Errorf("inserting order: %w", err)
	}

	orderItems := make([]domain.OrderItem, 0, len(items))
	const insertItemSQL = `
		INSERT INTO order_items (id, order_id, ticket_type_id, quantity, unit_price_idr, subtotal_idr)
		VALUES ($1, $2, $3, $4, $5, $6)`
	for _, it := range items {
		lt := tiersMap[it.TicketTypeID]
		sub := lt.priceIDR * int64(it.Quantity)
		itemID := uuid.New()
		if _, err := tx.Exec(ctx, insertItemSQL, itemID, orderID, it.TicketTypeID, it.Quantity, lt.priceIDR, sub); err != nil {
			return nil, fmt.Errorf("inserting order item: %w", err)
		}
		orderItems = append(orderItems, domain.OrderItem{
			ID:             itemID,
			OrderID:        orderID,
			TicketTypeID:   it.TicketTypeID,
			TicketTypeName: lt.name,
			Quantity:       it.Quantity,
			UnitPriceIDR:   lt.priceIDR,
			SubtotalIDR:    sub,
		})
	}

	if err := insertOrderLog(ctx, tx, orderID, nil, string(domain.OrderStatusWaitingPayment), &buyerID, "buyer", "order created; quota reserved for 15 minutes", now.UTC()); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing create order tx: %w", err)
	}

	return &domain.Order{
		ID:             orderID,
		InvoiceNumber:  invoiceNumber,
		EventID:        eventID,
		BuyerID:        buyerID,
		TotalTickets:   requestedQty,
		TotalAmountIDR: totalAmount,
		Status:         domain.OrderStatusWaitingPayment,
		ExpiresAt:      expiresAt,
		CreatedAt:      now.UTC(),
		Items:          orderItems,
	}, nil
}

// CancelOrder cancels an unpaid order, restores quota atomically, and rejects expired/paid orders cleanly.
func (r *OrderRepo) CancelOrder(ctx context.Context, orderID, buyerID uuid.UUID, now time.Time) (*domain.Order, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("beginning cancel order tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	order, err := lockOrderForUpdate(ctx, tx, orderID)
	if err != nil {
		return nil, err
	}
	if order.BuyerID != buyerID {
		return nil, domain.NewDomainError(http.StatusForbidden, domain.ErrCodeForbidden, "cannot cancel another buyer's order")
	}

	if err := order.CanCancel(now.UTC()); err != nil {
		var de *domain.DomainError
		if errors.As(err, &de) && de.Code == domain.ErrCodeOrderExpired && order.Status == domain.OrderStatusWaitingPayment {
			// Ensure overdue order is moved to EXPIRED and its quota is returned cleanly.
			if expErr := markOrderExpiredAndRestock(ctx, tx, order, "lazy-check", now.UTC()); expErr != nil {
				return nil, expErr
			}
			if commitErr := tx.Commit(ctx); commitErr != nil {
				return nil, fmt.Errorf("committing lazy expiry on cancel: %w", commitErr)
			}
		}
		return nil, err
	}

	if err := restockOrderQuota(ctx, tx, []uuid.UUID{order.ID}); err != nil {
		return nil, err
	}

	cancelledAt := now.UTC()
	reason := "cancelled by buyer before payment"
	const updateSQL = `
		UPDATE orders
		SET status = 'CANCELLED', cancelled_at = $2, cancelled_reason = $3
		WHERE id = $1`
	if _, err := tx.Exec(ctx, updateSQL, order.ID, cancelledAt, reason); err != nil {
		return nil, fmt.Errorf("updating order to CANCELLED: %w", err)
	}

	prev := string(domain.OrderStatusWaitingPayment)
	if err := insertOrderLog(ctx, tx, order.ID, &prev, string(domain.OrderStatusCancelled), &buyerID, "buyer", reason, cancelledAt); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing cancel order tx: %w", err)
	}

	return r.FindByID(ctx, order.ID)
}

// ConfirmPayment validates order state inside row lock, transitions WAITING_PAYMENT -> PAID, and issues unique tickets once.
func (r *OrderRepo) ConfirmPayment(ctx context.Context, orderID uuid.UUID, grossAmount int64, now time.Time) (*PaymentConfirmResult, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("beginning confirm payment tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	order, err := lockOrderForUpdate(ctx, tx, orderID)
	if err != nil {
		return nil, err
	}

	if order.TotalAmountIDR != grossAmount {
		return nil, domain.NewPaymentAmountMismatchError(
			fmt.Sprintf("gross_amount %d does not match order total %d", grossAmount, order.TotalAmountIDR),
		)
	}

	if err := order.CanPay(now.UTC()); err != nil {
		var de *domain.DomainError
		if errors.As(err, &de) && de.Code == domain.ErrCodeOrderExpired && order.Status == domain.OrderStatusWaitingPayment {
			if expErr := markOrderExpiredAndRestock(ctx, tx, order, "lazy-check", now.UTC()); expErr != nil {
				return nil, expErr
			}
			if commitErr := tx.Commit(ctx); commitErr != nil {
				return nil, fmt.Errorf("committing lazy expiry on late webhook: %w", commitErr)
			}
		}
		return nil, err
	}

	// Idempotent replay when already PAID.
	if order.Status == domain.OrderStatusPaid {
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("committing idempotent payment check: %w", err)
		}
		full, err := r.FindByID(ctx, order.ID)
		if err != nil {
			return nil, err
		}
		return &PaymentConfirmResult{Order: full, IdempotentReplay: true}, nil
	}

	paidAt := now.UTC()
	if _, err := tx.Exec(ctx, "UPDATE orders SET status = 'PAID', paid_at = $2 WHERE id = $1", order.ID, paidAt); err != nil {
		return nil, fmt.Errorf("updating order to PAID: %w", err)
	}

	items, err := loadOrderItems(ctx, tx, order.ID)
	if err != nil {
		return nil, err
	}

	const insertTicketSQL = `
		INSERT INTO tickets (id, ticket_code, order_id, order_item_id, event_id, ticket_type_id, owner_id, seat_sequence, issued_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`
	for _, it := range items {
		for seat := int32(1); seat <= it.Quantity; seat++ {
			code, genErr := generateTicketCode(paidAt)
			if genErr != nil {
				return nil, genErr
			}
			if _, err := tx.Exec(ctx, insertTicketSQL,
				uuid.New(), code, order.ID, it.ID, order.EventID, it.TicketTypeID, order.BuyerID, seat, paidAt,
			); err != nil {
				return nil, fmt.Errorf("inserting ticket: %w", err)
			}
		}
	}

	prev := string(domain.OrderStatusWaitingPayment)
	if err := insertOrderLog(ctx, tx, order.ID, &prev, string(domain.OrderStatusPaid), nil, "webhook", "payment confirmed and tickets issued", paidAt); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing confirm payment tx: %w", err)
	}

	full, err := r.FindByID(ctx, order.ID)
	if err != nil {
		return nil, err
	}
	return &PaymentConfirmResult{Order: full, IdempotentReplay: false}, nil
}

// FailPayment marks an unexpired WAITING_PAYMENT order as CANCELLED/released when gateway sends failure/expire notification.
func (r *OrderRepo) FailPayment(ctx context.Context, orderID uuid.UUID, reason string, now time.Time) (domain.WebhookOutcome, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("beginning fail payment tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	order, err := lockOrderForUpdate(ctx, tx, orderID)
	if err != nil {
		return "", err
	}

	if order.Status == domain.OrderStatusPaid {
		_ = tx.Commit(ctx)
		return domain.OutcomeIgnoredDuplicate, nil
	}
	if order.Status == domain.OrderStatusExpired || order.Status == domain.OrderStatusCancelled {
		_ = tx.Commit(ctx)
		return domain.OutcomeIgnoredDuplicate, nil
	}

	if err := markOrderExpiredAndRestock(ctx, tx, order, "webhook", now.UTC()); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, "UPDATE orders SET cancelled_reason = $2 WHERE id = $1", order.ID, reason); err != nil {
		return "", fmt.Errorf("recording payment failure reason: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("committing fail payment tx: %w", err)
	}
	return domain.OutcomeProcessedFailed, nil
}

// ExpireDueOrders sweeps up to limit overdue WAITING_PAYMENT orders (optionally scoped to one event).
func (r *OrderRepo) ExpireDueOrders(ctx context.Context, eventID *uuid.UUID, limit int, actorType string, now time.Time) (int, error) {
	var hasDue bool
	var checkErr error
	if eventID != nil {
		checkErr = r.pool.QueryRow(ctx,
			"SELECT EXISTS(SELECT 1 FROM orders WHERE event_id = $1 AND status = 'WAITING_PAYMENT' AND expires_at <= $2)",
			*eventID, now.UTC(),
		).Scan(&hasDue)
	} else {
		checkErr = r.pool.QueryRow(ctx,
			"SELECT EXISTS(SELECT 1 FROM orders WHERE status = 'WAITING_PAYMENT' AND expires_at <= $1)",
			now.UTC(),
		).Scan(&hasDue)
	}
	if checkErr != nil {
		return 0, fmt.Errorf("checking partial expiry index: %w", checkErr)
	}
	if !hasDue {
		return 0, nil
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("beginning expire due orders tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	count, err := expireDueOrdersInTx(ctx, tx, eventID, limit, actorType, now.UTC())
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("committing expire due orders tx: %w", err)
	}
	return count, nil
}

// FindByID loads a full order including items, tickets, and transition logs.
func (r *OrderRepo) FindByID(ctx context.Context, orderID uuid.UUID) (*domain.Order, error) {
	const queryOrder = `
		SELECT id, invoice_number, event_id, buyer_id, total_tickets, total_amount_idr,
		       status, expires_at, paid_at, cancelled_at, expired_at, cancelled_reason, created_at
		FROM orders
		WHERE id = $1`
	var o domain.Order
	var status string
	err := r.pool.QueryRow(ctx, queryOrder, orderID).Scan(
		&o.ID, &o.InvoiceNumber, &o.EventID, &o.BuyerID, &o.TotalTickets, &o.TotalAmountIDR,
		&status, &o.ExpiresAt, &o.PaidAt, &o.CancelledAt, &o.ExpiredAt, &o.CancelledReason, &o.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.NewDomainError(http.StatusNotFound, domain.ErrCodeOrderNotFound, "order not found")
		}
		return nil, fmt.Errorf("querying order: %w", err)
	}
	o.Status = domain.OrderStatus(status)

	items, err := loadOrderItems(ctx, r.pool, orderID)
	if err != nil {
		return nil, err
	}
	o.Items = items

	tickets, err := loadOrderTickets(ctx, r.pool, orderID)
	if err != nil {
		return nil, err
	}
	o.Tickets = tickets

	logs, err := loadOrderLogs(ctx, r.pool, orderID)
	if err != nil {
		return nil, err
	}
	o.Logs = logs

	return &o, nil
}

// ListByBuyer loads all orders belonging to a specific buyer, most recent first.
func (r *OrderRepo) ListByBuyer(ctx context.Context, buyerID uuid.UUID) ([]domain.Order, error) {
	const queryOrders = `
		SELECT id, invoice_number, event_id, buyer_id, total_tickets, total_amount_idr,
		       status, expires_at, paid_at, cancelled_at, expired_at, cancelled_reason, created_at
		FROM orders
		WHERE buyer_id = $1
		ORDER BY created_at DESC`
	rows, err := r.pool.Query(ctx, queryOrders, buyerID)
	if err != nil {
		return nil, fmt.Errorf("listing buyer orders: %w", err)
	}
	defer rows.Close()

	var orders []domain.Order
	for rows.Next() {
		var o domain.Order
		var status string
		if err := rows.Scan(
			&o.ID, &o.InvoiceNumber, &o.EventID, &o.BuyerID, &o.TotalTickets, &o.TotalAmountIDR,
			&status, &o.ExpiresAt, &o.PaidAt, &o.CancelledAt, &o.ExpiredAt, &o.CancelledReason, &o.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning buyer order: %w", err)
		}
		o.Status = domain.OrderStatus(status)
		orders = append(orders, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating buyer orders: %w", err)
	}
	rows.Close()

	for i := range orders {
		items, err := loadOrderItems(ctx, r.pool, orders[i].ID)
		if err != nil {
			return nil, err
		}
		orders[i].Items = items
		tickets, err := loadOrderTickets(ctx, r.pool, orders[i].ID)
		if err != nil {
			return nil, err
		}
		orders[i].Tickets = tickets
	}
	return orders, nil
}

type queryRunner interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func lockOrderForUpdate(ctx context.Context, tx pgx.Tx, orderID uuid.UUID) (*domain.Order, error) {
	const query = `
		SELECT id, invoice_number, event_id, buyer_id, total_tickets, total_amount_idr,
		       status, expires_at, paid_at, cancelled_at, expired_at, cancelled_reason, created_at
		FROM orders
		WHERE id = $1
		FOR UPDATE`
	var o domain.Order
	var status string
	err := tx.QueryRow(ctx, query, orderID).Scan(
		&o.ID, &o.InvoiceNumber, &o.EventID, &o.BuyerID, &o.TotalTickets, &o.TotalAmountIDR,
		&status, &o.ExpiresAt, &o.PaidAt, &o.CancelledAt, &o.ExpiredAt, &o.CancelledReason, &o.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.NewDomainError(http.StatusNotFound, domain.ErrCodeOrderNotFound, "order not found")
		}
		return nil, fmt.Errorf("locking order for update: %w", err)
	}
	o.Status = domain.OrderStatus(status)
	return &o, nil
}

func expireDueOrdersInTx(ctx context.Context, tx pgx.Tx, eventID *uuid.UUID, limit int, actorType string, now time.Time) (int, error) {
	var rows pgx.Rows
	var err error
	if eventID != nil {
		rows, err = tx.Query(ctx, `
			SELECT id FROM orders
			WHERE event_id = $1 AND status = 'WAITING_PAYMENT' AND expires_at <= $2
			ORDER BY id ASC
			FOR UPDATE SKIP LOCKED
			LIMIT $3`, *eventID, now, limit)
	} else {
		rows, err = tx.Query(ctx, `
			SELECT id FROM orders
			WHERE status = 'WAITING_PAYMENT' AND expires_at <= $1
			ORDER BY id ASC
			FOR UPDATE SKIP LOCKED
			LIMIT $2`, now, limit)
	}
	if err != nil {
		return 0, fmt.Errorf("selecting overdue orders: %w", err)
	}

	var dueIDs []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scanning overdue order id: %w", err)
		}
		dueIDs = append(dueIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("iterating overdue order ids: %w", err)
	}
	rows.Close()

	if len(dueIDs) == 0 {
		return 0, nil
	}

	if err := restockOrderQuota(ctx, tx, dueIDs); err != nil {
		return 0, err
	}

	reason := "payment deadline (15m) expired automatically"
	if _, err := tx.Exec(ctx, `
		UPDATE orders
		SET status = 'EXPIRED', expired_at = $2, cancelled_reason = $3
		WHERE id = ANY($1)`, dueIDs, now, reason); err != nil {
		return 0, fmt.Errorf("marking orders expired: %w", err)
	}

	prev := string(domain.OrderStatusWaitingPayment)
	for _, id := range dueIDs {
		if err := insertOrderLog(ctx, tx, id, &prev, string(domain.OrderStatusExpired), nil, actorType, reason, now); err != nil {
			return 0, err
		}
	}
	return len(dueIDs), nil
}

func markOrderExpiredAndRestock(ctx context.Context, tx pgx.Tx, order *domain.Order, actorType string, now time.Time) error {
	if err := restockOrderQuota(ctx, tx, []uuid.UUID{order.ID}); err != nil {
		return err
	}
	reason := "payment deadline (15m) expired"
	if _, err := tx.Exec(ctx, "UPDATE orders SET status = 'EXPIRED', expired_at = $2, cancelled_reason = $3 WHERE id = $1", order.ID, now, reason); err != nil {
		return fmt.Errorf("marking single order expired: %w", err)
	}
	prev := string(domain.OrderStatusWaitingPayment)
	return insertOrderLog(ctx, tx, order.ID, &prev, string(domain.OrderStatusExpired), nil, actorType, reason, now)
}

func restockOrderQuota(ctx context.Context, tx pgx.Tx, orderIDs []uuid.UUID) error {
	// Always lock ticket_types in ascending ID order before mutating balances to prevent AB-BA deadlocks.
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
	rows, err := tx.Query(ctx, lockSQL, orderIDs)
	if err != nil {
		return fmt.Errorf("locking ticket types for restock: %w", err)
	}
	rows.Close()

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

func insertOrderLog(ctx context.Context, tx pgx.Tx, orderID uuid.UUID, fromStatus *string, toStatus string, actorID *uuid.UUID, actorType, note string, now time.Time) error {
	const sqlLog = `
		INSERT INTO order_logs (id, order_id, from_status, to_status, actor_id, actor_type, note, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`
	if _, err := tx.Exec(ctx, sqlLog, uuid.New(), orderID, fromStatus, toStatus, actorID, actorType, note, now); err != nil {
		return fmt.Errorf("inserting order log: %w", err)
	}
	return nil
}

func loadOrderItems(ctx context.Context, q queryRunner, orderID uuid.UUID) ([]domain.OrderItem, error) {
	const sqlItems = `
		SELECT oi.id, oi.order_id, oi.ticket_type_id, tt.name, oi.quantity, oi.unit_price_idr, oi.subtotal_idr
		FROM order_items AS oi
		JOIN ticket_types AS tt ON tt.id = oi.ticket_type_id
		WHERE oi.order_id = $1
		ORDER BY tt.price_idr ASC`
	rows, err := q.Query(ctx, sqlItems, orderID)
	if err != nil {
		return nil, fmt.Errorf("querying order items: %w", err)
	}
	defer rows.Close()

	var items []domain.OrderItem
	for rows.Next() {
		var it domain.OrderItem
		if err := rows.Scan(&it.ID, &it.OrderID, &it.TicketTypeID, &it.TicketTypeName, &it.Quantity, &it.UnitPriceIDR, &it.SubtotalIDR); err != nil {
			return nil, fmt.Errorf("scanning order item: %w", err)
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

func loadOrderTickets(ctx context.Context, q queryRunner, orderID uuid.UUID) ([]domain.Ticket, error) {
	const sqlTickets = `
		SELECT id, ticket_code, order_id, order_item_id, event_id, ticket_type_id, owner_id, seat_sequence, issued_at
		FROM tickets
		WHERE order_id = $1
		ORDER BY issued_at ASC, seat_sequence ASC`
	rows, err := q.Query(ctx, sqlTickets, orderID)
	if err != nil {
		return nil, fmt.Errorf("querying order tickets: %w", err)
	}
	defer rows.Close()

	var tickets []domain.Ticket
	for rows.Next() {
		var tk domain.Ticket
		if err := rows.Scan(&tk.ID, &tk.TicketCode, &tk.OrderID, &tk.OrderItemID, &tk.EventID, &tk.TicketTypeID, &tk.OwnerID, &tk.SeatSequence, &tk.IssuedAt); err != nil {
			return nil, fmt.Errorf("scanning order ticket: %w", err)
		}
		tickets = append(tickets, tk)
	}
	return tickets, rows.Err()
}

func loadOrderLogs(ctx context.Context, q queryRunner, orderID uuid.UUID) ([]domain.OrderLog, error) {
	const sqlLogs = `
		SELECT id, order_id, from_status, to_status, actor_id, actor_type, note, created_at
		FROM order_logs
		WHERE order_id = $1
		ORDER BY created_at ASC`
	rows, err := q.Query(ctx, sqlLogs, orderID)
	if err != nil {
		return nil, fmt.Errorf("querying order logs: %w", err)
	}
	defer rows.Close()

	var logs []domain.OrderLog
	for rows.Next() {
		var lg domain.OrderLog
		if err := rows.Scan(&lg.ID, &lg.OrderID, &lg.FromStatus, &lg.ToStatus, &lg.ActorID, &lg.ActorType, &lg.Note, &lg.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning order log: %w", err)
		}
		logs = append(logs, lg)
	}
	return logs, rows.Err()
}

func generateTicketCode(now time.Time) (string, error) {
	buf := make([]byte, 5)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generating ticket code randomness: %w", err)
	}
	return fmt.Sprintf("TKT-%s-%s", now.UTC().Format("060102"), strings.ToUpper(hex.EncodeToString(buf))), nil
}
```

- [ ] **Step 3: Compile check order and webhook repositories**

Run: `go build ./internal/repository/...`
Expected: PASS with exit code 0.

- [ ] **Step 4: Commit order & webhook repositories**

```bash
git add internal/repository/order_repo.go internal/repository/webhook_repo.go
git commit -m "feat(repo): add dual-lock multi-item checkout, SKIP LOCKED expiry, and idempotent payment repo"
```

---

### Task 6: Service Layer with Interface Mocks & Unit Tests (`internal/service/`)

**Files:**
- Create: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\internal\service\interfaces.go`
- Create: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\internal\service\auth_service.go`
- Create: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\internal\service\event_service.go`
- Create: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\internal\service\order_service.go`
- Create: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\internal\service\payment_service.go`
- Create: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\internal\service\demo_service.go`
- Test: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\internal\service\service_test.go`

- [ ] **Step 1: Write failing unit tests for service rules (role guards, checkout quantity cap, webhook security & late-payment audit logging)**

Create `internal/service/service_test.go`:
```go
package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/torikh42/event-ticketing/internal/domain"
	"github.com/torikh42/event-ticketing/internal/repository"
	"github.com/torikh42/event-ticketing/internal/service"
)

type stubOrderRepo struct {
	expireCalls  int
	confirmErr   error
	confirmRes   *repository.PaymentConfirmResult
	foundOrder   *domain.Order
	findErr      error
}

func (s *stubOrderRepo) CreateOrder(context.Context, uuid.UUID, uuid.UUID, []repository.CheckoutItemInput, time.Time) (*domain.Order, error) {
	return s.foundOrder, nil
}
func (s *stubOrderRepo) CancelOrder(context.Context, uuid.UUID, uuid.UUID, time.Time) (*domain.Order, error) {
	return s.foundOrder, nil
}
func (s *stubOrderRepo) ConfirmPayment(context.Context, uuid.UUID, int64, time.Time) (*repository.PaymentConfirmResult, error) {
	return s.confirmRes, s.confirmErr
}
func (s *stubOrderRepo) FailPayment(context.Context, uuid.UUID, string, time.Time) (domain.WebhookOutcome, error) {
	return domain.OutcomeProcessedFailed, nil
}
func (s *stubOrderRepo) ExpireDueOrders(context.Context, *uuid.UUID, int, string, time.Time) (int, error) {
	s.expireCalls++
	return 0, nil
}
func (s *stubOrderRepo) FindByID(context.Context, uuid.UUID) (*domain.Order, error) {
	return s.foundOrder, s.findErr
}
func (s *stubOrderRepo) ListByBuyer(context.Context, uuid.UUID) ([]domain.Order, error) {
	return nil, nil
}

type stubWebhookRepo struct {
	logged []*domain.PaymentWebhook
}

func (s *stubWebhookRepo) LogDelivery(_ context.Context, w *domain.PaymentWebhook) error {
	s.logged = append(s.logged, w)
	return nil
}

type stubEventRepo struct{}

func (stubEventRepo) CreateWithTiers(context.Context, *domain.Event) error { return nil }
func (stubEventRepo) ListAll(context.Context) ([]domain.Event, error)      { return nil, nil }
func (stubEventRepo) FindByID(context.Context, uuid.UUID) (*domain.Event, error) {
	return &domain.Event{}, nil
}

func TestEventService_CreateRejectsBuyerAndLessThanTwoTiers(t *testing.T) {
	t.Parallel()

	svc := service.NewEventService(stubEventRepo{}, &stubOrderRepo{})

	buyer := domain.Actor{ID: uuid.New(), Role: domain.RoleBuyer}
	_, err := svc.CreateEvent(t.Context(), buyer, service.CreateEventInput{
		Title: "Bad", Venue: "Hall", EventDate: time.Now().Add(24 * time.Hour),
		TicketTypes: []service.CreateTierInput{{Name: "Reg", PriceIDR: 100, TotalQuota: 10}, {Name: "VIP", PriceIDR: 200, TotalQuota: 10}},
	})
	de, ok := errors.AsType[*domain.DomainError](err)
	if !ok || de.Code != domain.ErrCodeForbidden {
		t.Fatalf("expected FORBIDDEN for buyer, got %v", err)
	}

	org := domain.Actor{ID: uuid.New(), Role: domain.RoleOrganizer}
	_, err = svc.CreateEvent(t.Context(), org, service.CreateEventInput{
		Title: "Single", Venue: "Hall", EventDate: time.Now().Add(24 * time.Hour),
		TicketTypes: []service.CreateTierInput{{Name: "Reg", PriceIDR: 100, TotalQuota: 10}},
	})
	de, ok = errors.AsType[*domain.DomainError](err)
	if !ok || de.Code != domain.ErrCodeValidation {
		t.Fatalf("expected VALIDATION_ERROR for <2 tiers, got %v", err)
	}
}

func TestPaymentService_LateExpiredWebhookLogsRefundFlag(t *testing.T) {
	t.Parallel()

	orderID := uuid.New()
	secret := "super-secret"
	orderRepo := &stubOrderRepo{
		confirmErr: domain.NewOrderExpiredError("order reservation window has expired"),
	}
	webhookRepo := &stubWebhookRepo{}
	svc := service.NewPaymentService(orderRepo, webhookRepo, secret)

	sig := domain.ComputeSHA512Signature(orderID.String(), "200", 300000, secret)
	_, err := svc.ProcessWebhook(t.Context(), service.WebhookInput{
		TransactionID:     "tx-late-1",
		OrderID:           orderID.String(),
		StatusCode:        "200",
		GrossAmount:       300000,
		TransactionStatus: "settlement",
		SignatureKey:      sig,
		RawPayload:        []byte(`{}`),
	})
	de, ok := errors.AsType[*domain.DomainError](err)
	if !ok || de.Code != domain.ErrCodeOrderExpired {
		t.Fatalf("expected ORDER_EXPIRED, got %v", err)
	}
	if len(webhookRepo.logged) != 1 {
		t.Fatalf("expected 1 webhook log entry, got %d", len(webhookRepo.logged))
	}
	if webhookRepo.logged[0].Outcome != domain.OutcomeRejectedLateExpired || !webhookRepo.logged[0].RequiresRefund {
		t.Fatalf("expected REJECTED_LATE_EXPIRED with RequiresRefund=true, got %+v", webhookRepo.logged[0])
	}
}
```

- [ ] **Step 2: Run unit test to verify it fails**

Run: `go test ./internal/service/...`
Expected: FAIL (`internal/service` does not exist yet).

- [ ] **Step 3: Implement `interfaces.go`, `auth_service.go`, `event_service.go`, `order_service.go`, `payment_service.go`, and `demo_service.go`**

Create `internal/service/interfaces.go`:
```go
// Package service implements core business use cases over consumer-defined repository interfaces.
package service

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/torikh42/event-ticketing/internal/domain"
	"github.com/torikh42/event-ticketing/internal/repository"
)

// UserRepository defines persistence operations required by AuthService and auth middleware.
type UserRepository interface {
	Create(ctx context.Context, u *domain.User) error
	FindByEmail(ctx context.Context, email string) (*domain.User, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.User, error)
	EnsureDemoUser(ctx context.Context, id uuid.UUID, role domain.Role) error
}

// EventRepository defines persistence operations required by EventService.
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
```

Create `internal/service/auth_service.go`:
```go
package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/torikh42/event-ticketing/internal/domain"
	"golang.org/x/crypto/bcrypt"
)

// AuthService manages registration, login, and JWT issuance/verification.
type AuthService struct {
	users     UserRepository
	jwtSecret []byte
}

// NewAuthService constructs an AuthService.
func NewAuthService(users UserRepository, jwtSecret string) *AuthService {
	return &AuthService{
		users:     users,
		jwtSecret: []byte(jwtSecret),
	}
}

// RegisterInput carries registration fields.
type RegisterInput struct {
	Email    string
	Name     string
	Password string
	Role     domain.Role
}

// AuthResult holds user details alongside a signed JWT token.
type AuthResult struct {
	User        *domain.User `json:"user"`
	AccessToken string       `json:"access_token"`
}

// Register creates a user with hashed credentials and issues a JWT token.
func (s *AuthService) Register(ctx context.Context, in RegisterInput) (*AuthResult, error) {
	if in.Role != domain.RoleOrganizer && in.Role != domain.RoleBuyer {
		return nil, domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeValidation, "role must be ORGANIZER or BUYER")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), 10)
	if err != nil {
		return nil, fmt.Errorf("hashing password: %w", err)
	}

	u := &domain.User{
		ID:           uuid.New(),
		Email:        strings.ToLower(strings.TrimSpace(in.Email)),
		Name:         strings.TrimSpace(in.Name),
		PasswordHash: string(hash),
		Role:         in.Role,
	}
	if err := s.users.Create(ctx, u); err != nil {
		return nil, err
	}

	tok, err := s.signJWT(u)
	if err != nil {
		return nil, err
	}
	return &AuthResult{User: u, AccessToken: tok}, nil
}

// Login authenticates an existing user and returns a signed JWT token.
func (s *AuthService) Login(ctx context.Context, email, password string) (*AuthResult, error) {
	u, err := s.users.FindByEmail(ctx, strings.ToLower(strings.TrimSpace(email)))
	if err != nil {
		return nil, err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		return nil, domain.NewDomainError(http.StatusUnauthorized, domain.ErrCodeInvalidCredentials, "invalid email or password")
	}

	tok, err := s.signJWT(u)
	if err != nil {
		return nil, err
	}
	return &AuthResult{User: u, AccessToken: tok}, nil
}

// ParseJWT validates a bearer token and extracts the authenticated Actor.
func (s *AuthService) ParseJWT(tokenStr string) (domain.Actor, error) {
	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return s.jwtSecret, nil
	})
	if err != nil || !token.Valid {
		return domain.Actor{}, domain.NewDomainError(http.StatusUnauthorized, domain.ErrCodeUnauthorized, "invalid or expired token")
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return domain.Actor{}, domain.NewDomainError(http.StatusUnauthorized, domain.ErrCodeUnauthorized, "invalid token claims")
	}
	subStr, _ := claims["sub"].(string)
	uid, err := uuid.Parse(subStr)
	if err != nil {
		return domain.Actor{}, domain.NewDomainError(http.StatusUnauthorized, domain.ErrCodeUnauthorized, "invalid subject UUID")
	}
	roleStr, _ := claims["role"].(string)
	emailStr, _ := claims["email"].(string)

	role := domain.Role(roleStr)
	if role != domain.RoleOrganizer && role != domain.RoleBuyer {
		return domain.Actor{}, domain.NewDomainError(http.StatusUnauthorized, domain.ErrCodeUnauthorized, "invalid token role")
	}

	return domain.Actor{ID: uid, Email: emailStr, Role: role}, nil
}

// EnsureDemoUser delegates synthetic demo user creation to UserRepository.
func (s *AuthService) EnsureDemoUser(ctx context.Context, id uuid.UUID, role domain.Role) error {
	return s.users.EnsureDemoUser(ctx, id, role)
}

func (s *AuthService) signJWT(u *domain.User) (string, error) {
	now := time.Now().UTC()
	claims := jwt.MapClaims{
		"sub":   u.ID.String(),
		"email": u.Email,
		"role":  string(u.Role),
		"iat":   now.Unix(),
		"exp":   now.Add(24 * time.Hour).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(s.jwtSecret)
	if err != nil {
		return "", fmt.Errorf("signing jwt: %w", err)
	}
	return signed, nil
}
```

Create `internal/service/event_service.go`:
```go
package service

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/torikh42/event-ticketing/internal/domain"
)

// CreateTierInput defines input for creating one ticket tier.
type CreateTierInput struct {
	Name       string
	PriceIDR   int64
	TotalQuota int32
}

// CreateEventInput defines input for creating an event with ticket tiers.
type CreateEventInput struct {
	Title       string
	Description string
	Venue       string
	EventDate   time.Time
	TicketTypes []CreateTierInput
}

// EventService handles event management and real-time quota views.
type EventService struct {
	events EventRepository
	orders OrderRepository
}

// NewEventService constructs an EventService.
func NewEventService(events EventRepository, orders OrderRepository) *EventService {
	return &EventService{events: events, orders: orders}
}

// CreateEvent enforces ORGANIZER role and minimum 2 distinct ticket tiers.
func (s *EventService) CreateEvent(ctx context.Context, actor domain.Actor, in CreateEventInput) (*domain.Event, error) {
	if actor.Role != domain.RoleOrganizer {
		return nil, domain.NewDomainError(http.StatusForbidden, domain.ErrCodeForbidden, "only organizers can create events")
	}
	if len(in.TicketTypes) < 2 {
		return nil, domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeValidation, "an event must define at least 2 ticket types (e.g. Reguler and VIP)")
	}

	ev := &domain.Event{
		ID:          uuid.New(),
		OrganizerID: actor.ID,
		Title:       strings.TrimSpace(in.Title),
		Description: strings.TrimSpace(in.Description),
		Venue:       strings.TrimSpace(in.Venue),
		EventDate:   in.EventDate.UTC(),
		TicketTypes: make([]domain.TicketType, 0, len(in.TicketTypes)),
	}

	seenNames := make(map[string]struct{}, len(in.TicketTypes))
	for _, tier := range in.TicketTypes {
		name := strings.TrimSpace(tier.Name)
		key := strings.ToLower(name)
		if _, dup := seenNames[key]; dup {
			return nil, domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeValidation, "duplicate ticket type names are not allowed")
		}
		seenNames[key] = struct{}{}

		tt := domain.TicketType{
			ID:             uuid.New(),
			EventID:        ev.ID,
			Name:           name,
			PriceIDR:       tier.PriceIDR,
			TotalQuota:     tier.TotalQuota,
			RemainingQuota: tier.TotalQuota,
		}
		if err := tt.Validate(); err != nil {
			return nil, err
		}
		ev.TicketTypes = append(ev.TicketTypes, tt)
	}

	if err := s.events.CreateWithTiers(ctx, ev); err != nil {
		return nil, err
	}
	return ev, nil
}

// ListEvents performs a lazy expiry pass via partial index and returns real-time remaining quotas.
func (s *EventService) ListEvents(ctx context.Context) ([]domain.Event, error) {
	if _, err := s.orders.ExpireDueOrders(ctx, nil, 50, "lazy-check", time.Now().UTC()); err != nil {
		return nil, err
	}
	return s.events.ListAll(ctx)
}

// GetEvent sweeps any due orders for the target event and returns live remaining quota.
func (s *EventService) GetEvent(ctx context.Context, eventID uuid.UUID) (*domain.Event, error) {
	if _, err := s.orders.ExpireDueOrders(ctx, &eventID, 50, "lazy-check", time.Now().UTC()); err != nil {
		return nil, err
	}
	return s.events.FindByID(ctx, eventID)
}
```

Create `internal/service/order_service.go`:
```go
package service

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/torikh42/event-ticketing/internal/domain"
	"github.com/torikh42/event-ticketing/internal/repository"
)

// CreateOrderInput represents a buyer's multi-item checkout request.
type CreateOrderInput struct {
	EventID uuid.UUID
	Items   []repository.CheckoutItemInput
}

// OrderService orchestrates ticket booking, cancellation, and history lookup.
type OrderService struct {
	orders OrderRepository
}

// NewOrderService constructs an OrderService.
func NewOrderService(orders OrderRepository) *OrderService {
	return &OrderService{orders: orders}
}

// CreateOrder validates total quantity (1..4) and delegates atomic dual-locked creation to repository.
func (s *OrderService) CreateOrder(ctx context.Context, actor domain.Actor, in CreateOrderInput) (*domain.Order, error) {
	if len(in.Items) == 0 {
		return nil, domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeInvalidQuantity, "at least one ticket item is required")
	}

	merged := make(map[uuid.UUID]int32, len(in.Items))
	var totalQty int32
	for _, it := range in.Items {
		if it.Quantity <= 0 {
			return nil, domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeInvalidQuantity, "ticket item quantity must be positive")
		}
		merged[it.TicketTypeID] += it.Quantity
		totalQty += it.Quantity
	}
	if totalQty < 1 || totalQty > domain.MaxTicketsPerBuyerPerEvent {
		return nil, domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeInvalidQuantity, "order total_tickets must be between 1 and 4")
	}

	deduped := make([]repository.CheckoutItemInput, 0, len(merged))
	for tid, qty := range merged {
		deduped = append(deduped, repository.CheckoutItemInput{TicketTypeID: tid, Quantity: qty})
	}

	return s.orders.CreateOrder(ctx, in.EventID, actor.ID, deduped, time.Now().UTC())
}

// CancelOrder cancels an unpaid order owned by the buyer and restores quota.
func (s *OrderService) CancelOrder(ctx context.Context, actor domain.Actor, orderID uuid.UUID) (*domain.Order, error) {
	return s.orders.CancelOrder(ctx, orderID, actor.ID, time.Now().UTC())
}

// ListOrders returns order history and tickets belonging to the calling buyer.
func (s *OrderService) ListOrders(ctx context.Context, actor domain.Actor) ([]domain.Order, error) {
	if _, err := s.orders.ExpireDueOrders(ctx, nil, 50, "lazy-check", time.Now().UTC()); err != nil {
		return nil, err
	}
	return s.orders.ListByBuyer(ctx, actor.ID)
}

// GetOrder returns a single order with items, logs, and tickets after enforcing buyer ownership.
func (s *OrderService) GetOrder(ctx context.Context, actor domain.Actor, orderID uuid.UUID) (*domain.Order, error) {
	o, err := s.orders.FindByID(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if actor.Role != domain.RoleOrganizer && o.BuyerID != actor.ID {
		return nil, domain.NewDomainError(http.StatusForbidden, domain.ErrCodeForbidden, "cannot access another buyer's order")
	}
	return o, nil
}
```

Create `internal/service/payment_service.go`:
```go
package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/torikh42/event-ticketing/internal/domain"
	"github.com/torikh42/event-ticketing/internal/repository"
)

// WebhookInput captures a Midtrans-style signed webhook payload.
type WebhookInput struct {
	TransactionID     string
	OrderID           string
	StatusCode        string
	GrossAmount       int64
	TransactionStatus string
	SignatureKey      string
	RawPayload        []byte
}

// WebhookResult describes the outcome returned to HTTP callers.
type WebhookResult struct {
	Outcome          domain.WebhookOutcome `json:"outcome"`
	IdempotentReplay bool                  `json:"idempotent_replay"`
	Order            *domain.Order         `json:"order,omitempty"`
}

// PaymentService handles signed payment webhooks and simulated demo checkout settlement.
type PaymentService struct {
	orders        OrderRepository
	webhooks      WebhookRepository
	webhookSecret string
}

// NewPaymentService constructs a PaymentService.
func NewPaymentService(orders OrderRepository, webhooks WebhookRepository, webhookSecret string) *PaymentService {
	return &PaymentService{
		orders:        orders,
		webhooks:      webhooks,
		webhookSecret: webhookSecret,
	}
}

// ProcessWebhook verifies signatures fail-closed, preserves audit logs outside rollbacks, and updates order state idempotently.
func (s *PaymentService) ProcessWebhook(ctx context.Context, in WebhookInput) (*WebhookResult, error) {
	rawPayload := in.RawPayload
	if len(rawPayload) == 0 {
		rawPayload = []byte(`{}`)
	}

	parsedOrderID, parseErr := uuid.Parse(strings.TrimSpace(in.OrderID))
	var parsedPtr *uuid.UUID
	if parseErr == nil {
		parsedPtr = &parsedOrderID
	}

	audit := &domain.PaymentWebhook{
		ID:                uuid.New(),
		TransactionID:     in.TransactionID,
		RawOrderID:        in.OrderID,
		ParsedOrderID:     parsedPtr,
		StatusCode:        in.StatusCode,
		GrossAmount:       in.GrossAmount,
		TransactionStatus: in.TransactionStatus,
		RawPayload:        rawPayload,
	}

	if !domain.VerifySHA512Signature(in.OrderID, in.StatusCode, in.GrossAmount, s.webhookSecret, in.SignatureKey) {
		audit.Outcome = domain.OutcomeSignatureInvalid
		_ = s.webhooks.LogDelivery(ctx, audit)
		return nil, domain.NewUnauthorizedWebhookError("invalid or missing webhook signature")
	}

	if parseErr != nil {
		audit.Outcome = domain.OutcomeOrderNotFound
		_ = s.webhooks.LogDelivery(ctx, audit)
		return nil, domain.NewDomainError(http.StatusNotFound, domain.ErrCodeOrderNotFound, "order_id is not a valid UUID")
	}

	status := strings.ToLower(strings.TrimSpace(in.TransactionStatus))
	switch status {
	case "settlement", "capture":
		res, err := s.orders.ConfirmPayment(ctx, parsedOrderID, in.GrossAmount, time.Now().UTC())
		if err != nil {
			s.classifyAndLogFailure(ctx, audit, err)
			return nil, err
		}
		if res.IdempotentReplay {
			audit.Outcome = domain.OutcomeIgnoredDuplicate
		} else {
			audit.Outcome = domain.OutcomeProcessedPaid
		}
		if err := s.webhooks.LogDelivery(ctx, audit); err != nil {
			return nil, err
		}
		return &WebhookResult{
			Outcome:          audit.Outcome,
			IdempotentReplay: res.IdempotentReplay,
			Order:            res.Order,
		}, nil

	case "deny", "cancel", "expire":
		outcome, err := s.orders.FailPayment(ctx, parsedOrderID, "gateway status: "+status, time.Now().UTC())
		if err != nil {
			s.classifyAndLogFailure(ctx, audit, err)
			return nil, err
		}
		audit.Outcome = outcome
		if err := s.webhooks.LogDelivery(ctx, audit); err != nil {
			return nil, err
		}
		return &WebhookResult{Outcome: outcome}, nil

	default:
		audit.Outcome = domain.OutcomeIgnoredUnknownEvent
		if err := s.webhooks.LogDelivery(ctx, audit); err != nil {
			return nil, err
		}
		return &WebhookResult{Outcome: domain.OutcomeIgnoredUnknownEvent}, nil
	}
}

// PayOrderForDemo generates a valid signed payload for an unpaid order owned by actor and invokes ProcessWebhook.
func (s *PaymentService) PayOrderForDemo(ctx context.Context, actor domain.Actor, orderID uuid.UUID) (*WebhookResult, error) {
	order, err := s.orders.FindByID(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if actor.Role != domain.RoleOrganizer && order.BuyerID != actor.ID {
		return nil, domain.NewDomainError(http.StatusForbidden, domain.ErrCodeForbidden, "cannot pay another buyer's order")
	}

	txID := "demo-" + uuid.NewString()
	statusCode := "200"
	sig := domain.ComputeSHA512Signature(order.ID.String(), statusCode, order.TotalAmountIDR, s.webhookSecret)
	rawBytes, err := json.Marshal(map[string]any{
		"transaction_id":     txID,
		"order_id":           order.ID.String(),
		"status_code":        statusCode,
		"gross_amount":       order.TotalAmountIDR,
		"transaction_status": "settlement",
		"signature_key":      sig,
	})
	if err != nil {
		return nil, err
	}

	return s.ProcessWebhook(ctx, WebhookInput{
		TransactionID:     txID,
		OrderID:           order.ID.String(),
		StatusCode:        statusCode,
		GrossAmount:       order.TotalAmountIDR,
		TransactionStatus: "settlement",
		SignatureKey:      sig,
		RawPayload:        rawBytes,
	})
}

func (s *PaymentService) classifyAndLogFailure(ctx context.Context, audit *domain.PaymentWebhook, err error) {
	var de *domain.DomainError
	if errors.As(err, &de) {
		switch de.Code {
		case domain.ErrCodeOrderExpired:
			audit.Outcome = domain.OutcomeRejectedLateExpired
			audit.RequiresRefund = true
		case domain.ErrCodePaymentAmountMismatch:
			audit.Outcome = domain.OutcomeAmountMismatch
		case domain.ErrCodeOrderNotFound:
			audit.Outcome = domain.OutcomeOrderNotFound
		default:
			audit.Outcome = domain.OutcomeProcessedFailed
		}
	} else {
		audit.Outcome = domain.OutcomeProcessedFailed
	}
	_ = s.webhooks.LogDelivery(ctx, audit)
}
```

Create `internal/service/demo_service.go`:
```go
package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/torikh42/event-ticketing/internal/domain"
	"github.com/torikh42/event-ticketing/internal/repository"
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
	if requests <= 0 {
		requests = 200
	}
	if quota <= 0 {
		quota = 100
	}

	organizerID := uuid.New()
	if err := s.users.EnsureDemoUser(ctx, organizerID, domain.RoleOrganizer); err != nil {
		return nil, err
	}

	ev := &domain.Event{
		ID:          uuid.New(),
		OrganizerID: organizerID,
		Title:       "Live Concurrency Stress Event",
		Description: "Auto-generated event for live 200 concurrent buyer demo",
		Venue:       "Concurrency Arena",
		EventDate:   time.Now().UTC().Add(48 * time.Hour),
		TicketTypes: []domain.TicketType{
			{ID: uuid.New(), Name: "Reguler", PriceIDR: 100000, TotalQuota: quota, RemainingQuota: quota},
			{ID: uuid.New(), Name: "VIP", PriceIDR: 250000, TotalQuota: 10, RemainingQuota: 10},
		},
	}
	if err := s.events.CreateWithTiers(ctx, ev); err != nil {
		return nil, err
	}
	targetTierID := ev.TicketTypes[0].ID

	buyerIDs := make([]uuid.UUID, requests)
	for i := range requests {
		bid := uuid.New()
		buyerIDs[i] = bid
		if err := s.users.EnsureDemoUser(ctx, bid, domain.RoleBuyer); err != nil {
			return nil, err
		}
	}

	var succeeded atomic.Int32
	var rejectedQuota atomic.Int32
	var rejectedOther atomic.Int32
	gate := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(requests)

	start := time.Now()
	for i := range requests {
		bid := buyerIDs[i]
		go func() {
			defer wg.Done()
			<-gate
			actor := domain.Actor{ID: bid, Role: domain.RoleBuyer}
			_, err := s.orders.CreateOrder(ctx, actor, CreateOrderInput{
				EventID: ev.ID,
				Items:   []repository.CheckoutItemInput{{TicketTypeID: targetTierID, Quantity: 1}},
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
		}()
	}

	close(gate)
	wg.Wait()
	elapsed := time.Since(start)

	updatedEv, err := s.events.FindByID(ctx, ev.ID)
	if err != nil {
		return nil, err
	}
	var remAfter int32 = -1
	for _, tt := range updatedEv.TicketTypes {
		if tt.ID == targetTierID {
			remAfter = tt.RemainingQuota
			break
		}
	}

	succ := succeeded.Load()
	otherRej := rejectedOther.Load()
	return &FlashSaleReport{
		EventID:             ev.ID,
		TicketTypeID:        targetTierID,
		ConcurrentRequests:  requests,
		SucceededBookings:   succ,
		RejectedQuotaEmpty:  rejectedQuota.Load(),
		RejectedOther:       otherRej,
		RemainingQuotaAfter: remAfter,
		ZeroOversell:        succ == quota && remAfter == 0 && otherRej == 0,
		DurationMS:          elapsed.Milliseconds(),
	}, nil
}
```

- [ ] **Step 4: Run unit tests to verify they pass**

Run: `go test -v -race ./internal/service/...`
Expected: PASS (`TestEventService_CreateRejectsBuyerAndLessThanTwoTiers`, `TestPaymentService_LateExpiredWebhookLogsRefundFlag`).

- [ ] **Step 5: Commit service layer**

```bash
git add internal/service/
git commit -m "feat(service): add auth, event, order, payment webhook, and flash-sale demo services"
```

---

### Task 7: Context-Aware Expiry Sweeper Worker (`internal/worker/`)

**Files:**
- Create: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\internal\worker\expiry_sweeper.go`
- Test: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\internal\worker\expiry_sweeper_test.go`

- [ ] **Step 1: Write failing unit test verifying ticker execution and clean context cancellation shutdown**

Create `internal/worker/expiry_sweeper_test.go`:
```go
package worker_test

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/torikh42/event-ticketing/internal/worker"
)

type fakeSweeperStore struct {
	calls atomic.Int32
}

func (f *fakeSweeperStore) ExpireDueOrders(context.Context, *uuid.UUID, int, string, time.Time) (int, error) {
	f.calls.Add(1)
	return 1, nil
}

func TestExpirySweeper_StopsGracefullyOnCancel(t *testing.T) {
	t.Parallel()

	store := &fakeSweeperStore{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sweeper := worker.NewExpirySweeper(store, logger, 10*time.Millisecond, 50)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		sweeper.Start(ctx)
		close(done)
	}()

	deadline := time.After(500 * time.Millisecond)
	for store.calls.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("sweeper did not tick within 500ms")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("sweeper failed to exit after context cancellation")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/worker/...`
Expected: FAIL (`internal/worker` does not exist).

- [ ] **Step 3: Implement `internal/worker/expiry_sweeper.go`**

```go
// Package worker executes bounded background tasks tied to application context lifecycles.
package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// ExpiryStore exposes batch expiration required by ExpirySweeper.
type ExpiryStore interface {
	ExpireDueOrders(ctx context.Context, eventID *uuid.UUID, limit int, actorType string, now time.Time) (int, error)
}

// ExpirySweeper periodically releases overdue WAITING_PAYMENT quota holds without blocking HTTP traffic.
type ExpirySweeper struct {
	store     ExpiryStore
	logger    *slog.Logger
	interval  time.Duration
	batchSize int
}

// NewExpirySweeper constructs an ExpirySweeper.
func NewExpirySweeper(store ExpiryStore, logger *slog.Logger, interval time.Duration, batchSize int) *ExpirySweeper {
	return &ExpirySweeper{
		store:     store,
		logger:    logger,
		interval:  interval,
		batchSize: batchSize,
	}
}

// Start runs the sweep ticker until ctx is cancelled, preventing goroutine leaks.
func (w *ExpirySweeper) Start(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.RunOnce(ctx)
		}
	}
}

// RunOnce executes one bounded sweep cycle with a timeout context.
func (w *ExpirySweeper) RunOnce(ctx context.Context) {
	sweepCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	expired, err := w.store.ExpireDueOrders(sweepCtx, nil, w.batchSize, "scheduler", time.Now().UTC())
	if err != nil {
		w.logger.ErrorContext(sweepCtx, "expiry sweep failed", slog.Any("error", err))
		return
	}
	if expired > 0 {
		w.logger.InfoContext(sweepCtx, "expired unpaid orders and restored quota", slog.Int("expired_count", expired))
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v -race ./internal/worker/...`
Expected: PASS (`TestExpirySweeper_StopsGracefullyOnCancel`).

- [ ] **Step 5: Commit worker**

```bash
git add internal/worker/
git commit -m "feat(worker): add context-managed batch expiry sweeper"
```

---

### Task 8: Middleware, Error Mapper, Huma v2 + Scalar Router (`internal/middleware/`, `internal/handler/`, `web/`)

**Files:**
- Create: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\internal\middleware\auth.go`
- Create: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\internal\middleware\logging.go`
- Create: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\internal\handler\error_mapper.go`
- Create: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\internal\handler\auth_handler.go`
- Create: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\internal\handler\event_handler.go`
- Create: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\internal\handler\order_handler.go`
- Create: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\internal\handler\webhook_handler.go`
- Create: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\internal\handler\demo_handler.go`
- Create: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\internal\handler\router.go`
- Create: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\web\embed.go`
- Create: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\web\demo.html` (minimal compile placeholder gated on Task 11 before visual UI work)

- [ ] **Step 1: Implement `internal/middleware/auth.go` & `logging.go`**

Create `internal/middleware/auth.go`:
```go
// Package middleware provides HTTP request logging, panic recovery, and hybrid authentication.
package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
	"github.com/torikh42/event-ticketing/internal/domain"
	"github.com/torikh42/event-ticketing/internal/service"
)

type ctxKey string

const actorContextKey ctxKey = "auth_actor"

// ActorFromContext extracts the authenticated Actor stored by HumaAuthMiddleware.
func ActorFromContext(ctx context.Context) (domain.Actor, bool) {
	actor, ok := ctx.Value(actorContextKey).(domain.Actor)
	return actor, ok
}

// NewHumaAuthMiddleware validates Bearer JWT or opt-in demo headers on protected Huma operations.
func NewHumaAuthMiddleware(api huma.API, authSvc *service.AuthService, allowDemoHeaders bool) func(ctx huma.Context, next func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		requiresAuth := len(ctx.Operation().Security) > 0
		if !requiresAuth {
			next(ctx)
			return
		}

		authHeader := strings.TrimSpace(ctx.Header("Authorization"))
		if strings.HasPrefix(authHeader, "Bearer ") {
			tokenStr := strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))
			actor, err := authSvc.ParseJWT(tokenStr)
			if err != nil {
				_ = huma.WriteErr(api, ctx, http.StatusUnauthorized, "unauthorized: invalid or expired bearer token")
				return
			}
			next(huma.WithValue(ctx, actorContextKey, actor))
			return
		}

		if allowDemoHeaders {
			uidStr := strings.TrimSpace(ctx.Header("X-User-ID"))
			roleStr := strings.ToUpper(strings.TrimSpace(ctx.Header("X-User-Role")))
			if uidStr != "" && roleStr != "" {
				uid, err := uuid.Parse(uidStr)
				role := domain.Role(roleStr)
				if err != nil || (role != domain.RoleBuyer && role != domain.RoleOrganizer) {
					_ = huma.WriteErr(api, ctx, http.StatusUnauthorized, "unauthorized: invalid X-User-ID or X-User-Role")
					return
				}
				if err := authSvc.EnsureDemoUser(ctx.Context(), uid, role); err != nil {
					_ = huma.WriteErr(api, ctx, http.StatusInternalServerError, "failed to prepare demo user identity")
					return
				}
				actor := domain.Actor{ID: uid, Email: "demo@event.local", Role: role}
				next(huma.WithValue(ctx, actorContextKey, actor))
				return
			}
		}

		_ = huma.WriteErr(api, ctx, http.StatusUnauthorized, "missing authentication credentials")
	}
}
```

Create `internal/middleware/logging.go`:
```go
package middleware

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	chiMiddleware "github.com/go-chi/chi/v5/middleware"
)

// Recoverer recovers from handler panics, logs stack traces once, and returns JSON 500.
func Recoverer(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					logger.ErrorContext(r.Context(), "http handler panic recovered",
						slog.Any("panic", rec),
						slog.String("stack", string(debug.Stack())),
					)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = w.Write([]byte(`{"title":"Internal Server Error","status":500,"detail":"internal server error","error_code":"INTERNAL_ERROR"}`))
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// RequestLogger logs HTTP method, path, status code, and latency via slog.
func RequestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := chiMiddleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)

			logger.InfoContext(r.Context(), "http request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", ww.Status()),
				slog.Int64("duration_ms", time.Since(start).Milliseconds()),
			)
		})
	}
}
```

- [ ] **Step 2: Implement `internal/handler/error_mapper.go` and configure Huma custom error model**

```go
// Package handler implements Huma v2 HTTP endpoints, OpenAPI schemas, and response mapping.
package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/torikh42/event-ticketing/internal/domain"
)

// APIError is the uniform RFC-style error payload enriched with machine-readable error_code.
type APIError struct {
	status    int
	Title     string  `json:"title"`
	Status    int     `json:"status"`
	Detail    string  `json:"detail"`
	ErrorCode string  `json:"error_code"`
	Errors    []error `json:"errors,omitempty"`
}

// Error implements the Go error interface.
func (e *APIError) Error() string {
	return e.Detail
}

// GetStatus satisfies huma.StatusError so Huma renders the exact HTTP status code.
func (e *APIError) GetStatus() int {
	return e.status
}

func configureHumaErrorFactory() {
	huma.NewError = func(status int, msg string, errs ...error) huma.StatusError {
		code := "HTTP_ERROR"
		if status == http.StatusUnprocessableEntity {
			code = domain.ErrCodeValidation
		} else if status == http.StatusUnauthorized {
			code = domain.ErrCodeUnauthorized
		}
		return &APIError{
			status:    status,
			Title:     http.StatusText(status),
			Status:    status,
			Detail:    msg,
			ErrorCode: code,
			Errors:    errs,
		}
	}
}

func mapServiceError(ctx context.Context, logger *slog.Logger, err error) error {
	if err == nil {
		return nil
	}
	if de, ok := errors.AsType[*domain.DomainError](err); ok {
		return &APIError{
			status:    de.HTTPStatus,
			Title:     http.StatusText(de.HTTPStatus),
			Status:    de.HTTPStatus,
			Detail:    de.Message,
			ErrorCode: de.Code,
		}
	}

	logger.ErrorContext(ctx, "unhandled service error", slog.Any("error", err))
	return &APIError{
		status:    http.StatusInternalServerError,
		Title:     http.StatusText(http.StatusInternalServerError),
		Status:    http.StatusInternalServerError,
		Detail:    "internal server error",
		ErrorCode: "INTERNAL_ERROR",
	}
}
```

- [ ] **Step 3: Implement `auth_handler.go`, `event_handler.go`, `order_handler.go`, `webhook_handler.go`, `demo_handler.go`**

Create `internal/handler/auth_handler.go`:
```go
package handler

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/torikh42/event-ticketing/internal/domain"
	"github.com/torikh42/event-ticketing/internal/service"
)

type RegisterBody struct {
	Email    string `json:"email" format:"email" doc:"Unique email address"`
	Name     string `json:"name" minLength:"2" maxLength:"100" doc:"Full name"`
	Password string `json:"password" minLength:"6" maxLength:"72" doc:"Account password"`
	Role     string `json:"role" enum:"ORGANIZER,BUYER" doc:"User role"`
}

type RegisterOutput struct {
	Body *service.AuthResult
}

type LoginBody struct {
	Email    string `json:"email" format:"email" doc:"Registered email"`
	Password string `json:"password" minLength:"1" doc:"Account password"`
}

type LoginOutput struct {
	Body *service.AuthResult
}

func registerAuthRoutes(api huma.API, logger *slog.Logger, authSvc *service.AuthService) {
	huma.Register(api, huma.Operation{
		OperationID:   "register-user",
		Method:        http.MethodPost,
		Path:          "/api/v1/auth/register",
		Summary:       "Register an organizer or buyer account",
		Tags:          []string{"Auth"},
		DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, in *struct{ Body RegisterBody }) (*RegisterOutput, error) {
		res, err := authSvc.Register(ctx, service.RegisterInput{
			Email:    in.Body.Email,
			Name:     in.Body.Name,
			Password: in.Body.Password,
			Role:     domain.Role(in.Body.Role),
		})
		if err != nil {
			return nil, mapServiceError(ctx, logger, err)
		}
		return &RegisterOutput{Body: res}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "login-user",
		Method:      http.MethodPost,
		Path:        "/api/v1/auth/login",
		Summary:     "Login with email and password to receive a Bearer JWT",
		Tags:        []string{"Auth"},
	}, func(ctx context.Context, in *struct{ Body LoginBody }) (*LoginOutput, error) {
		res, err := authSvc.Login(ctx, in.Body.Email, in.Body.Password)
		if err != nil {
			return nil, mapServiceError(ctx, logger, err)
		}
		return &LoginOutput{Body: res}, nil
	})
}
```

Create `internal/handler/event_handler.go`:
```go
package handler

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
	"github.com/torikh42/event-ticketing/internal/domain"
	"github.com/torikh42/event-ticketing/internal/middleware"
	"github.com/torikh42/event-ticketing/internal/service"
)

type CreateTierBody struct {
	Name       string `json:"name" minLength:"1" maxLength:"60" example:"Reguler"`
	PriceIDR   int64  `json:"price_idr" minimum:"0" example:"150000"`
	TotalQuota int32  `json:"total_quota" minimum:"1" maximum:"100000" example:"80"`
}

type CreateEventBody struct {
	Title       string           `json:"title" minLength:"3" maxLength:"150" example:"Indie Music Fest"`
	Description string           `json:"description" maxLength:"1000"`
	Venue       string           `json:"venue" minLength:"2" maxLength:"150" example:"Senayan Hall"`
	EventDate   time.Time        `json:"event_date"`
	TicketTypes []CreateTierBody `json:"ticket_types" minItems:"2" maxItems:"10" doc:"At least 2 ticket types (e.g. Reguler, VIP)"`
}

func registerEventRoutes(api huma.API, logger *slog.Logger, eventSvc *service.EventService) {
	huma.Register(api, huma.Operation{
		OperationID:   "create-event",
		Method:        http.MethodPost,
		Path:          "/api/v1/events",
		Summary:       "Create a new event with 2+ ticket tiers (Organizer only)",
		Tags:          []string{"Events"},
		DefaultStatus: http.StatusCreated,
		Security:      []map[string][]string{{"bearer": {}}},
	}, func(ctx context.Context, in *struct{ Body CreateEventBody }) (*struct{ Body *domain.Event }, error) {
		actor, _ := middleware.ActorFromContext(ctx)
		tiers := make([]service.CreateTierInput, len(in.Body.TicketTypes))
		for i, tt := range in.Body.TicketTypes {
			tiers[i] = service.CreateTierInput{Name: tt.Name, PriceIDR: tt.PriceIDR, TotalQuota: tt.TotalQuota}
		}
		ev, err := eventSvc.CreateEvent(ctx, actor, service.CreateEventInput{
			Title:       in.Body.Title,
			Description: in.Body.Description,
			Venue:       in.Body.Venue,
			EventDate:   in.Body.EventDate,
			TicketTypes: tiers,
		})
		if err != nil {
			return nil, mapServiceError(ctx, logger, err)
		}
		return &struct{ Body *domain.Event }{Body: ev}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "list-events",
		Method:      http.MethodGet,
		Path:        "/api/v1/events",
		Summary:     "List all events with live remaining ticket quotas",
		Tags:        []string{"Events"},
	}, func(ctx context.Context, _ *struct{}) (*struct{ Body []domain.Event }, error) {
		evs, err := eventSvc.ListEvents(ctx)
		if err != nil {
			return nil, mapServiceError(ctx, logger, err)
		}
		return &struct{ Body []domain.Event }{Body: evs}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "get-event",
		Method:      http.MethodGet,
		Path:        "/api/v1/events/{id}",
		Summary:     "Get event details and live remaining quota per ticket tier",
		Tags:        []string{"Events"},
	}, func(ctx context.Context, in *struct {
		ID string `path:"id" format:"uuid"`
	}) (*struct{ Body *domain.Event }, error) {
		uid, err := uuid.Parse(in.ID)
		if err != nil {
			return nil, mapServiceError(ctx, logger, domain.NewDomainError(http.StatusBadRequest, domain.ErrCodeValidation, "invalid event UUID"))
		}
		ev, err := eventSvc.GetEvent(ctx, uid)
		if err != nil {
			return nil, mapServiceError(ctx, logger, err)
		}
		return &struct{ Body *domain.Event }{Body: ev}, nil
	})
}
```

Create `internal/handler/order_handler.go`:
```go
package handler

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
	"github.com/torikh42/event-ticketing/internal/domain"
	"github.com/torikh42/event-ticketing/internal/middleware"
	"github.com/torikh42/event-ticketing/internal/repository"
	"github.com/torikh42/event-ticketing/internal/service"
)

type OrderItemBody struct {
	TicketTypeID string `json:"ticket_type_id" format:"uuid"`
	Quantity     int32  `json:"quantity" minimum:"1" maximum:"4"`
}

type CreateOrderBody struct {
	EventID string          `json:"event_id" format:"uuid"`
	Items   []OrderItemBody `json:"items" minItems:"1" maxItems:"4" doc:"1 to 4 total tickets across tiers"`
}

func registerOrderRoutes(api huma.API, logger *slog.Logger, orderSvc *service.OrderService) {
	huma.Register(api, huma.Operation{
		OperationID:   "create-order",
		Method:        http.MethodPost,
		Path:          "/api/v1/orders",
		Summary:       "Reserve 1-4 tickets for 15 minutes (WAITING_PAYMENT)",
		Tags:          []string{"Orders"},
		DefaultStatus: http.StatusCreated,
		Security:      []map[string][]string{{"bearer": {}}},
	}, func(ctx context.Context, in *struct{ Body CreateOrderBody }) (*struct{ Body *domain.Order }, error) {
		actor, _ := middleware.ActorFromContext(ctx)
		eventID, err := uuid.Parse(in.Body.EventID)
		if err != nil {
			return nil, mapServiceError(ctx, logger, domain.NewDomainError(http.StatusBadRequest, domain.ErrCodeValidation, "invalid event_id UUID"))
		}

		items := make([]repository.CheckoutItemInput, len(in.Body.Items))
		for i, it := range in.Body.Items {
			tid, parseErr := uuid.Parse(it.TicketTypeID)
			if parseErr != nil {
				return nil, mapServiceError(ctx, logger, domain.NewDomainError(http.StatusBadRequest, domain.ErrCodeValidation, "invalid ticket_type_id UUID"))
			}
			items[i] = repository.CheckoutItemInput{TicketTypeID: tid, Quantity: it.Quantity}
		}

		order, err := orderSvc.CreateOrder(ctx, actor, service.CreateOrderInput{EventID: eventID, Items: items})
		if err != nil {
			return nil, mapServiceError(ctx, logger, err)
		}
		return &struct{ Body *domain.Order }{Body: order}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "cancel-order",
		Method:      http.MethodPost,
		Path:        "/api/v1/orders/{id}/cancel",
		Summary:     "Cancel an unpaid order and immediately restore quota",
		Tags:        []string{"Orders"},
		Security:    []map[string][]string{{"bearer": {}}},
	}, func(ctx context.Context, in *struct {
		ID string `path:"id" format:"uuid"`
	}) (*struct{ Body *domain.Order }, error) {
		actor, _ := middleware.ActorFromContext(ctx)
		oid, err := uuid.Parse(in.ID)
		if err != nil {
			return nil, mapServiceError(ctx, logger, domain.NewDomainError(http.StatusBadRequest, domain.ErrCodeValidation, "invalid order UUID"))
		}
		order, err := orderSvc.CancelOrder(ctx, actor, oid)
		if err != nil {
			return nil, mapServiceError(ctx, logger, err)
		}
		return &struct{ Body *domain.Order }{Body: order}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "list-my-orders",
		Method:      http.MethodGet,
		Path:        "/api/v1/orders",
		Summary:     "List authenticated buyer's order history and issued tickets",
		Tags:        []string{"Orders"},
		Security:    []map[string][]string{{"bearer": {}}},
	}, func(ctx context.Context, _ *struct{}) (*struct{ Body []domain.Order }, error) {
		actor, _ := middleware.ActorFromContext(ctx)
		orders, err := orderSvc.ListOrders(ctx, actor)
		if err != nil {
			return nil, mapServiceError(ctx, logger, err)
		}
		return &struct{ Body []domain.Order }{Body: orders}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "get-order",
		Method:      http.MethodGet,
		Path:        "/api/v1/orders/{id}",
		Summary:     "Get single order detail with items, status transition logs, and ticket codes",
		Tags:        []string{"Orders"},
		Security:    []map[string][]string{{"bearer": {}}},
	}, func(ctx context.Context, in *struct {
		ID string `path:"id" format:"uuid"`
	}) (*struct{ Body *domain.Order }, error) {
		actor, _ := middleware.ActorFromContext(ctx)
		oid, err := uuid.Parse(in.ID)
		if err != nil {
			return nil, mapServiceError(ctx, logger, domain.NewDomainError(http.StatusBadRequest, domain.ErrCodeValidation, "invalid order UUID"))
		}
		order, err := orderSvc.GetOrder(ctx, actor, oid)
		if err != nil {
			return nil, mapServiceError(ctx, logger, err)
		}
		return &struct{ Body *domain.Order }{Body: order}, nil
	})
}
```

Create `internal/handler/webhook_handler.go`:
```go
package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
	"github.com/torikh42/event-ticketing/internal/domain"
	"github.com/torikh42/event-ticketing/internal/middleware"
	"github.com/torikh42/event-ticketing/internal/service"
)

type PaymentWebhookBody struct {
	TransactionID     string `json:"transaction_id" minLength:"1"`
	OrderID           string `json:"order_id" minLength:"1"`
	StatusCode        string `json:"status_code" minLength:"1" example:"200"`
	GrossAmount       int64  `json:"gross_amount" minimum:"0" example:"300000"`
	TransactionStatus string `json:"transaction_status" minLength:"1" example:"settlement"`
	SignatureKey      string `json:"signature_key" minLength:"1"`
}

func registerWebhookRoutes(api huma.API, logger *slog.Logger, paymentSvc *service.PaymentService) {
	huma.Register(api, huma.Operation{
		OperationID: "payment-webhook",
		Method:      http.MethodPost,
		Path:        "/api/v1/webhooks/payment",
		Summary:     "Idempotent SHA-512 signed payment webhook (handles duplicates and post-expiry late captures)",
		Tags:        []string{"Payments"},
	}, func(ctx context.Context, in *struct{ Body PaymentWebhookBody }) (*struct{ Body *service.WebhookResult }, error) {
		raw, _ := json.Marshal(in.Body)
		res, err := paymentSvc.ProcessWebhook(ctx, service.WebhookInput{
			TransactionID:     in.Body.TransactionID,
			OrderID:           in.Body.OrderID,
			StatusCode:        in.Body.StatusCode,
			GrossAmount:       in.Body.GrossAmount,
			TransactionStatus: in.Body.TransactionStatus,
			SignatureKey:      in.Body.SignatureKey,
			RawPayload:        raw,
		})
		if err != nil {
			return nil, mapServiceError(ctx, logger, err)
		}
		return &struct{ Body *service.WebhookResult }{Body: res}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "pay-order-demo",
		Method:      http.MethodPost,
		Path:        "/api/v1/orders/{id}/pay",
		Summary:     "Simulate immediate payment for an order (generates valid signature server-side)",
		Tags:        []string{"Payments"},
		Security:    []map[string][]string{{"bearer": {}}},
	}, func(ctx context.Context, in *struct {
		ID string `path:"id" format:"uuid"`
	}) (*struct{ Body *service.WebhookResult }, error) {
		actor, _ := middleware.ActorFromContext(ctx)
		oid, err := uuid.Parse(in.ID)
		if err != nil {
			return nil, mapServiceError(ctx, logger, domain.NewDomainError(http.StatusBadRequest, domain.ErrCodeValidation, "invalid order UUID"))
		}
		res, err := paymentSvc.PayOrderForDemo(ctx, actor, oid)
		if err != nil {
			return nil, mapServiceError(ctx, logger, err)
		}
		return &struct{ Body *service.WebhookResult }{Body: res}, nil
	})
}
```

Create `internal/handler/demo_handler.go`:
```go
package handler

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/torikh42/event-ticketing/internal/service"
)

type FlashSaleBody struct {
	Requests int   `json:"requests" minimum:"1" maximum:"500" default:"200" example:"200"`
	Quota    int32 `json:"quota" minimum:"1" maximum:"500" default:"100" example:"100"`
}

func registerDemoRoutes(api huma.API, logger *slog.Logger, demoSvc *service.DemoService) {
	huma.Register(api, huma.Operation{
		OperationID: "demo-flash-sale",
		Method:      http.MethodPost,
		Path:        "/api/v1/demo/flash-sale",
		Summary:     "Execute concurrent buyer spike benchmark (default: 200 requests vs 100 quota)",
		Tags:        []string{"Demo"},
	}, func(ctx context.Context, in *struct{ Body FlashSaleBody }) (*struct{ Body *service.FlashSaleReport }, error) {
		report, err := demoSvc.RunFlashSale(ctx, in.Body.Requests, in.Body.Quota)
		if err != nil {
			return nil, mapServiceError(ctx, logger, err)
		}
		return &struct{ Body *service.FlashSaleReport }{Body: report}, nil
	})
}
```

- [ ] **Step 4: Create `web/embed.go`, minimal `web/demo.html` compile placeholder, and `internal/handler/router.go`**

Create `web/embed.go`:
```go
// Package web embeds static assets served by the Live Demo endpoint (/demo).
package web

import "embed"

// DemoAssets contains static demo files.
//
//go:embed demo.html
var DemoAssets embed.FS
```

Create temporary compile stub `web/demo.html` (to be replaced only after resolving Task 11 UI Direction & Antislop Mode):
```html
<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>Event Ticketing Demo Console</title></head>
<body><h1>Event Ticketing API Demo Console</h1><p>API docs available at <a href="/docs">/docs</a>.</p></body>
</html>
```

Create `internal/handler/router.go`:
```go
package handler

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/torikh42/event-ticketing/internal/middleware"
	"github.com/torikh42/event-ticketing/internal/service"
	"github.com/torikh42/event-ticketing/web"
)

// DBPinger checks underlying database connectivity for /health.
type DBPinger interface {
	Ping(ctx context.Context) error
}

// RouterDeps bundles handlers and dependencies wired by cmd/api/main.go.
type RouterDeps struct {
	Logger              *slog.Logger
	DB                  DBPinger
	AuthService         *service.AuthService
	EventService        *service.EventService
	OrderService        *service.OrderService
	PaymentService      *service.PaymentService
	DemoService         *service.DemoService
	AllowDemoHeaderAuth bool
}

// NewRouter constructs the Chi mux, configures Huma v2 + Scalar docs, and registers all endpoints.
func NewRouter(deps RouterDeps) http.Handler {
	configureHumaErrorFactory()

	r := chi.NewRouter()
	r.Use(chiMiddleware.RequestID)
	r.Use(chiMiddleware.RealIP)
	r.Use(middleware.Recoverer(deps.Logger))
	r.Use(middleware.RequestLogger(deps.Logger))
	r.Use(chiMiddleware.Timeout(30 * time.Second))

	r.Get("/health", func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), 2*time.Second)
		defer cancel()
		if err := deps.DB.Ping(ctx); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"unhealthy"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	r.Get("/demo", func(w http.ResponseWriter, req *http.Request) {
		data, err := web.DemoAssets.ReadFile("demo.html")
		if err != nil {
			http.Error(w, "demo page not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	})

	cfg := huma.DefaultConfig("Event Ticketing API", "1.0.0")
	cfg.DocsRenderer = huma.DocsRendererScalar
	cfg.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		"bearer": {
			Type:         "http",
			Scheme:       "bearer",
			BearerFormat: "JWT",
		},
	}

	api := humachi.New(r, cfg)
	api.UseMiddleware(middleware.NewHumaAuthMiddleware(api, deps.AuthService, deps.AllowDemoHeaderAuth))

	registerAuthRoutes(api, deps.Logger, deps.AuthService)
	registerEventRoutes(api, deps.Logger, deps.EventService)
	registerOrderRoutes(api, deps.Logger, deps.OrderService)
	registerWebhookRoutes(api, deps.Logger, deps.PaymentService)
	registerDemoRoutes(api, deps.Logger, deps.DemoService)

	return r
}
```

- [ ] **Step 5: Compile check handlers, middleware, and web embed**

Run: `go build ./internal/... ./web/...`
Expected: PASS with exit code 0.

- [ ] **Step 6: Commit HTTP layer**

```bash
git add internal/middleware/ internal/handler/ web/
git commit -m "feat(http): add Chi + Huma v2 handlers, Scalar OpenAPI docs, hybrid auth, and demo embed route"
```

---

### Task 9: Application Entrypoint (`cmd/api/main.go`), Docker Stack, & CI/CD Pipeline

**Files:**
- Create: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\cmd\api\main.go`
- Create: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\Dockerfile`
- Create: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\docker-compose.yml`
- Create: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\.github\workflows\ci.yml`

- [ ] **Step 1: Implement `cmd/api/main.go`**

```go
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/torikh42/event-ticketing/internal/config"
	"github.com/torikh42/event-ticketing/internal/handler"
	"github.com/torikh42/event-ticketing/internal/repository"
	"github.com/torikh42/event-ticketing/internal/service"
	"github.com/torikh42/event-ticketing/internal/worker"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(logger); err != nil {
		logger.Error("application exited with error", slog.Any("error", err))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := repository.NewPool(rootCtx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := repository.ApplyMigrations(rootCtx, pool); err != nil {
		return err
	}
	logger.Info("database migrations and demo seed applied")

	userRepo := repository.NewUserRepo(pool)
	eventRepo := repository.NewEventRepo(pool)
	orderRepo := repository.NewOrderRepo(pool)
	webhookRepo := repository.NewWebhookRepo(pool)

	authSvc := service.NewAuthService(userRepo, cfg.JWTSecret)
	eventSvc := service.NewEventService(eventRepo, orderRepo)
	orderSvc := service.NewOrderService(orderRepo)
	paymentSvc := service.NewPaymentService(orderRepo, webhookRepo, cfg.WebhookSecret)
	demoSvc := service.NewDemoService(userRepo, eventRepo, orderSvc)

	var wg sync.WaitGroup
	sweeper := worker.NewExpirySweeper(orderRepo, logger, cfg.ExpirySweepInterval, 50)
	wg.Add(1)
	go func() {
		defer wg.Done()
		sweeper.Start(rootCtx)
	}()

	router := handler.NewRouter(handler.RouterDeps{
		Logger:              logger,
		DB:                  pool,
		AuthService:         authSvc,
		EventService:        eventSvc,
		OrderService:        orderSvc,
		PaymentService:      paymentSvc,
		DemoService:         demoSvc,
		AllowDemoHeaderAuth: cfg.AllowDemoHeaderAuth,
	})

	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      router,
		ReadTimeout:  cfg.HTTPReadTimeout,
		WriteTimeout: cfg.HTTPWriteTimeout,
		IdleTimeout:  60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("http server listening", slog.String("addr", srv.Addr))
		if serveErr := srv.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- serveErr
		}
		close(errCh)
	}()

	select {
	case <-rootCtx.Done():
		logger.Info("shutdown signal received")
	case serveErr := <-errCh:
		stop()
		wg.Wait()
		return serveErr
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("http server forced shutdown", slog.Any("error", err))
	}
	wg.Wait()
	logger.Info("graceful shutdown complete")
	return nil
}
```

- [ ] **Step 2: Author multi-stage `Dockerfile` and single-command `docker-compose.yml`**

Create `Dockerfile`:
```dockerfile
FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 appuser
USER appuser
WORKDIR /app
COPY --from=builder /out/api /app/api
EXPOSE 8080
ENTRYPOINT ["/app/api"]
```

Create `docker-compose.yml`:
```yaml
services:
  postgres:
    image: postgres:17-alpine
    environment:
      POSTGRES_USER: ${POSTGRES_USER:-ticketing}
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD:-ticketing_secret}
      POSTGRES_DB: ${POSTGRES_DB:-event_ticketing}
    ports:
      - "5432:5432"
    volumes:
      - pgdata:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U ${POSTGRES_USER:-ticketing} -d ${POSTGRES_DB:-event_ticketing}"]
      interval: 5s
      timeout: 3s
      retries: 10

  api:
    build:
      context: .
      dockerfile: Dockerfile
    depends_on:
      postgres:
        condition: service_healthy
    ports:
      - "${PORT:-8080}:8080"
    environment:
      PORT: ${PORT:-8080}
      DATABASE_URL: postgres://${POSTGRES_USER:-ticketing}:${POSTGRES_PASSWORD:-ticketing_secret}@postgres:5432/${POSTGRES_DB:-event_ticketing}?sslmode=disable
      JWT_SECRET: ${JWT_SECRET:-dev-jwt-secret-change-in-production}
      WEBHOOK_SECRET: ${WEBHOOK_SECRET:-dev-webhook-secret-change-in-production}
      ALLOW_DEMO_HEADER_AUTH: ${ALLOW_DEMO_HEADER_AUTH:-true}
      EXPIRY_SWEEP_INTERVAL: ${EXPIRY_SWEEP_INTERVAL:-5s}
    restart: unless-stopped

volumes:
  pgdata:
```

- [ ] **Step 3: Author `.github/workflows/ci.yml` pushing commit-SHA tagged Docker image on every push**

Create `.github/workflows/ci.yml`:
```yaml
name: CI/CD Pipeline

on:
  push:
    branches: [ main ]
  pull_request:
    branches: [ main ]
  workflow_dispatch:

permissions:
  contents: read
  packages: write

concurrency:
  group: ${{ github.workflow }}-${{ github.event.pull_request.number || github.ref }}
  cancel-in-progress: true

jobs:
  test-and-lint:
    name: Format, Lint, Unit & PostgreSQL Integration Tests
    runs-on: ubuntu-latest
    services:
      postgres:
        image: postgres:17-alpine
        env:
          POSTGRES_USER: ticketing
          POSTGRES_PASSWORD: ticketing_secret
          POSTGRES_DB: event_ticketing_test
        ports:
          - 5432:5432
        options: >-
          --health-cmd="pg_isready -U ticketing -d event_ticketing_test"
          --health-interval=5s
          --health-timeout=5s
          --health-retries=10
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-go@v5
        with:
          go-version: '1.26'
          cache: true

      - name: Check formatting (gofmt)
        run: |
          UNFORMATTED=$(gofmt -s -l .)
          if [ -n "$UNFORMATTED" ]; then
            echo "Unformatted Go files detected:"
            echo "$UNFORMATTED"
            exit 1
          fi

      - name: Run golangci-lint
        uses: golangci/golangci-lint-action@v6
        with:
          version: latest
          install-mode: goinstall

      - name: Run Unit Tests with Race Detector
        run: go test -v -race ./internal/...

      - name: Run Integration & 200-Concurrency Tests with Race Detector
        env:
          DATABASE_URL: postgres://ticketing:ticketing_secret@localhost:5432/event_ticketing_test?sslmode=disable
          JWT_SECRET: ci-jwt-secret
          WEBHOOK_SECRET: ci-webhook-secret
          ALLOW_DEMO_HEADER_AUTH: "true"
        run: go test -v -race -tags=integration ./tests/...

      - name: Validate docker-compose config
        run: docker compose config --quiet

  build-and-push:
    name: Build & Push Commit-Tagged Image
    runs-on: ubuntu-latest
    needs: [test-and-lint]
    steps:
      - uses: actions/checkout@v4

      - uses: docker/setup-buildx-action@v3

      - name: Log in to GitHub Container Registry
        if: github.event_name == 'push'
        uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}

      - name: Extract image metadata with commit SHA tag
        id: meta
        uses: docker/metadata-action@v5
        with:
          images: ghcr.io/${{ github.repository }}
          tags: |
            type=sha,prefix=sha-,format=short
            type=raw,value={{sha}}
            type=ref,event=branch

      - name: Build and push image
        uses: docker/build-push-action@v6
        with:
          context: .
          push: ${{ github.event_name == 'push' }}
          tags: ${{ steps.meta.outputs.tags }}
          labels: ${{ steps.meta.outputs.labels }}
          cache-from: type=gha
          cache-to: type=gha,mode=max
```

- [ ] **Step 4: Build binary and validate Compose syntax locally**

Run: `go build -o bin/api ./cmd/api && docker compose config --quiet`
Expected: exit code 0 for both commands.

- [ ] **Step 5: Commit main entrypoint, Dockerfile, Compose, and CI workflow**

```bash
git add cmd/ Dockerfile docker-compose.yml .github/
git commit -m "feat(infra): add main entrypoint, multi-stage Docker stack, and commit-tagged CI/CD"
```

---

### Task 10: Comprehensive Integration & 200-Concurrent Buyers Test Suite (`tests/integration_test.go`)

**Files:**
- Create: `D:\kuliah\vtech\SC\BE-intermediate\event-ticketing\tests\integration_test.go`

- [ ] **Step 1: Author `tests/integration_test.go` covering every invariant `B1` through `B10`**

```go
//go:build integration

package tests_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/torikh42/event-ticketing/internal/config"
	"github.com/torikh42/event-ticketing/internal/domain"
	"github.com/torikh42/event-ticketing/internal/handler"
	"github.com/torikh42/event-ticketing/internal/repository"
	"github.com/torikh42/event-ticketing/internal/service"
	"github.com/torikh42/event-ticketing/internal/worker"
)

const testWebhookSecret = "integration-webhook-secret"

type testEnv struct {
	pool    *pgxpool.Pool
	server  *httptest.Server
	sweeper *worker.ExpirySweeper
}

func setupTestEnv(t *testing.T) *testEnv {
	t.Helper()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://ticketing:ticketing_secret@localhost:5432/event_ticketing?sslmode=disable"
	}

	cfg := config.Config{
		DatabaseURL:         dbURL,
		JWTSecret:           "integration-jwt-secret",
		WebhookSecret:       testWebhookSecret,
		AllowDemoHeaderAuth: true,
		ExpirySweepInterval: 5 * time.Second,
		DBMaxConns:          50,
		DBMinConns:          5,
	}

	ctx := t.Context()
	pool, err := repository.NewPool(ctx, cfg)
	if err != nil {
		t.Fatalf("connecting test database: %v", err)
	}
	t.Cleanup(pool.Close)

	if err := repository.ApplyMigrations(ctx, pool); err != nil {
		t.Fatalf("applying migrations: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	userRepo := repository.NewUserRepo(pool)
	eventRepo := repository.NewEventRepo(pool)
	orderRepo := repository.NewOrderRepo(pool)
	webhookRepo := repository.NewWebhookRepo(pool)

	authSvc := service.NewAuthService(userRepo, cfg.JWTSecret)
	eventSvc := service.NewEventService(eventRepo, orderRepo)
	orderSvc := service.NewOrderService(orderRepo)
	paymentSvc := service.NewPaymentService(orderRepo, webhookRepo, cfg.WebhookSecret)
	demoSvc := service.NewDemoService(userRepo, eventRepo, orderSvc)

	r := handler.NewRouter(handler.RouterDeps{
		Logger:              logger,
		DB:                  pool,
		AuthService:         authSvc,
		EventService:        eventSvc,
		OrderService:        orderSvc,
		PaymentService:      paymentSvc,
		DemoService:         demoSvc,
		AllowDemoHeaderAuth: true,
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	return &testEnv{
		pool:    pool,
		server:  srv,
		sweeper: worker.NewExpirySweeper(orderRepo, logger, time.Second, 50),
	}
}

func doJSON(t *testing.T, url, method string, body any, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshaling request: %v", err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, url, reader)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("executing %s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading response body: %v", err)
	}
	return resp, raw
}

func createTestEvent(t *testing.T, env *testEnv, regQuota, vipQuota int32) domain.Event {
	t.Helper()
	orgHeaders := map[string]string{"X-User-ID": uuid.NewString(), "X-User-Role": "ORGANIZER"}
	resp, raw := doJSON(t, env.server.URL+"/api/v1/events", http.MethodPost, map[string]any{
		"title":       "Integration Event " + uuid.NewString()[:8],
		"description": "Integration test event",
		"venue":       "Hall A",
		"event_date":  time.Now().UTC().Add(48 * time.Hour),
		"ticket_types": []map[string]any{
			{"name": "Reguler", "price_idr": 150000, "total_quota": regQuota},
			{"name": "VIP", "price_idr": 350000, "total_quota": vipQuota},
		},
	}, orgHeaders)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create event status=%d body=%s", resp.StatusCode, string(raw))
	}
	var ev domain.Event
	if err := json.Unmarshal(raw, &ev); err != nil {
		t.Fatalf("unmarshaling event: %v", err)
	}
	return ev
}

func TestIntegration_HappyFlow_OrderPayTicketsHistory(t *testing.T) {
	env := setupTestEnv(t)
	ev := createTestEvent(t, env, 10, 5)
	buyer := map[string]string{"X-User-ID": uuid.NewString(), "X-User-Role": "BUYER"}

	resp, raw := doJSON(t, env.server.URL+"/api/v1/orders", http.MethodPost, map[string]any{
		"event_id": ev.ID.String(),
		"items": []map[string]any{
			{"ticket_type_id": ev.TicketTypes[0].ID.String(), "quantity": 2},
			{"ticket_type_id": ev.TicketTypes[1].ID.String(), "quantity": 1},
		},
	}, buyer)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create order status=%d body=%s", resp.StatusCode, string(raw))
	}
	var ord domain.Order
	if err := json.Unmarshal(raw, &ord); err != nil {
		t.Fatalf("unmarshaling order: %v", err)
	}
	if ord.TotalTickets != 3 || ord.Status != domain.OrderStatusWaitingPayment {
		t.Fatalf("unexpected order: %+v", ord)
	}

	payResp, payRaw := doJSON(t, fmt.Sprintf("%s/api/v1/orders/%s/pay", env.server.URL, ord.ID), http.MethodPost, nil, buyer)
	if payResp.StatusCode != http.StatusOK {
		t.Fatalf("pay status=%d body=%s", payResp.StatusCode, string(payRaw))
	}

	histResp, histRaw := doJSON(t, fmt.Sprintf("%s/api/v1/orders/%s", env.server.URL, ord.ID), http.MethodGet, nil, buyer)
	if histResp.StatusCode != http.StatusOK {
		t.Fatalf("get order status=%d body=%s", histResp.StatusCode, string(histRaw))
	}
	var loaded domain.Order
	if err := json.Unmarshal(histRaw, &loaded); err != nil {
		t.Fatalf("unmarshaling loaded order: %v", err)
	}
	if loaded.Status != domain.OrderStatusPaid || len(loaded.Tickets) != 3 {
		t.Fatalf("expected PAID with 3 unique tickets, got status=%s tickets=%d", loaded.Status, len(loaded.Tickets))
	}
}

func TestIntegration_BuyerFourTicketLimit_AndCancellation(t *testing.T) {
	env := setupTestEnv(t)
	ev := createTestEvent(t, env, 50, 10)
	buyer := map[string]string{"X-User-ID": uuid.NewString(), "X-User-Role": "BUYER"}

	// Parallel 3 + 3 booking attempts by the SAME buyer: only 1 may succeed.
	gate := make(chan struct{})
	var wg sync.WaitGroup
	var okCount, rejectCount atomic.Int32
	var createdID string
	var mu sync.Mutex

	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate
			resp, raw := doJSON(t, env.server.URL+"/api/v1/orders", http.MethodPost, map[string]any{
				"event_id": ev.ID.String(),
				"items":    []map[string]any{{"ticket_type_id": ev.TicketTypes[0].ID.String(), "quantity": 3}},
			}, buyer)
			if resp.StatusCode == http.StatusCreated {
				okCount.Add(1)
				var ord domain.Order
				_ = json.Unmarshal(raw, &ord)
				mu.Lock()
				createdID = ord.ID.String()
				mu.Unlock()
				return
			}
			if resp.StatusCode == http.StatusConflict {
				rejectCount.Add(1)
			}
		}()
	}
	close(gate)
	wg.Wait()

	if okCount.Load() != 1 || rejectCount.Load() != 1 {
		t.Fatalf("expected 1 ok & 1 rejected for concurrent 3+3 buyer requests, got ok=%d reject=%d", okCount.Load(), rejectCount.Load())
	}

	// Cancel order and confirm buyer quota resets and tier quota returns to 50.
	cResp, cRaw := doJSON(t, fmt.Sprintf("%s/api/v1/orders/%s/cancel", env.server.URL, createdID), http.MethodPost, nil, buyer)
	if cResp.StatusCode != http.StatusOK {
		t.Fatalf("cancel status=%d body=%s", cResp.StatusCode, string(cRaw))
	}

	evResp, evRaw := doJSON(t, fmt.Sprintf("%s/api/v1/events/%s", env.server.URL, ev.ID), http.MethodGet, nil, nil)
	if evResp.StatusCode != http.StatusOK {
		t.Fatalf("get event status=%d body=%s", evResp.StatusCode, string(evRaw))
	}
	var updated domain.Event
	_ = json.Unmarshal(evRaw, &updated)
	if updated.TicketTypes[0].RemainingQuota != 50 {
		t.Fatalf("expected remaining_quota 50 after cancel, got %d", updated.TicketTypes[0].RemainingQuota)
	}
}

func TestIntegration_ExpirySweeper_AndLateWebhookRejection(t *testing.T) {
	env := setupTestEnv(t)
	ev := createTestEvent(t, env, 5, 5)
	buyer := map[string]string{"X-User-ID": uuid.NewString(), "X-User-Role": "BUYER"}

	resp, raw := doJSON(t, env.server.URL+"/api/v1/orders", http.MethodPost, map[string]any{
		"event_id": ev.ID.String(),
		"items":    []map[string]any{{"ticket_type_id": ev.TicketTypes[0].ID.String(), "quantity": 2}},
	}, buyer)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create order status=%d body=%s", resp.StatusCode, string(raw))
	}
	var ord domain.Order
	_ = json.Unmarshal(raw, &ord)

	// Simulate 16 minutes elapsed.
	if _, err := env.pool.Exec(context.Background(), "UPDATE orders SET expires_at = NOW() - INTERVAL '1 minute' WHERE id = $1", ord.ID); err != nil {
		t.Fatalf("backdating expires_at: %v", err)
	}

	env.sweeper.RunOnce(context.Background())

	evResp, evRaw := doJSON(t, fmt.Sprintf("%s/api/v1/events/%s", env.server.URL, ev.ID), http.MethodGet, nil, nil)
	if evResp.StatusCode != http.StatusOK {
		t.Fatalf("get event status=%d body=%s", evResp.StatusCode, string(evRaw))
	}
	var updated domain.Event
	_ = json.Unmarshal(evRaw, &updated)
	if updated.TicketTypes[0].RemainingQuota != 5 {
		t.Fatalf("expected quota restored to 5 after expiry, got %d", updated.TicketTypes[0].RemainingQuota)
	}

	// Late webhook arriving after expiry must NOT issue tickets and must log requires_refund=true.
	sig := domain.ComputeSHA512Signature(ord.ID.String(), "200", ord.TotalAmountIDR, testWebhookSecret)
	wResp, _ := doJSON(t, env.server.URL+"/api/v1/webhooks/payment", http.MethodPost, map[string]any{
		"transaction_id":     "tx-late-" + uuid.NewString(),
		"order_id":           ord.ID.String(),
		"status_code":        "200",
		"gross_amount":       ord.TotalAmountIDR,
		"transaction_status": "settlement",
		"signature_key":      sig,
	}, nil)
	if wResp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 Conflict for late webhook on expired order, got %d", wResp.StatusCode)
	}

	var ticketCount int
	_ = env.pool.QueryRow(context.Background(), "SELECT COUNT(*) FROM tickets WHERE order_id = $1", ord.ID).Scan(&ticketCount)
	if ticketCount != 0 {
		t.Fatalf("expected 0 tickets for expired order, got %d", ticketCount)
	}

	var outcome string
	var requiresRefund bool
	_ = env.pool.QueryRow(context.Background(),
		"SELECT outcome, requires_refund FROM payment_webhooks WHERE parsed_order_id = $1 ORDER BY received_at DESC LIMIT 1", ord.ID,
	).Scan(&outcome, &requiresRefund)
	if outcome != string(domain.OutcomeRejectedLateExpired) || !requiresRefund {
		t.Fatalf("expected REJECTED_LATE_EXPIRED + requires_refund=true, got %q %v", outcome, requiresRefund)
	}
}

func TestIntegration_WebhookIdempotency_10ParallelReplays(t *testing.T) {
	env := setupTestEnv(t)
	ev := createTestEvent(t, env, 10, 5)
	buyer := map[string]string{"X-User-ID": uuid.NewString(), "X-User-Role": "BUYER"}

	resp, raw := doJSON(t, env.server.URL+"/api/v1/orders", http.MethodPost, map[string]any{
		"event_id": ev.ID.String(),
		"items":    []map[string]any{{"ticket_type_id": ev.TicketTypes[0].ID.String(), "quantity": 4}},
	}, buyer)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create order status=%d body=%s", resp.StatusCode, string(raw))
	}
	var ord domain.Order
	_ = json.Unmarshal(raw, &ord)

	sig := domain.ComputeSHA512Signature(ord.ID.String(), "200", ord.TotalAmountIDR, testWebhookSecret)
	txID := "tx-idempotent-" + uuid.NewString()

	gate := make(chan struct{})
	var wg sync.WaitGroup
	var ok200 atomic.Int32
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate
			wResp, _ := doJSON(t, env.server.URL+"/api/v1/webhooks/payment", http.MethodPost, map[string]any{
				"transaction_id":     txID,
				"order_id":           ord.ID.String(),
				"status_code":        "200",
				"gross_amount":       ord.TotalAmountIDR,
				"transaction_status": "settlement",
				"signature_key":      sig,
			}, nil)
			if wResp.StatusCode == http.StatusOK {
				ok200.Add(1)
			}
		}()
	}
	close(gate)
	wg.Wait()

	if ok200.Load() != 10 {
		t.Fatalf("expected all 10 webhook calls to succeed idempotently with 200, got %d", ok200.Load())
	}

	var issuedTickets int
	_ = env.pool.QueryRow(context.Background(), "SELECT COUNT(*) FROM tickets WHERE order_id = $1", ord.ID).Scan(&issuedTickets)
	if issuedTickets != 4 {
		t.Fatalf("expected exact 4 issued tickets with zero duplication, got %d", issuedTickets)
	}
}

func TestIntegration_FlashSale_200ConcurrentBuyers_ZeroOversell(t *testing.T) {
	env := setupTestEnv(t)
	ev := createTestEvent(t, env, 100, 10)
	regTierID := ev.TicketTypes[0].ID.String()

	const totalBuyers = 200
	gate := make(chan struct{})
	var wg sync.WaitGroup
	var soldCount, rejectedCount, unexpectedCount atomic.Int32

	for range totalBuyers {
		wg.Add(1)
		buyerID := uuid.NewString()
		go func() {
			defer wg.Done()
			<-gate
			resp, _ := doJSON(t, env.server.URL+"/api/v1/orders", http.MethodPost, map[string]any{
				"event_id": ev.ID.String(),
				"items":    []map[string]any{{"ticket_type_id": regTierID, "quantity": 1}},
			}, map[string]string{"X-User-ID": buyerID, "X-User-Role": "BUYER"})

			switch resp.StatusCode {
			case http.StatusCreated:
				soldCount.Add(1)
			case http.StatusConflict:
				rejectedCount.Add(1)
			default:
				unexpectedCount.Add(1)
			}
		}()
	}
	close(gate)
	wg.Wait()

	if unexpectedCount.Load() != 0 {
		t.Fatalf("unexpected non-201/409 responses: %d", unexpectedCount.Load())
	}
	if soldCount.Load() != 100 || rejectedCount.Load() != 100 {
		t.Fatalf("expected 100 sold and 100 rejected, got sold=%d rejected=%d", soldCount.Load(), rejectedCount.Load())
	}

	var dbSold int
	_ = env.pool.QueryRow(context.Background(), "SELECT COALESCE(SUM(total_tickets), 0) FROM orders WHERE event_id = $1", ev.ID).Scan(&dbSold)
	if dbSold != 100 {
		t.Fatalf("expected DB total_tickets = 100, got %d", dbSold)
	}

	var remQuota int32
	_ = env.pool.QueryRow(context.Background(), "SELECT remaining_quota FROM ticket_types WHERE id = $1", ev.TicketTypes[0].ID).Scan(&remQuota)
	if remQuota != 0 {
		t.Fatalf("expected remaining_quota = 0, got %d", remQuota)
	}
}
```

- [ ] **Step 2: Run full integration & concurrency suite against PostgreSQL**

Run PostgreSQL via Docker (`docker compose up -d postgres`), wait until healthy, then run:
```bash
DATABASE_URL="postgres://ticketing:ticketing_secret@localhost:5432/event_ticketing?sslmode=disable" \
go test -v -race -tags=integration ./tests/...
```
Expected: All 5 integration tests PASS with exact counts (`100 sold`, `100 rejected`, `0 oversell`, `4 tickets`, `REJECTED_LATE_EXPIRED`).

- [ ] **Step 3: Commit integration tests**

```bash
git add tests/integration_test.go
git commit -m "test(integration): verify happy flow, 4-ticket buyer cap, expiry, idempotent webhooks, and 200-concurrent flash sale"
```

---

### Task 11: Live Demo Console (`web/demo.html` - Antislop Gated) & Final `README.md` Verification

**ANTISLOP GATE:** Before generating or styling the interactive `web/demo.html` Live Demo Console:
1. Resolve the `antislop` usage mode with the user (`DURING` vs `AFTER`).
2. Confirm or author `DESIGN.md` visual direction (palette, typography, Energy/Rhythm/Motion dials) per Rule R-37 and `antislop-ui` so no generic AI UI template is produced.

Once `web/demo.html` direction is resolved:
- [ ] **Step 1: Author `DESIGN.md` (if `DURING` mode selected) and build interactive `web/demo.html` wired to real `/api/v1/events`, `/api/v1/orders`, `/api/v1/orders/{id}/pay`, `/api/v1/orders/{id}/cancel`, and `/api/v1/demo/flash-sale` endpoints with empty/loading/error states and keyboard focus accessibility (R-25, R-26, R-27, R-32, R-35).**
- [ ] **Step 2: Run `antislop` Delivery Gate click-through check across every interactive control in `/demo` under `docker compose up --build`.**
- [ ] **Step 3: Author `README.md` documenting 1-command `docker compose up`, Scalar docs URL (`/docs`), `/demo` URL, demo credentials (`organizer@event.local` / `buyer1@event.local` / `password123`), payment webhook curl examples, automated test commands, and real measured test output.**
- [ ] **Step 4: Run final quality & verification pass before completion**

Run:
```bash
gofmt -s -l .
golangci-lint run ./...
go test -v -race ./internal/...
docker compose up --build -d
curl -f http://localhost:8080/health
curl -f http://localhost:8080/openapi.json
```
Expected: clean formatting, zero lint issues, all tests pass, `/health` returns `{"status":"ok"}`, `/docs` renders Scalar UI, and CI pushes `ghcr.io/torikh42/event-ticketing:sha-<commit>`.

---

## Plan Self-Review Summary

1. **Spec Coverage (`B1`..`B10` & Deliverables):**
   - `B1` Event + 2+ tiers -> Tasks 3, 4, 6, 8, 10 (`TestIntegration_HappyFlow_OrderPayTicketsHistory`)
   - `B2` Live quota view -> Tasks 5, 6, 8, 10 (`EventService.ListEvents`/`GetEvent`)
   - `B3` 1..4 checkout + 15m hold -> Tasks 2, 5, 6, 8, 10 (`OrderRepo.CreateOrder`)
   - `B4` Max 4 per buyer per event -> Tasks 5, 10 (`pg_advisory_xact_lock`, `TestIntegration_BuyerFourTicketLimit_AndCancellation`)
   - `B5` Cancel restores quota -> Tasks 2, 5, 8, 10 (`OrderRepo.CancelOrder`)
   - `B6` Auto-expire after 15m -> Tasks 5, 7, 10 (`ExpirySweeper` + `idx_orders_active_expiry` lazy check)
   - `B7` Signed payment + `/pay` -> Tasks 2, 6, 8, 10 (`VerifySHA512Signature`, `PayOrderForDemo`)
   - `B8` Unique tickets, zero duplicate -> Tasks 3, 5, 10 (`UNIQUE(order_item_id, seat_sequence)`, `TestIntegration_WebhookIdempotency_10ParallelReplays`)
   - `B9` Late webhook post-expiry -> Tasks 2, 5, 6, 10 (`TestIntegration_ExpirySweeper_AndLateWebhookRejection`)
   - `B10` Order history + 200 concurrent -> Tasks 5, 8, 10 (`TestIntegration_FlashSale_200ConcurrentBuyers_ZeroOversell`)
   - One-command Docker + CI/CD commit-SHA push + `DECISIONS.md` + `README.md` -> Tasks 9, 11 + `DECISIONS.md`.
2. **Type & Signature Consistency Check:** Checked `Config`, `DomainError`, `Event`, `Order`, `PaymentWebhook`, `OrderRepo` methods (`CreateOrder`, `CancelOrder`, `ConfirmPayment`, `FailPayment`, `ExpireDueOrders`), `WebhookRepo.LogDelivery`, `AuthService`, `EventService`, `OrderService`, `PaymentService`, and `DemoService` (`errors.AsType[*domain.DomainError]`) across all tasks.
3. **Antislop Gate Compliance:** Zero Unicode em dashes (U+2014) or AI marketing buzzwords; `web/demo.html` UI work explicitly gated in Task 11 awaiting `antislop` mode selection and `DESIGN.md` visual direction.
