# Treean - Flash Event Ticketing API & Web Console

High-concurrency ticketing platform featuring a Go (`go1.26.2`) REST API (`pgx/v5`, `go-chi/chi/v5`, `Huma v2` OpenAPI 3.1 + Scalar UI) and a Signal Yellow Web-Brutalist React + Vite + TanStack frontend (`frontend/`).

Repository: `https://github.com/Torikh42/treean`

## Scope Note

This monorepo pairs the Go backend REST API (`go1.26.2`) with a React + Vite + TanStack frontend (`frontend/`, styled per [`DESIGN.md`](DESIGN.md) in Signal Yellow Web-Brutalism). In Docker Compose, Nginx serves the frontend at `http://localhost:5173` and proxies `/api/*`, `/docs`, `/openapi.json`, `/health`, and `/schemas/*` to the Go API (`:8080`). Interactive Scalar docs live at `/docs` and `/openapi.json`, while `/demo` on the API serves a minimal landing stub linking to the docs and frontend.

## Key Capabilities & Invariants

- **Atomic Multi-Tier Reservation (`1..4` tickets per order):** Orders reserve quota across one or more ticket tiers of an event for `15 minutes` (`WAITING_PAYMENT`).
- **Zero-Oversell Concurrency Control:** Multi-item checkout locks `ticket_types` rows in ascending UUID order (`ORDER BY id ASC FOR UPDATE`) paired with PostgreSQL `CHECK (remaining_quota >= 0 AND remaining_quota <= total_quota)` constraints.
- **Per-Buyer Ticket Cap (`max 4` per event):** Concurrent checkout requests from the same buyer on the same event are serialized via `pg_advisory_xact_lock(hashtext(event_id), hashtext(buyer_id))` across `WAITING_PAYMENT` and `PAID` orders. `CANCELLED` and `EXPIRED` orders release their allowance.
- **Hybrid 15-Minute Auto-Expiry:** A background ticker (`worker.ExpirySweeper`, `FOR UPDATE SKIP LOCKED`) and partial-index lazy checks on event/order reads and mutations transition overdue orders to `EXPIRED` and restore tier quota once.
- **Fail-Closed & Idempotent Webhooks (Quota Safety First):** Payment callbacks verify Midtrans-style SHA-512 signatures in constant time, persist every attempt to `payment_webhooks` outside rollback boundaries, return `200 OK` (`idempotent_replay: true`) on duplicate paid callbacks without issuing extra tickets, and reject late captures after expiry with `409 ORDER_EXPIRED` (`REJECTED_LATE_EXPIRED`, `requires_refund = true`).

## Project Structure (Clean Layered Architecture)

```text
.
├── cmd/api/                 # Minimal composition root, migrations, worker lifecycle, graceful shutdown
├── internal/
│   ├── config/              # Env config loader, validation, and slog secret redaction
│   ├── domain/              # Pure entities, state machine (CanPay/CanCancel), SHA-512 & error codes
│   ├── handler/             # Huma v2 + Chi routes, error mapping, /health, /docs, /openapi.json, /demo
│   ├── middleware/          # slog request logging, panic Recoverer, 1 MiB body limit, JWT + demo header auth
│   ├── repository/          # pgxpool PostgreSQL repos, locking SQL, advisory-locked SQL migrator
│   ├── service/             # Business use cases and consumer-defined repository interfaces
│   └── worker/              # Background ExpirySweeper ticker (FOR UPDATE SKIP LOCKED)
├── frontend/                # React 19 + Vite + TanStack Router/Query/Form + @hey-api/openapi-ts (Nginx container)
├── migrations/              # Embedded SQL schema (001_init_schema.sql) and demo seed (002_seed_demo.sql)
├── tests/                   # PostgreSQL integration & 200-concurrent-buyer test suite (-tags=integration)
├── web/                     # Embedded minimal /demo API landing stub
├── DECISIONS.md             # Architecture Decision Records (ADR-001 through ADR-008)
├── DESIGN.md                # Signal Yellow Web-Brutalism design tokens & antislop rules
├── docker-compose.yml       # Single-command PostgreSQL 17 + Go API + Nginx Frontend runtime
├── Dockerfile               # Multi-stage Alpine build (Go API)
└── Makefile                 # Local dev, test, lint, and docker shortcuts
```

## Quick Start (Single-Command Docker Compose)

Run PostgreSQL 17 and the API server together:

```bash
docker compose up --build
```

Or using `Makefile`:

```bash
make up
```

On startup, the API connects to PostgreSQL, acquires an advisory lock, applies embedded migrations (`001_init_schema.sql`), and seeds deterministic demo accounts and `Tech Flash Concert 2026` (`002_seed_demo.sql`).

Once running:

- Web Console (React + Nginx / Vite dev): `http://localhost:5173`
- Scalar API Docs: `http://localhost:8080/docs`
- OpenAPI 3.1 JSON: `http://localhost:8080/openapi.json`
- Health Check: `http://localhost:8080/health` (`200 {"status":"ok"}` when DB is reachable, `503 {"status":"unhealthy"}` otherwise)
- Backend Landing Stub: `http://localhost:8080/demo`

To stop containers and wipe the PostgreSQL volume:

```bash
make down
```

## Configuration (`.env.example`)

`cmd/api` loads configuration from environment variables (see `.env.example` and `internal/config/config.go`):

| Variable | Required | Default (`config.Load`) | `.env.example` / Compose Value | Description |
| :--- | :--- | :--- | :--- | :--- |
| `PORT` | No | `8080` | `8080` | HTTP listen port (in Compose, overrides host port mapping; container binds `8080`) |
| `DATABASE_URL` | Yes | - | `postgres://ticketing:ticketing_secret@localhost:5432/event_ticketing?sslmode=disable` | PostgreSQL DSN (Compose points `api` to `postgres:5432`) |
| `JWT_SECRET` | Yes | - | `dev-jwt-secret-change-in-production` | HS256 signing secret for Bearer JWT tokens |
| `WEBHOOK_SECRET` | Yes | - | `dev-webhook-secret-change-in-production` | Shared secret for SHA-512 webhook verification (blank secret fails closed) |
| `ALLOW_DEMO_HEADER_AUTH` | No | `false` | `false` | Enables opt-in `X-User-ID` / `X-User-Role` demo headers when set `true`; web app uses Bearer JWT and `/flash-sale` works independently |
| `EXPIRY_SWEEP_INTERVAL` | No | `5s` | `5s` | Background `ExpirySweeper` ticker interval |
| `HTTP_READ_TIMEOUT` | No | `15s` | `15s` | HTTP server read timeout |
| `HTTP_WRITE_TIMEOUT` | No | `30s` | `30s` | HTTP server write timeout |
| `DB_MAX_CONNS` | No | `50` | `50` | `pgxpool` maximum connections |
| `DB_MIN_CONNS` | No | `5` | `5` | `pgxpool` minimum warm connections |
| `CORS_ALLOWED_ORIGINS` | No | `http://localhost:5173,...` | `http://localhost:5173,http://127.0.0.1:5173` | Comma-separated allowed browser origins (`*` allowed explicitly) |
| `FRONTEND_PORT` | No | - | `5173` | Compose host port mapping for the Nginx frontend container |

`docker-compose.yml` also accepts optional `POSTGRES_USER` (`ticketing`), `POSTGRES_PASSWORD` (`ticketing_secret`), and `POSTGRES_DB` (`event_ticketing`).

## Seeded Demo Accounts & Event

Applied automatically by `migrations/002_seed_demo.sql`:

| Account | Email | Password | Role | Deterministic UUID |
| :--- | :--- | :--- | :--- | :--- |
| Demo Organizer | `organizer@event.local` | `password123` | `ORGANIZER` | `11111111-1111-1111-1111-111111111111` |
| Demo Buyer 1 | `buyer1@event.local` | `password123` | `BUYER` | `22222222-2222-2222-2222-222222222221` |
| Demo Buyer 2 | `buyer2@event.local` | `password123` | `BUYER` | `22222222-2222-2222-2222-222222222222` |

Seeded event:

- **Title:** `Tech Flash Concert 2026` (`aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaa1`)
- **Venue:** `Jakarta Convention Hall` (`2026-12-20T19:00:00Z`)
- **Tiers:**
  - `Reguler` (`bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbb1`): `150000` IDR, quota `80`
  - `VIP` (`bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbb2`): `350000` IDR, quota `20`

## Authentication Modes

Protected routes support two authentication mechanisms (`internal/middleware/auth.go`):

1. **Bearer JWT (standard):** Call `POST /api/v1/auth/register` or `POST /api/v1/auth/login` and pass `Authorization: Bearer <access_token>`. Session reloads validate the persisted JWT via `GET /api/v1/auth/me`.
2. **Local Demo Headers (opt-in):** When `ALLOW_DEMO_HEADER_AUTH=true`, pass both `X-User-ID: <uuid>` and `X-User-Role: ORGANIZER|BUYER`. Missing users are provisioned automatically via `EnsureDemoUser` so concurrent load tests can simulate hundreds of distinct buyers without pre-registering JWTs.

## Web Console Routes & RBAC Guards (`frontend/`)

| Route | Access / Guard | Description |
| :--- | :--- | :--- |
| `/` | Public | Live Event Box Office catalog with title/venue search and live tier quotas |
| `/events/$eventId` | Public view / `BUYER` checkout | Event & tier quota breakdown; guests see `Sign In as Buyer to Reserve`, `ORGANIZER` accounts see a Buyer-only checkout notice |
| `/flash-sale` | Public | 200-concurrent Flash Sale benchmark (`POST /api/v1/demo/flash-sale`) & SHA-512 Webhook Lab |
| `/login` | Guest-only (`requireGuest`) | Email/password JWT sign-in with sanitized `redirect` support |
| `/register` | Guest-only (`requireGuest`) | Create a `BUYER` or `ORGANIZER` account (`POST /api/v1/auth/register`) |
| `/forbidden` | Public | Role mismatch notice (`403`) with contextual navigation |
| `/orders`, `/orders/$orderId` | `BUYER` (`beforeLoad: requireRole("BUYER")`) | Buyer order history, 15-minute hold countdown, payment/cancel actions, and issued ticket codes |
| `/organizer` | `ORGANIZER` (`beforeLoad: requireRole("ORGANIZER")`) | Organizer Event CRUD Console (owned events list, create, edit tiers/quotas, inline-confirmed delete) |

## API Endpoints

| Method | Path | Auth | Description |
| :--- | :--- | :--- | :--- |
| `GET` | `/health` | Public | Pings PostgreSQL (`200` healthy / `503` unhealthy) |
| `GET` | `/docs` | Public | Scalar interactive OpenAPI UI |
| `GET` | `/openapi.json` | Public | Generated OpenAPI 3.1 JSON specification |
| `GET` | `/demo` | Public | Minimal HTML API landing stub |
| `POST` | `/api/v1/auth/register` | Public | Register `ORGANIZER` or `BUYER` account and return JWT (`201`) |
| `POST` | `/api/v1/auth/login` | Public | Authenticate email/password and return JWT (`200`) |
| `GET` | `/api/v1/auth/me` | Authenticated | Return authenticated caller profile (`200`, or `401` if invalid/deleted) |
| `POST` | `/api/v1/events` | `ORGANIZER` | Create event with 2 to 10 ticket tiers atomically (`201`) |
| `GET` | `/api/v1/events` | Public | List events with live remaining quota after lazy expiry check (`200`) |
| `GET` | `/api/v1/organizer/events` | `ORGANIZER` | List events owned by the authenticated organizer (`200`) |
| `GET` | `/api/v1/events/{id}` | Public | Get event details and live tier quotas (`200`) |
| `PUT` | `/api/v1/events/{id}` | Owner `ORGANIZER` | Update event metadata and upsert/validate ticket tiers atomically (`200`, `403`, `409`) |
| `DELETE` | `/api/v1/events/{id}` | Owner `ORGANIZER` | Delete an unused event with no orders (`204`, `403`, `409 EVENT_HAS_ORDERS`) |
| `POST` | `/api/v1/orders` | Authenticated | Reserve `1..4` tickets for 15 minutes in `WAITING_PAYMENT` (`201`) |
| `GET` | `/api/v1/orders` | Authenticated | List caller's orders with items and issued tickets (`200`) |
| `GET` | `/api/v1/orders/{id}` | Buyer / Event Organizer | Get order detail with items, status logs, and issued tickets (`200`) |
| `POST` | `/api/v1/orders/{id}/cancel` | Order Buyer | Cancel `WAITING_PAYMENT` order and restore quota immediately (`200`) |
| `POST` | `/api/v1/orders/{id}/pay` | Buyer / Event Organizer | Convenience demo endpoint that signs and submits a settlement webhook (`200`) |
| `POST` | `/api/v1/webhooks/payment` | Signed Webhook | Process Midtrans-style SHA-512 payment callback (`200`, `400`, `401`, `404`, `409`) |
| `POST` | `/api/v1/demo/flash-sale` | Public | Run in-process concurrent booking benchmark (default `requests=200`, `quota=100`, max `500`) |

## Payment Webhook Verification & Behavior

`POST /api/v1/webhooks/payment` computes the expected signature using:

```text
signature_key = hex(sha512(order_id + status_code + gross_amount + WEBHOOK_SECRET))
```

Where `gross_amount` is formatted as a base-10 integer string (for example `300000`), and `signature_key` is compared in constant time (`hmac.Equal`, lowercase hex).

### Webhook Outcomes

- **Invalid or missing signature / blank `WEBHOOK_SECRET`:** Rejects with `401 UNAUTHORIZED_WEBHOOK` and logs `outcome = SIGNATURE_INVALID` to `payment_webhooks`.
- **Negative or mismatched `gross_amount`:** Rejects with `400 PAYMENT_AMOUNT_MISMATCH` and logs `outcome = AMOUNT_MISMATCH`.
- **Valid `settlement` or `capture` before `expires_at`:** Transitions `WAITING_PAYMENT` -> `PAID`, issues unique `ticket_code` rows once (`UNIQUE (order_item_id, seat_sequence)`), logs `PROCESSED_PAID`, and returns `200 OK`.
- **Duplicate `settlement` / `capture` replay on `PAID` order:** Returns `200 OK` with `idempotent_replay: true`, returns existing tickets without inserting duplicates, and logs `IGNORED_DUPLICATE`.
- **Late `settlement` / `capture` after expiry (or on `EXPIRED` / `CANCELLED` order):** Never issues tickets (Quota Safety First); returns `409 ORDER_EXPIRED` (or `409 INVALID_ORDER_STATE` if previously cancelled) and persists `outcome = REJECTED_LATE_EXPIRED` with `requires_refund = true`.
- **Gateway failure status (`deny`, `cancel`, `expire`):** Returns `200 OK` (`outcome = PROCESSED_FAILED`), restores reserved tier quota, and transitions `WAITING_PAYMENT` orders to `CANCELLED` (or `EXPIRED` when `expire` or past `expires_at`).

### Curl Example (Bash)

```bash
ORDER_ID="<order-uuid>"
STATUS_CODE="200"
GROSS_AMOUNT="300000"
WEBHOOK_SECRET="dev-webhook-secret-change-in-production"

SIG=$(printf '%s' "${ORDER_ID}${STATUS_CODE}${GROSS_AMOUNT}${WEBHOOK_SECRET}" | sha512sum | awk '{print $1}')

curl -i -X POST http://localhost:8080/api/v1/webhooks/payment \
  -H "Content-Type: application/json" \
  -d "{
    \"transaction_id\": \"tx-demo-001\",
    \"order_id\": \"${ORDER_ID}\",
    \"status_code\": \"${STATUS_CODE}\",
    \"gross_amount\": ${GROSS_AMOUNT},
    \"transaction_status\": \"settlement\",
    \"signature_key\": \"${SIG}\"
  }"
```

### PowerShell Example

```powershell
$orderId = "<order-uuid>"
$statusCode = "200"
$grossAmount = 300000
$webhookSecret = "dev-webhook-secret-change-in-production"

$raw = "$orderId$statusCode$grossAmount$webhookSecret"
$sha = [System.Security.Cryptography.SHA512]::Create()
$hashBytes = $sha.ComputeHash([System.Text.Encoding]::UTF8.GetBytes($raw))
$sig = ([System.BitConverter]::ToString($hashBytes)).Replace("-", "").ToLowerInvariant()

$body = @{
    transaction_id     = "tx-demo-001"
    order_id           = $orderId
    status_code        = $statusCode
    gross_amount       = $grossAmount
    transaction_status = "settlement"
    signature_key      = $sig
} | ConvertTo-Json

Invoke-RestMethod -Method Post -Uri "http://localhost:8080/api/v1/webhooks/payment" -ContentType "application/json" -Body $body
```

## Testing, Formatting & Linting

Unit tests (no external database required):

```bash
make test
# Or full unit + migration + entrypoint packages as in CI:
go test -v -race ./internal/... ./migrations/... ./cmd/...
```

PostgreSQL integration & 200-concurrent-buyer tests (`tests/integration_test.go`, guarded by `//go:build integration`):

```bash
# Requires a running PostgreSQL instance matching DATABASE_URL
# (defaults to postgres://ticketing:ticketing_secret@localhost:5432/event_ticketing?sslmode=disable)
make test-integration
```

`tests/integration_test.go` exercises all invariants `B1` through `B10` end-to-end over HTTP and PostgreSQL:
- `TestIntegration_HappyFlow_OrderPayTicketsHistory`: multi-tier event creation, live quota view, 15-minute checkout hold, order access control, simulated payment, unique ticket issuance, and buyer order history.
- `TestIntegration_BuyerFourTicketLimit_AndCancellation`: concurrent `3 + 3` same-buyer bookings (`1` created, `1` `409 BUYER_LIMIT_EXCEEDED`), cancellation quota restoration, and allowance reset.
- `TestIntegration_ExpirySweeper_AndLateWebhookRejection`: backdated `expires_at` sweep via `ExpirySweeper.RunOnce`, quota recovery, `409 ORDER_EXPIRED` rejection on late settlement, `0` tickets issued, and `REJECTED_LATE_EXPIRED` + `requires_refund = true` audit logging.
- `TestIntegration_WebhookValidationAudit_AndFailureTransition`: missing/empty signature (`401` + `SIGNATURE_INVALID`), negative/mismatched `gross_amount` (`400` + `AMOUNT_MISMATCH`), and signed `deny` webhook restoring quota and transitioning `WAITING_PAYMENT` -> `CANCELLED` (`PROCESSED_FAILED`).
- `TestIntegration_WebhookIdempotency_10ParallelReplays`: 10 concurrent identical signed webhooks returning `200 OK` (`1` `PROCESSED_PAID`, `9` `IGNORED_DUPLICATE`) with exact `4` issued tickets.
- `TestIntegration_FlashSale_200ConcurrentBuyers_ZeroOversell`: 200 concurrent distinct buyers racing for 100 `Reguler` tickets (`100` `201 Created`, `100` `409 Conflict`, DB `SUM(total_tickets) == 100`, `remaining_quota == 0`).

Formatting, linting, and binary build:

```bash
make fmt
make lint
make build
```

Frontend development, OpenAPI SDK regeneration, lint, typecheck, and unit tests (`frontend/`, using `bun`):

```bash
cd frontend
bun install
bun run dev              # Vite dev server on :5173 with proxy to Go API :8080
bun run openapi:generate # Regenerates src/lib/api/generated from openapi.json
bun run typecheck
bun run lint
bun run test
bun run build
```
