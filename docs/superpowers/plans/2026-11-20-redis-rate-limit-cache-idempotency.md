# Hybrid Nginx Edge, Redis Fail-Open Rate Limiter & Catalog Cache, and Order Idempotency Implementation Plan

> **For agentic workers:** Implementing sequentially with TDD, strict `gofmt` / `golangci-lint` compliance, and `-race` verification.

**Goal:** Add database-backed `Idempotency-Key` on `POST /api/v1/orders`, Redis `fail-open` user-centric rate limiting & public event catalog caching (`3s` TTL + invalidation), Nginx Single-Entry-Point edge hardening (`limit_req` `429`, gzip, security headers, trusted proxy IP forwarding), `docker-compose.db-debug.yml`, CI Redis + `nginx -t` checks, and frontend `Idempotency-Key` + `RATE_LIMITED` support.

**Architecture:** PostgreSQL stays Single Source of Truth for zero-oversell quotas, 4-ticket buyer caps, 15m expiry (`ExpirySweeper`), and `(buyer_id, idempotency_key)` uniqueness. Redis (`github.com/redis/go-redis/v9`) operates purely as a fast `fail-open` layer (`500ms` timeout) for atomic Lua fixed-window rate limiting and short-TTL event catalog JSON cache invalidated on all event/order/payment/expiry mutations. Nginx fronts the single `api` container and PostgreSQL/Redis inside `app-network`.

**Tech Stack:** Go 1.26 (`chi/v5`, `Huma v2`, `pgx/v5`, `go-redis/v9`), PostgreSQL 17, Redis 7, Nginx 1.27 Alpine, React 19 + TanStack Query, GitHub Actions.

**Spec:** [docs/superpowers/specs/2026-11-20-redis-rate-limit-cache-idempotency-design.md](../specs/2026-11-20-redis-rate-limit-cache-idempotency-design.md)

## Global Constraints
- 1 `api` replica only (no `deploy.replicas: 3`).
- Redis failure / blank `REDIS_URL` / timeout (`500ms`) MUST be strictly `fail-open`.
- Proxy IP trust: Nginx overwrites `X-Forwarded-For $remote_addr` & `X-Real-IP $remote_addr`; Go only trusts `X-Real-IP` / `X-Forwarded-For` when `RemoteAddr` is loopback/private IP.
- Idempotent checkout replay on `POST /api/v1/orders` returns `201 Created` + `Idempotent-Replay: true` header + `*domain.Order`.
- Frontend OpenAPI spec/SDK and Vitest tests updated in lockstep.

## Tasks
- [ ] **Task 1: Config, Redis Store (`fail-open` Lua limiter + catalog cache), and Unit Tests**
- [ ] **Task 2: Domain/Migration `003_order_idempotency.sql` & `OrderRepo` `Idempotency-Key` + Affected-Event Returning Helpers**
- [ ] **Task 3: Client IP + Huma Rate Limit Middleware (`429 RATE_LIMITED`), `AuthService` Login Guard, Event Catalog Cache + Invalidation**
- [ ] **Task 4: `routes.NewRouter` + `cmd/api/main.go` Wiring + Graceful Shutdown**
- [ ] **Task 5: Nginx Edge Hardening, Single-Entry-Point `docker-compose.yml`, `docker-compose.db-debug.yml`, `.env.example`, `.github/workflows/ci.yml`**
- [ ] **Task 6: Frontend OpenAPI Regen, Checkout `Idempotency-Key`, `RATE_LIMITED` Error Hint & Tests**
- [ ] **Task 7: Integration Tests (`tests/integration_test.go`), `DECISIONS.md` / `README.md`, and Full Verification**
