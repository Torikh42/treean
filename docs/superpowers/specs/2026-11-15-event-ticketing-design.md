# Design Specification: Treean High-Concurrency Event Ticketing Platform

- **Date:** 2026-11-15 (Updated 2026-11-19 for Treean Web Console)
- **Repository:** `https://github.com/Torikh42/treean` (`github.com/torikh42/treean`)
- **Target Runtime:** Go 1.26 (`go1.26.2`), PostgreSQL 17, Docker Compose
- **Companion Docs:**
  - Product Spec: [`PRODUCT.md`](../../specs/event-ticketing/PRODUCT.md)
  - Technical Spec: [`TECH.md`](../../specs/event-ticketing/TECH.md)
  - Decision Log: [`DECISIONS.md`](../../DECISIONS.md)
  - Implementation Plan: [`2026-11-15-event-ticketing-implementation.md`](../plans/2026-11-15-event-ticketing-implementation.md)

---

## 1. Context & Constraints

From [`final-project.md`](../../../final-project.md), the system must implement an event ticket booking backend within an 8-day individual scope (deadline Saturday 21 November 2026, 23:59 WIB; 5-minute live demo Sunday 22 November 2026, 13:00 WIB):

- **Core Business Flows (8 rules):** Organizer creates events with 2+ ticket types (`Reguler`, `VIP`); real-time quota display; 1 to 4 ticket checkout with 15-minute quota hold; 4-ticket per buyer per event cap across active + paid orders; buyer cancellation before payment with immediate quota return; simulated payment + unique ticket code issuance with zero duplicate tickets; buyer order history & ticket listing.
- **Bonus Challenges:** Idempotent webhook resilient to duplicate, out-of-order, and late post-expiry deliveries; 200 concurrent requests against a 100-ticket quota resulting in exact 100 sold / 100 rejected (zero oversell).
- **Operational Deliverables:** Runs with one command (`docker compose up`), auto-migrates and auto-seeds on startup, includes automated tests (`unit` + `integration` + `200-concurrent` + `race`), pushes commit-SHA tagged images in CI/CD, and includes clear `README.md` + `DECISIONS.md`.

---

## 2. Selected Architecture & Layout

We selected **Pragmatic Clean Layered Architecture + DDD-Lite** (`handlers -> services -> repository` backed by a dependency-free `domain` core):

```text
event-ticketing/
├── .github/workflows/ci.yml
├── cmd/api/main.go                  # Composition root: config, pgxpool, migrate+seed, sweeper, server, graceful shutdown
├── internal/
│   ├── config/                      # Typed env config loader
│   ├── domain/                      # Pure entities, Order state machine, constants, DomainError codes (0 infra imports)
│   ├── handler/                     # Huma v2 + Chi handlers & OpenAPI struct schemas (/docs Scalar UI, /openapi.json)
│   ├── middleware/                  # Hybrid JWT + demo header auth, structured slog logger, panic recoverer
│   ├── repository/                  # pgx/v5 PostgreSQL repositories, dual-lock transactions, SKIP LOCKED sweeper
│   ├── service/                     # Consumer-defined repository interfaces + business orchestration
│   └── worker/                      # Context-managed 5s expiry sweeper goroutine
├── migrations/001_init_schema.sql   # DDL + check constraints + partial expiry index + invoice_seq + initial seed
├── web/                             # Embedded demo console (/demo) via go:embed (UI styling gated on antislop direction)
├── tests/                           # Integration & 200-concurrent flash-sale test suite (build tag: integration)
├── Dockerfile
├── docker-compose.yml
├── .golangci.yml
├── Makefile
├── DECISIONS.md
└── README.md
```

**Dependency Direction:** `handler -> service -> domain <- repository` (`service` defines repository interfaces it consumes; `domain` imports only standard library packages).

---

## 3. Core Architecture & Reliability Patterns

1. **Structured Domain Errors:**
   Every business failure returns a predictable HTTP status plus machine-readable `error_code` (`QUOTA_EXHAUSTED`, `BUYER_LIMIT_EXCEEDED`, `ORDER_EXPIRED`, `PAYMENT_AMOUNT_MISMATCH`, `UNAUTHORIZED_WEBHOOK`, `INVALID_ORDER_STATE`).
2. **Audit Logging Outside Transaction Boundaries:**
   - `payment_webhooks` inserts occur outside the rollback boundary of business transactions so invalid signatures, amount mismatches, and late webhooks remain auditable.
   - `order_logs` records every state transition (`WAITING_PAYMENT`, `PAID`, `CANCELLED`, `EXPIRED`) alongside actor type (`buyer`, `webhook`, `scheduler`, `lazy-check`).
3. **Locked State Re-Check & Exact Integer Amount Comparison:**
   - Webhook validation enforces fail-closed secret presence, constant-time SHA-512 comparison (`hmac.Equal`), and exact integer `int64` IDR matching (`gross_amount == order.TotalAmountIDR`).
   - Orders are locked with `SELECT ... FOR UPDATE` and state transitions are re-evaluated inside the transaction. Unlike non-quota booking flows ("PAID Always Wins"), finite flash-sale ticketing enforces **Quota Safety First**: expired orders never issue tickets after quota has been released.
4. **Atomic Multi-Item Reservation Without Invoice Lock Contention:**
   - Multi-item checkout supports ordering e.g. `2 Reguler + 2 VIP` in one order (`orders` + `order_items`).
   - Instead of global `SELECT MAX(...) FOR UPDATE` contention, invoice numbering uses PostgreSQL `SEQUENCE` (`invoice_seq`) to stay lock-free under 200 simultaneous buyers.
   - Per-buyer concurrency uses `pg_advisory_xact_lock(hashtext(event_id), hashtext(buyer_id))` followed by sorted `ticket_types` locks (`ORDER BY id ASC FOR UPDATE`) to eliminate deadlocks.
5. **Non-Blocking Batch Expiry Sweeper (`SKIP LOCKED`):**
   - The background ticker selects expired pending orders in small batches (`ORDER BY id ASC FOR UPDATE SKIP LOCKED LIMIT 50`) so it never stalls active buyers or blocks on concurrent payment/cancel transactions.

---

## 4. API Surface (Huma v2 + Chi v5 + Scalar UI)

- **Documentation:** `GET /docs` (Scalar UI), `GET /openapi.json` (OpenAPI 3.1 auto-generated from Go structs).
- **Demo Console:** `GET /demo` (serves embedded single-page helper UI for 5-minute live demo; visual HTML/CSS implementation gated on user's `antislop` mode & `DESIGN.md` confirmation).
- **Auth:**
  - `POST /api/v1/auth/register` (Register buyer or organizer; returns user + JWT)
  - `POST /api/v1/auth/login` (Login with email/password; returns JWT)
- **Events:**
  - `POST /api/v1/events` (`ORGANIZER`) Create event + 2+ ticket types
  - `GET /api/v1/events` (Public) List events with real-time remaining quota
  - `GET /api/v1/events/{id}` (Public) Event detail with real-time remaining quota
- **Orders & Tickets:**
  - `POST /api/v1/orders` (`BUYER`) Book 1 to 4 tickets (multi-item), 15-minute quota hold
  - `POST /api/v1/orders/{id}/cancel` (`BUYER`) Cancel `WAITING_PAYMENT` order & restore quota immediately
  - `GET /api/v1/orders` (`BUYER`) List buyer's orders
  - `GET /api/v1/orders/{id}` (`BUYER`) Order detail including `order_items`, `order_logs`, and issued `tickets`
- **Payments & Webhooks:**
  - `POST /api/v1/webhooks/payment` (Signed SHA-512 webhook handler with idempotency & late-payment guard)
  - `POST /api/v1/orders/{id}/pay` (`BUYER`) Demo convenience trigger that signs and invokes the payment processor for the given order
- **Flash-Sale Demo Endpoint:**
  - `POST /api/v1/demo/flash-sale` Runs 200 concurrent buyer bookings in-process against a dedicated 100-quota event (or user-specified event) and returns exact success/failure counts, zero-oversell verification, and elapsed milliseconds.

---

## 5. Verification Strategy

1. **Unit Tests (`internal/domain/...`, `internal/service/...`):** State machine transitions (`CanPay`, `CanCancel`), SHA-512 constant-time signature verification, Huma validation boundaries, service orchestration with interface mocks (`go test -race ./internal/...`).
2. **PostgreSQL Integration & Flash Sale Concurrency Tests (`//go:build integration` in `tests/`):**
   - Happy path: organizer creates event -> buyer books -> buyer pays -> unique tickets issued -> history verified.
   - Quota exhaustion & zero oversell under **200 concurrent goroutines** for **100 quota** (using a Go start barrier channel so all 200 goroutines fire simultaneously).
   - Same-buyer concurrent limit enforcement: 2 parallel requests of 3 tickets from 1 buyer -> only 1 succeeds (`3 <= 4`), 1 rejected (`BUYER_LIMIT_EXCEEDED`).
   - Cancellation quota recovery + buyer limit liberation.
   - 15-minute expiry (verified via backdated `expires_at` + immediate lazy check AND background ticker run).
   - Duplicate concurrent webhook replay (10 parallel identical webhooks -> 1 issues tickets, 9 return idempotent replay, DB contains exact `total_tickets` rows).
   - Late webhook after expiry -> rejected (`409 ORDER_EXPIRED`), zero tickets created, `payment_webhooks` records `REJECTED_LATE_EXPIRED` with `requires_refund = true`, quota intact.
3. **One-Command Docker & CI/CD Smoke Verification:** Fresh `docker compose up --build` boots PostgreSQL + auto-migration + seed + API; GitHub Actions runs lint, unit tests, PostgreSQL integration tests with `-race`, and pushes `ghcr.io/<owner>/event-ticketing:sha-<commit>` on push.
