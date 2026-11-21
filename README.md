# Treean: Event Ticketing API & Web Console

Treean is a flash-sale event ticketing platform built with a Go (`go1.26.2`) backend API (`pgx/v5`, `go-chi/chi/v5`, `Huma v2` OpenAPI 3.1 + Scalar UI, `go-redis/v9`) and a React 19 + Vite + TanStack frontend (`frontend/`).

Repository: `https://github.com/Torikh42/treean`

---

## 1. System Architecture (How Everything Connects)

In Docker Compose, the system uses a **Single Entry Point** pattern. Only the Nginx container (`frontend`) opens a port to your host machine (`http://localhost:5173`). The Go API, PostgreSQL, and Redis run inside a private Docker bridge network (`app-network`).

```text
                        Host Machine (Browser / curl)
                                     |
                           http://localhost:5173
                                     v
           +---------------------------------------------------+
           |  Nginx Edge Container (frontend, port 5173 -> 80) |
           |  - Serves React SPA static bundle (/)             |
           |  - Proxies /api, /docs, /openapi.json, /health    |
           |  - Overwrites X-Forwarded-For with $remote_addr   |
           |  - Edge IP flood limit: 50 req/s (burst 250)      |
           +---------------------------------------------------+
                                     |
                     Docker private network (app-network)
                                     v
           +---------------------------------------------------+
           |  Go API Container (api:8080, internal only)       |
           |  - Chi + Huma v2 OpenAPI 3.1 router               |
           |  - JWT Bearer auth + opt-in demo header auth      |
           |  - Background ExpirySweeper worker (every 5s)     |
           +---------------------------------------------------+
                      /                             \
      500ms timeout  / (Fail-Open)                   \  ACID Transactions
                    v                                 v
   +---------------------------------+   +---------------------------------+
   | Redis 7 (redis:6379, internal)  |   | PostgreSQL 17 (postgres:5432)   |
   | - Fixed-window Lua rate limiter |   | - Source of truth for quotas    |
   | - 3s read-through event cache   |   | - Row locks & advisory locks    |
   | - Auto-invalidated on writes    |   | - Order Idempotency-Key index   |
   +---------------------------------+   +---------------------------------+
```

### Why Each Layer Exists

| Component | Role | What Happens If It Fails? |
| :--- | :--- | :--- |
| **Nginx Edge (`frontend/nginx.conf`)** | Serves the React web app and routes API calls from the same origin (`http://localhost:5173/api/...`). This avoids browser CORS preflight overhead and keeps backend ports hidden from the host. | Entry point for traffic (`restart: unless-stopped`). |
| **Go API (`cmd/api`)** | Validates inputs, enforces role access (`ORGANIZER` vs `BUYER`), orchestrates database locks, and sweeps expired unpaid orders every 5 seconds. | Health check (`/health`) reports `503` if PostgreSQL is unreachable. |
| **Redis 7 (`internal/redisstore`)** | Speeds up public event catalog reads (`3s` cache) and enforces per-user and per-IP rate limits using atomic Lua scripts. | **Fail-Open**: if Redis is down, slow (`> 500ms`), or `REDIS_URL` is empty, the API logs a warning and continues serving traffic directly from PostgreSQL. |
| **PostgreSQL 17 (`internal/repository`)** | Single source of truth for ticket inventory, orders, idempotency keys, and payment webhook logs. | **Fail-Closed**: ticket bookings and webhook settlements never proceed without ACID transaction guarantees. |

---

## 2. How Core Ticketing Problems Are Solved

Full trade-offs are documented in [`DECISIONS.md`](DECISIONS.md) (`ADR-001` through `ADR-009`). Here is the plain-English summary:

1. **Zero Oversell During Flash Sales (`ADR-003`)**
   - **Problem:** 200 buyers click "Buy" at the same millisecond for 100 remaining tickets, or two buyers lock `[VIP, Reguler]` vs `[Reguler, VIP]` in opposite order and cause a deadlock.
   - **Solution:** Checkout sorts requested `ticket_types` by UUID (`ORDER BY id ASC FOR UPDATE`) before deducting `remaining_quota`, backed by a database constraint `CHECK (remaining_quota >= 0 AND remaining_quota <= total_quota)`. Sorting lock acquisition order prevents deadlocks, and row locking guarantees exact quota deduction.

2. **Max 4 Tickets per Buyer per Event (`ADR-004`)**
   - **Problem:** A buyer sends two parallel requests for 3 tickets each (`3 + 3 = 6`) to bypass the 4-ticket limit before either transaction commits.
   - **Solution:** At the start of checkout, PostgreSQL takes a transaction-scoped advisory lock for that specific `(event_id, buyer_id)` pair: `pg_advisory_xact_lock(hashtext(event_id), hashtext(buyer_id))`. Different buyers still run in parallel, while parallel requests from the same buyer wait in line and the second request is rejected with `409 BUYER_LIMIT_EXCEEDED`.

3. **15-Minute Payment Hold & Auto-Expiry (`ADR-005`)**
   - **Problem:** Unpaid reservations (`WAITING_PAYMENT`) must expire after 15 minutes and return their tickets to the pool immediately.
   - **Solution:** Hybrid expiry:
     - **Background Worker (`worker.ExpirySweeper`):** Runs every `5s`, picks up to 50 overdue orders using `FOR UPDATE SKIP LOCKED`, marks them `EXPIRED`, restores `remaining_quota`, and invalidates the Redis event cache.
     - **Lazy Pre-Check:** Event reads and order mutations check a partial index (`WHERE status = 'WAITING_PAYMENT'`) so overdue quota is released even between 5-second ticks.

4. **Safe Double-Click & Retry Checkout (`ADR-009`)**
   - **Problem:** A buyer on unstable mobile internet double-clicks checkout or retries a timed-out request and accidentally creates two orders.
   - **Solution:** `POST /api/v1/orders` accepts an `Idempotency-Key` header (stored in a `useRef` per checkout attempt on the frontend). PostgreSQL enforces a partial unique index on `(buyer_id, idempotency_key)`. Retrying with the same key and items returns the original order (`201 Created` + header `Idempotent-Replay: true`) without deducting quota twice. Using the same key with different items returns `409 IDEMPOTENCY_CONFLICT`.

5. **Payment Webhooks: Quota Safety First (`ADR-006`)**
   - **Problem:** Payment gateways retry webhooks multiple times, or a late payment webhook arrives after the 15-minute hold already expired and the ticket was sold to someone else.
   - **Solution:**
     - Signatures (`SHA-512(order_id + status_code + gross_amount + WEBHOOK_SECRET)`) are verified in constant time.
     - Every webhook attempt is saved to `payment_webhooks` outside the business transaction so failed attempts are still audited.
     - Duplicate `settlement` webhooks on an already `PAID` order return `200 OK` (`idempotent_replay: true`, `IGNORED_DUPLICATE`) without creating extra tickets.
     - Late `settlement` webhooks arriving after `EXPIRED` are rejected with `409 ORDER_EXPIRED` and flagged `REJECTED_LATE_EXPIRED` (`requires_refund = true`) so venue capacity is never oversold.

6. **Multi-Layer Rate Limiting & Short-TTL Cache (`ADR-009`)**
   - **Problem:** Shared campus/office WiFi puts many legitimate buyers behind one public IP, while bots try to spam account registration or hammer `GET /api/v1/events`.
   - **Solution:**
     - **Checkout (`POST /api/v1/orders`) & Cancel:** Rate-limited primarily per authenticated `user_id` (`20 req/min`) plus a high per-IP safety ceiling (`300 req/min`), so buyers on shared WiFi do not block each other.
     - **Registration (`POST /api/v1/auth/register`):** Strict per-IP limit (`5 req/15m`) to slow down bulk bot sign-ups.
     - **Login (`POST /api/v1/auth/login`):** Limited per `IP + email` (`10 req/min`) and per IP (`60 req/min`).
     - **Event Catalog Cache:** `GET /api/v1/events` and `GET /api/v1/events/{id}` cache responses in Redis for `3s` (`EVENT_CACHE_TTL`) and invalidate immediately whenever an event, order, payment webhook, or expiry sweep changes quota.

---

## 3. UI Design System (`DESIGN.md` & Antislop Rules)

The frontend (`frontend/`) follows [`DESIGN.md`](DESIGN.md) in a **Signal Yellow Web-Brutalist** style inspired by printed box-office ticket stubs and concert manifests. Every visual decision has an explicit functional purpose (`R-31`):

- **Antislop Dials (`R-37`):** `ENERGY 3 / RHYTHM 3 / MOTION 2`
  - **ENERGY 3:** High-contrast `#111111` structural borders (`2px` to `3px`) and sharp zero-blur offset shadows (`4px 4px 0 #111111`).
  - **RHYTHM 3:** Asymmetric split layouts where active checkout and flash-sale simulators take visual priority while tables and logs stay compact.
  - **MOTION 2:** Tactile `2px` button press displacement on hover/active with zero infinite decorative animations, respecting `prefers-reduced-motion`.
- **Color Palette (`<= 3 Core + 1 Accent`, `R-29`, WCAG AA/AAA `R-25`):**
  - **Canvas Ground (`#F6F3EB`):** Warm archival paper cream to reduce glare compared to pure white.
  - **Card Surface (`#FFFFFF`) & Muted Band (`#EDE8DC`):** Separates ticket cards from table headers and metadata rows.
  - **Primary Ink & Border (`#111111`):** High-contrast structural framing and typography.
  - **Primary Accent (`#FFE500` Signal Yellow):** Reserved strictly for primary CTAs, active navigation tabs, and `:focus-visible` outlines (`>= 16:1` contrast with `#111111` ink).
  - **Semantic Status Badges (`R-09`):** Used only for real order/system states: `PAID`/`OK` (`#DCFCE7` fill, `#166534` ink), `WAITING_PAYMENT` (`#FEF3C7` fill, `#92400E` ink), and `CANCELLED`/`EXPIRED`/`UNHEALTHY` (`#FEE2E2` fill, `#991B1B` ink).
- **Typography (`R-06`):**
  - **`Space Grotesk Variable`:** Geometric display and UI sans for titles, navigation, and quota counters.
  - **`IBM Plex Mono`:** Used strictly for tabular data: ticket codes (`TKT-...`), UUIDs, IDR currency alignment, `MM:SS` hold timers, and raw webhook JSON payloads.
- **Interaction & Code Hygiene (`R-03`, `R-24`, `R-26`, `R-27`):**
  - Every route and button triggers a real TanStack Router navigation or OpenAPI mutation (zero dead links or placeholder buttons).
  - Every data view implements explicit **Loading**, **Empty**, and **Error** states (`StateNotice` and `StatusBadge`) surfacing backend `error_code` and `Retry-After` feedback.
  - Minimum `44px` tap targets, visible keyboard focus rings, responsive reflow down to `375px`, and strict separation between logic (`frontend/src/hooks/`) and presentational JSX (`frontend/src/components/`).

---

## 4. Project Structure (Clean Layered Architecture)

```text
.
├── cmd/api/                      # Minimal entrypoint: config, migrations, Redis, worker, graceful shutdown
├── internal/
│   ├── config/                   # Env loader, validation, defaults, and slog secret redaction
│   ├── domain/                   # Pure entities, order state machine (CanPay/CanCancel), SHA-512 & error codes
│   ├── handler/                  # HTTP/Huma handlers, request/response DTOs, and domain error mapper
│   ├── middleware/               # Client IP extractor, JWT + demo auth, Redis rate limiter, CORS, logger, recoverer
│   ├── redisstore/               # Fail-open go-redis client, Lua fixed-window limiter, and 3s event catalog cache
│   ├── repository/               # pgxpool PostgreSQL repositories, locking SQL, and advisory-locked migrator
│   ├── routes/                   # Chi router, Huma v2 OpenAPI 3.1 + Scalar setup, and route registration
│   ├── service/                  # Business use cases and consumer-defined repository/cache interfaces
│   └── worker/                   # Background ExpirySweeper ticker (FOR UPDATE SKIP LOCKED + cache invalidation)
├── frontend/                     # React 19 + Vite + TanStack Router/Query/Form + @hey-api/openapi-ts + Nginx config
├── migrations/                   # Embedded SQL: 001_init_schema.sql, 002_seed_demo.sql, 003_order_idempotency.sql
├── tests/                        # PostgreSQL + Redis integration & 200-concurrent-buyer suite (-tags=integration)
├── web/                          # Embedded minimal /demo API HTML landing stub
├── DECISIONS.md                  # Architecture Decision Records (ADR-001 through ADR-009)
├── DESIGN.md                     # Signal Yellow Web-Brutalism design tokens & antislop rules
├── docker-compose.yml            # Single-entry-point runtime (Nginx :5173 + Go API + PostgreSQL 17 + Redis 7)
├── docker-compose.db-debug.yml   # Optional port override exposing :5432, :6379, and :8080 to localhost
├── Dockerfile                    # Multi-stage Alpine build for the Go API binary
└── Makefile                      # Shortcuts for docker compose, tests, linter, formatter, and git hooks
```

---

## 5. Quick Start (Docker Compose)

Start PostgreSQL 17, Redis 7, the Go API, and the Nginx frontend with one command:

```bash
docker compose up -d --build
```

Or via `Makefile`:

```bash
make up
```

On startup, `api` waits for PostgreSQL, acquires a migration advisory lock, applies embedded migrations (`001_init_schema.sql` through `003_order_idempotency.sql`), and seeds the demo accounts and `Tech Flash Concert 2026`.

### Access URLs (via Nginx Single Entry Point on `:5173`)

- **Web Console (React SPA):** `http://localhost:5173`
- **Scalar Interactive API Docs:** `http://localhost:5173/docs`
- **OpenAPI 3.1 JSON Spec:** `http://localhost:5173/openapi.json`
- **Health Check:** `http://localhost:5173/health` (`200 {"status":"ok"}` when DB is healthy)
- **Backend Landing Stub:** `http://localhost:5173/demo`

### Optional: Expose Internal Ports (`:5432`, `:6379`, `:8080`) for Local Debugging

By default, PostgreSQL, Redis, and the Go API are isolated inside `app-network`. If you want to connect DataGrip, `psql`, `redis-cli`, or run host integration tests directly against the containers, layer `docker-compose.db-debug.yml`:

```bash
docker compose -f docker-compose.yml -f docker-compose.db-debug.yml up -d
```

### Stopping Containers

```bash
# Stop containers and keep database volume (pgdata):
docker compose down --remove-orphans

# Or stop containers AND wipe pgdata volume to reset demo state:
make down
```

---

## 6. Configuration (`.env.example`)

`cmd/api` reads configuration from environment variables (see [`.env.example`](.env.example) and [`internal/config/config.go`](internal/config/config.go)):

| Variable | Required | Default (`config.Load`) | Compose Value | Description |
| :--- | :--- | :--- | :--- | :--- |
| `PORT` | No | `8080` | `8080` | Go API HTTP listen port inside the container |
| `FRONTEND_PORT` | No | - | `5173` | Host port mapped to the Nginx `frontend` container (`5173:80`) |
| `DATABASE_URL` | Yes | - | `postgres://ticketing:ticketing_secret@postgres:5432/event_ticketing?sslmode=disable` | PostgreSQL connection string |
| `REDIS_URL` | No | `""` (disabled) | `redis://redis:6379/0` | Redis connection URL; leave blank to disable Redis cleanly (`fail-open`) |
| `REDIS_TIMEOUT` | No | `500ms` | `500ms` | Max dial/read/write timeout per Redis operation before failing open |
| `EVENT_CACHE_TTL` | No | `3s` | `3s` | Redis TTL for public `GET /api/v1/events` and `GET /api/v1/events/{id}` cache |
| `RATE_LIMIT_ENABLED` | No | `true` | `true` | Enables Redis rate limiting when `REDIS_URL` is configured |
| `RATE_LIMIT_REGISTER_MAX` | No | `5` | `5` | Max `POST /api/v1/auth/register` requests per IP per `15 minutes` |
| `RATE_LIMIT_LOGIN_MAX` | No | `10` | `10` | Max `POST /api/v1/auth/login` attempts per `IP + email` per `1 minute` |
| `RATE_LIMIT_LOGIN_IP_MAX` | No | `60` | `60` | Max `POST /api/v1/auth/login` requests per IP per `1 minute` |
| `RATE_LIMIT_ORDER_USER_MAX` | No | `20` | `20` | Max order create/cancel requests per authenticated `user_id` per `1 minute` |
| `RATE_LIMIT_ORDER_IP_MAX` | No | `300` | `300` | Max order create/cancel requests per IP per `1 minute` (shared WiFi ceiling) |
| `RATE_LIMIT_DEMO_FLASH_SALE_MAX` | No | `5` | `5` | Max `POST /api/v1/demo/flash-sale` runs per IP per `1 minute` |
| `JWT_SECRET` | Yes | - | `dev-jwt-secret-change-in-production` | HS256 secret used to sign and verify Bearer JWT tokens |
| `WEBHOOK_SECRET` | Yes | - | `dev-webhook-secret-change-in-production` | Shared secret for SHA-512 payment webhook verification (fails closed if blank) |
| `ALLOW_DEMO_HEADER_AUTH` | No | `false` | `true` | Allows `X-User-ID` and `X-User-Role` demo headers for local concurrency tests |
| `EXPIRY_SWEEP_INTERVAL` | No | `5s` | `5s` | Interval between background `ExpirySweeper` runs |
| `HTTP_READ_TIMEOUT` | No | `15s` | `15s` | HTTP server read timeout |
| `HTTP_WRITE_TIMEOUT` | No | `30s` | `30s` | HTTP server write timeout |
| `DB_MAX_CONNS` | No | `50` | `50` | Maximum `pgxpool` database connections |
| `DB_MIN_CONNS` | No | `5` | `5` | Minimum idle `pgxpool` database connections |
| `CORS_ALLOWED_ORIGINS` | No | `http://localhost:5173,...` | `http://localhost:5173,http://127.0.0.1:5173` | Comma-separated allowed browser origins (`*` supported if explicitly set) |

`docker-compose.yml` also supports optional `POSTGRES_USER` (`ticketing`), `POSTGRES_PASSWORD` (`ticketing_secret`), and `POSTGRES_DB` (`event_ticketing`).

---

## 7. Seeded Demo Accounts & Event

Seeded automatically by [`migrations/002_seed_demo.sql`](migrations/002_seed_demo.sql):

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

---

## 8. Authentication Modes

Protected endpoints support two authentication modes ([`internal/middleware/auth.go`](internal/middleware/auth.go)):

1. **Bearer JWT (Standard):** Sign up via `POST /api/v1/auth/register` or sign in via `POST /api/v1/auth/login` and send `Authorization: Bearer <access_token>`. On page reload, the web app validates stored tokens against `GET /api/v1/auth/me`.
2. **Local Demo Headers (Opt-in):** When `ALLOW_DEMO_HEADER_AUTH=true`, requests may pass `X-User-ID: <uuid>` and `X-User-Role: ORGANIZER|BUYER`. Missing demo accounts are created on the fly via `EnsureDemoUser` so 200-buyer load tests can run without pre-generating 200 bcrypt JWTs.

---

## 9. Web Console Routes & RBAC Guards (`frontend/`)

| Route | Access / Guard | Purpose |
| :--- | :--- | :--- |
| `/` | Public | Live Event Box Office catalog with title/venue search and remaining tier quotas |
| `/events/$eventId` | Public view / `BUYER` checkout | Event & tier quota breakdown; guests see a sign-in prompt, `ORGANIZER` accounts see a Buyer-only notice, and `BUYER` accounts can reserve `1..4` tickets with automatic `Idempotency-Key` protection |
| `/flash-sale` | Public | 200-concurrent Flash Sale benchmark (`POST /api/v1/demo/flash-sale`) and interactive SHA-512 Webhook Lab |
| `/login` | Guest-only (`requireGuest`) | Email/password JWT sign-in with sanitized `redirect` support |
| `/register` | Guest-only (`requireGuest`) | Create a `BUYER` or `ORGANIZER` account (`POST /api/v1/auth/register`) |
| `/forbidden` | Public | Role mismatch notice (`403`) with direct navigation back to valid pages |
| `/orders`, `/orders/$orderId` | `BUYER` (`requireRole("BUYER")`) | Buyer order history, live 15-minute `MM:SS` hold countdown, one-click pay/cancel, and issued ticket codes |
| `/organizer` | `ORGANIZER` (`requireRole("ORGANIZER")`) | Organizer Event CRUD Console (list owned events, create events, edit tiers/quotas, delete unused events) |

---

## 10. API Endpoints

All routes are available through the Nginx entry point at `http://localhost:5173` (or directly on `http://localhost:8080` when running `go run ./cmd/api` or `docker-compose.db-debug.yml`). Rate-limited endpoints return `429 RATE_LIMITED` with a `Retry-After` header when exceeded.

| Method | Path | Auth | Description |
| :--- | :--- | :--- | :--- |
| `GET` | `/health` | Public | Pings PostgreSQL (`200 {"status":"ok"}` or `503 {"status":"unhealthy"}`) |
| `GET` | `/docs` | Public | Scalar interactive OpenAPI 3.1 documentation UI |
| `GET` | `/openapi.json` | Public | Generated OpenAPI 3.1 JSON specification |
| `GET` | `/demo` | Public | Minimal HTML API landing stub |
| `POST` | `/api/v1/auth/register` | Public | Register `ORGANIZER` or `BUYER` account and return JWT (`201`, IP rate-limited) |
| `POST` | `/api/v1/auth/login` | Public | Authenticate email/password and return JWT (`200`, `IP + email` and IP rate-limited) |
| `GET` | `/api/v1/auth/me` | Authenticated | Return caller profile (`200`, or `401` if token is invalid or user was deleted) |
| `POST` | `/api/v1/events` | `ORGANIZER` | Create event with 2 to 10 ticket tiers atomically (`201`, invalidates event cache) |
| `GET` | `/api/v1/events` | Public | List public events with live remaining quota (`200`, `3s` Redis read-through cache) |
| `GET` | `/api/v1/organizer/events` | `ORGANIZER` | List events owned by the authenticated organizer (`200`, always reads PostgreSQL) |
| `GET` | `/api/v1/events/{id}` | Public | Get single event detail and tier quotas (`200`, `3s` Redis read-through cache) |
| `PUT` | `/api/v1/events/{id}` | Owner `ORGANIZER` | Update event and upsert/validate ticket tiers atomically (`200`, `403`, `409`) |
| `DELETE` | `/api/v1/events/{id}` | Owner `ORGANIZER` | Delete an event that has zero orders (`204`, `403`, `409 EVENT_HAS_ORDERS`) |
| `POST` | `/api/v1/orders` | Authenticated | Reserve `1..4` tickets for 15 minutes (`201`). Accepts optional `Idempotency-Key` header (max 128 chars); replays return `201` + `Idempotent-Replay: true`, payload mismatches return `409 IDEMPOTENCY_CONFLICT` |
| `GET` | `/api/v1/orders` | Authenticated | List caller's orders with items and issued tickets (`200`) |
| `GET` | `/api/v1/orders/{id}` | Buyer / Event Organizer | Get order detail with items, status logs, and issued tickets (`200`) |
| `POST` | `/api/v1/orders/{id}/cancel` | Order Buyer | Cancel `WAITING_PAYMENT` order, restore quota, and invalidate cache (`200`) |
| `POST` | `/api/v1/orders/{id}/pay` | Buyer / Event Organizer | Demo helper that signs and submits a settlement webhook (`200`) |
| `POST` | `/api/v1/webhooks/payment` | Signed Webhook | Process Midtrans-style SHA-512 payment callback (`200`, `400`, `401`, `404`, `409`) |
| `POST` | `/api/v1/demo/flash-sale` | Public | Run in-process concurrent booking benchmark (default `requests=200`, `quota=100`, max `500`, IP rate-limited) |

---

## 11. Payment Webhook Verification & Examples

`POST /api/v1/webhooks/payment` verifies the SHA-512 signature using:

```text
signature_key = hex(sha512(order_id + status_code + gross_amount + WEBHOOK_SECRET))
```

`gross_amount` is formatted as a base-10 integer string (for example `300000`), and `signature_key` is compared in constant time (`hmac.Equal`, lowercase hex).

### Webhook Outcomes

- **Invalid/missing signature or blank `WEBHOOK_SECRET`:** Rejects with `401 UNAUTHORIZED_WEBHOOK` and logs `outcome = SIGNATURE_INVALID` in `payment_webhooks`.
- **Negative or mismatched `gross_amount`:** Rejects with `400 PAYMENT_AMOUNT_MISMATCH` and logs `outcome = AMOUNT_MISMATCH`.
- **Valid `settlement` or `capture` before `expires_at`:** Transitions `WAITING_PAYMENT` -> `PAID`, issues unique `ticket_code` rows once (`UNIQUE (order_item_id, seat_sequence)`), logs `PROCESSED_PAID`, and returns `200 OK`.
- **Duplicate `settlement` / `capture` on an already `PAID` order:** Returns `200 OK` with `idempotent_replay: true`, returns existing tickets without inserting duplicates, and logs `IGNORED_DUPLICATE`.
- **Late `settlement` / `capture` after expiry (or on `EXPIRED` / `CANCELLED` order):** Never issues tickets (Quota Safety First); returns `409 ORDER_EXPIRED` (or `409 INVALID_ORDER_STATE` if cancelled), restores quota if newly expired, and logs `outcome = REJECTED_LATE_EXPIRED` with `requires_refund = true`.
- **Gateway failure status (`deny`, `cancel`, `expire`):** Returns `200 OK` (`outcome = PROCESSED_FAILED`), restores reserved tier quota, invalidates the event cache, and transitions `WAITING_PAYMENT` orders to `CANCELLED` (or `EXPIRED` for `expire`).

### Curl Example (Bash, targeting `:5173` Single Entry Point)

```bash
ORDER_ID="<order-uuid>"
STATUS_CODE="200"
GROSS_AMOUNT="300000"
WEBHOOK_SECRET="dev-webhook-secret-change-in-production"

SIG=$(printf '%s' "${ORDER_ID}${STATUS_CODE}${GROSS_AMOUNT}${WEBHOOK_SECRET}" | sha512sum | awk '{print $1}')

curl -i -X POST http://localhost:5173/api/v1/webhooks/payment \
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

### PowerShell Example (targeting `:5173` Single Entry Point)

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

Invoke-RestMethod -Method Post -Uri "http://localhost:5173/api/v1/webhooks/payment" -ContentType "application/json" -Body $body
```

---

## 12. Testing, Formatting & Linting

### Unit Tests (No External Database Required)

```bash
make test
# Or run all Go unit, migration, and entrypoint packages as in CI:
go test -v -race ./internal/... ./migrations/... ./cmd/...
```

### Integration & Concurrency Tests (`tests/integration_test.go`, `-tags=integration`)

Requires a running PostgreSQL instance matching `DATABASE_URL` (and optionally `REDIS_URL` to run the live Redis rate-limit and cache integration test):

```bash
make test-integration
```

`tests/integration_test.go` verifies all 11 end-to-end scenarios against live PostgreSQL (and Redis when `REDIS_URL` is set):

- `TestIntegration_HappyFlow_OrderPayTicketsHistory`: multi-tier event creation, live quota view, 15-minute checkout hold, order access control, payment settlement, unique ticket issuance, and buyer order history.
- `TestIntegration_BuyerFourTicketLimit_AndCancellation`: concurrent `3 + 3` same-buyer bookings (`1` created, `1` `409 BUYER_LIMIT_EXCEEDED`), cancellation quota restoration, and allowance reset.
- `TestIntegration_ExpirySweeper_AndLateWebhookRejection`: backdated `expires_at` sweep via `ExpirySweeper.RunOnce`, quota recovery, `409 ORDER_EXPIRED` rejection on late settlement, `0` tickets issued, and `REJECTED_LATE_EXPIRED` + `requires_refund = true` audit logging.
- `TestIntegration_WebhookValidationAudit_AndFailureTransition`: missing/empty signature (`401` + `SIGNATURE_INVALID`), negative/mismatched `gross_amount` (`400` + `AMOUNT_MISMATCH`), and signed `deny` webhook restoring quota and transitioning `WAITING_PAYMENT` -> `CANCELLED` (`PROCESSED_FAILED`).
- `TestIntegration_WebhookIdempotency_10ParallelReplays`: 10 concurrent identical signed webhooks returning `200 OK` (`1` `PROCESSED_PAID`, `9` `IGNORED_DUPLICATE`) with exact `4` issued tickets.
- `TestIntegration_FlashSale_200ConcurrentBuyers_ZeroOversell`: 200 concurrent distinct buyers racing for 100 `Reguler` tickets (`100` `201 Created`, `100` `409 Conflict`, DB `SUM(total_tickets) == 100`, `remaining_quota == 0`).
- `TestIntegration_AuthMe_And_OrganizerEventsList`: JWT `/api/v1/auth/me` verification (without leaking `password_hash`) and organizer-only event ownership isolation on `GET /api/v1/organizer/events`.
- `TestIntegration_OrganizerEventUpdate_SwapReuseAndConflicts`: atomic tier updates, quota shrink protection (`409 QUOTA_CONFLICT`), ordered-tier deletion protection (`409 TIER_HAS_ORDERS`), and tier name swap/reuse without unique constraint errors.
- `TestIntegration_OrganizerEventDelete`: blocks deleting events with existing orders (`409 EVENT_HAS_ORDERS`) and allows `204 No Content` deletion for unused events.
- `TestIntegration_OrderIdempotencyKey_ConcurrentReplay`: 5 concurrent checkout requests with the same `Idempotency-Key` create exactly 1 order and deduct quota once, and subsequent replay returns `201 Created` with `Idempotent-Replay: true` even after remaining quota reaches `0`.
- `TestIntegration_RateLimiting_And_EventCache_WithRedis`: verifies live Redis IP rate limiting on `/api/v1/auth/register` (`429 RATE_LIMITED`), per-user rate limiting on `/api/v1/orders` without blocking another buyer on the same IP, and `3s` event catalog cache population plus invalidation on checkout.

### Formatting, Linting & Pre-Commit Hooks

```bash
make hooks # Configures .githooks/pre-commit (env guard, gofmt, go vet, oxlint, tsc)
make fmt
make lint
make build
```

### Frontend Commands (`frontend/`, using `bun`)

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
