# Design Spec: Hybrid Nginx Single-Entry-Point, Redis Fail-Open Rate Limiter & Cache, and Order Idempotency-Key

Date: 2026-11-20
Proposed Branch: `feat/redis-rate-limit-idempotency` (branched from `refactor/backend-routes-package`)
Product: Treean (`github.com/torikh42/treean`)

## Goal
Upgrade **Treean** with Tugas-2-aligned Single Entry Point Nginx hardening plus AmbaTicket-inspired resilience:
1. **PostgreSQL `Idempotency-Key`** on `POST /api/v1/orders` (plus stable client key across checkout retries).
2. **Defense-in-Depth Hybrid Rate Limiting:**
   - **Outer Edge (Nginx):** Coarse IP flood protection (`limit_req` returning HTTP `429`), security headers, gzip, `server_tokens off`, explicit proxy timeouts, and single-entry-point host port isolation (1 API replica).
   - **Inner App (Go Middleware + Redis, strict `fail-open`):** Fine-grained per-`user_id` (`actor.ID`) limits on authenticated order actions, strict IP throttling on `/api/v1/auth/register` (anti-Sybil), and `IP + sha256(email)` throttling on `/api/v1/auth/login` before `bcrypt`.
3. **Short-TTL Redis Read-Through Cache** (`EVENT_CACHE_TTL`, default `3s`) for public `GET /api/v1/events` and `GET /api/v1/events/{id}` with best-effort mutation invalidation.
4. **Single Entry Point Compose + Debug Override & CI Validation:** `docker-compose.yml` exposes only Nginx (`:5173`), isolating `api`, `postgres`, and `redis` on an internal bridge network (`app-network`); `docker-compose.db-debug.yml` exposes DB/Redis ports when needed locally; CI validates `nginx -t` and both compose configs.

## Constraints, Non-Goals & Anti-Goals
- **Single API Replica Only:** Keep 1 `api` service instance (no `deploy.replicas: 3` or Docker DNS `upstream` multi-replica load balancing requested).
- **Single Source of Truth Stays PostgreSQL:** Zero-oversell row locks (`ORDER BY id ASC FOR UPDATE`), 4-ticket `pg_advisory_xact_lock`, `CHECK` constraints, and hybrid `ExpirySweeper` (`5s` + partial-index lazy checks) MUST remain untouched in PostgreSQL.
- **No Redis Queue / No Redis Sessions:** Do NOT replace `ExpirySweeper` with Redis delayed jobs, and do NOT replace Stateless Bearer JWT with stateful Redis sessions.
- **Strict Fail-Open on Redis Failure:** If `REDIS_URL` is blank, unreachable, or exceeds `REDIS_TIMEOUT` (`500ms`), both Go rate limiting and catalog caching MUST degrade transparently (allow traffic / fall back to Postgres) without failing unit tests or blocking checkouts.
- **Preserve 200-Concurrent-Buyer Benchmark:** Nginx edge `limit_req` burst (`rate=50r/s`, `burst=250 nodelay`) and Go IP ceilings (`RATE_LIMIT_ORDER_IP_MAX=300`) must allow local 200-buyer concurrent runs without false `429` rejections while still stopping runaway floods.
- **Explicit Dependency Gate:** Adding `github.com/redis/go-redis/v9` and `redis:7-alpine` in `docker-compose.yml` awaits explicit user confirmation of this final spec before running `go get` or editing source code.

## Approaches Considered
- **Approach A — Go Middleware + Redis (`fail-open`) Only:**
  Clean single config layer in Go, protects `:8080` directly, no Nginx changes.
  *Rejected by user:* lacks edge Nginx hardening, single-entry-point isolation, gzip/security headers, and `nginx -t` CI gate matching Tugas 2.
- **Approach B — Hybrid Edge Nginx Single-Entry-Point + Go Redis Middleware + Catalog Cache + Postgres `Idempotency-Key` (Chosen):**
  Combines Tugas 2's Nginx edge gateway (`server_tokens off`, gzip, security headers, `limit_req` `429`, port isolation + `docker-compose.db-debug.yml`, `nginx -t` CI) with Go + Redis user-aware `fail-open` limits, short-TTL catalog caching, and DB-enforced order idempotency.
- **Approach C — Nginx `limit_req` Only (No Redis):**
  Zero Redis dependency, IP-only rate limit in Nginx.
  *Rejected:* cannot enforce per-`user_id` or `IP+email` rules, unfairly blocks shared WiFi/NAT users, and provides no catalog cache.

---

## Detailed Design

### 1. Checkout Idempotency (`Idempotency-Key` on `POST /api/v1/orders`)
- **Migration (`migrations/003_order_idempotency.sql`):**
  - `ALTER TABLE orders ADD COLUMN IF NOT EXISTS idempotency_key TEXT NULL;`
  - `ALTER TABLE orders ADD CONSTRAINT chk_orders_idempotency_key_len CHECK (idempotency_key IS NULL OR char_length(idempotency_key) BETWEEN 1 AND 128);`
  - `CREATE UNIQUE INDEX IF NOT EXISTS uidx_orders_buyer_idempotency_key ON orders (buyer_id, idempotency_key) WHERE idempotency_key IS NOT NULL;`
- **API & Domain (`internal/domain/order.go`, `internal/handler/order_handler.go`):**
  - Accept optional HTTP header `Idempotency-Key` (`header:"Idempotency-Key"`) on `OrderHandler.Create`; trimmed whitespace (`""` treated as omitted); `> 128` runes returns `422 VALIDATION_ERROR`.
  - Expose `IdempotencyKey *string` (`json:"idempotency_key,omitempty"`) on `domain.Order`.
- **Atomic Persistence (`internal/service/order_service.go`, `internal/repository/order_repo.go`):**
  - Insert `NULLIF($idempotencyKey, '')` inside `createOrderInTx`.
  - On PostgreSQL unique violation (`pgconn.PgError` SQLSTATE `23505`, `ConstraintName == "uidx_orders_buyer_idempotency_key"`): roll back tx cleanly (releasing held quota & locks), query existing `(buyer_id, idempotency_key)` order with items/logs, and return HTTP `201 Created` with `*domain.Order` plus `Idempotent-Replay: true` response header (`*domain.Order` schema stays identical for `@hey-api/openapi-ts` compatibility). First-writer wins for a given `(buyer_id, idempotency_key)`.
- **Frontend Integration (`frontend/src/hooks/useEventCheckout.ts` + regenerated OpenAPI client):**
  - Maintain a stable UUID (`crypto.randomUUID()`) per pending checkout selection; clear/rotate it when tier quantities change or checkout succeeds; pass `headers: { "Idempotency-Key": key }` on `createOrderMutation`.

### 2. Edge Gateway & Single Entry Point (`frontend/nginx.conf`, `Dockerfile`, `docker-compose*.yml`)
- **Nginx Configuration (`frontend/nginx.conf`):**
  - Add `server_tokens off;`
  - Security headers (`always`): `X-Content-Type-Options "nosniff"`, `X-Frame-Options "DENY"`, `Referrer-Policy "no-referrer"` (repeated in any `location` block that sets `add_header` so Nginx header inheritance never drops them).
  - Gzip compression: `gzip on; gzip_vary on; gzip_proxied any; gzip_comp_level 5; gzip_min_length 256; gzip_types application/json text/plain text/css application/javascript image/svg+xml;`
  - Coarse DDoS/Flood Edge Rate Limit:
    - `limit_req_zone $binary_remote_addr zone=edge_api_limit:10m rate=50r/s;`
    - `limit_req_status 429;`
    - Proxy location `^/(api|docs|openapi\.json|health|schemas|demo)`: applies `limit_req zone=edge_api_limit burst=250 nodelay;`, overwrites `X-Real-IP $remote_addr` and `X-Forwarded-For $remote_addr` (Nginx is the sole untrusted edge so client-forged `X-Forwarded-For` cannot spoof rate-limit IPs), forwards `Host` and `X-Forwarded-Proto`, and sets `proxy_connect_timeout 2s`, `proxy_read_timeout 30s`, `proxy_send_timeout 15s`.
  - Add `location = /health` returning or proxying cleanly to `api:8080/health` so gateway probes work out of the box.
- **Single Entry Point & Isolation (`docker-compose.yml` & `docker-compose.db-debug.yml`):**
  - Shared bridge network `app-network`.
  - `postgres`: `expose: ["5432"]` (NO host `ports` in base compose), existing healthcheck, `app-network`.
  - `redis` (`redis:7-alpine`): `expose: ["6379"]`, healthcheck `redis-cli ping`, `app-network` (NO host `ports` in base compose).
  - `api`: single instance (no `replicas`), `expose: ["8080"]` (NO host `ports` in base compose), `healthcheck` via `/app/api -healthcheck` or `wget -qO- http://127.0.0.1:8080/health || exit 1` (Alpine includes BusyBox `wget`? If Alpine 3.21 has `wget`, keep `wget`; verify during build!), `depends_on: postgres: condition: service_healthy`, `redis: condition: service_started`, `REDIS_URL: redis://redis:6379/0`.
  - `frontend` (Nginx gateway): only container exposing host port `${FRONTEND_PORT:-5173}:80` (with `depends_on: api: condition: service_healthy`), `app-network`.
  - **`docker-compose.db-debug.yml`** (optional local debug override, matching Tugas 2): exposes `postgres` (`${DB_DEBUG_PORT:-5432}:5432`), `redis` (`${REDIS_DEBUG_PORT:-6379}:6379`), and `api` (`${API_DEBUG_PORT:-8080}:8080`) for DataGrip, direct `curl :8080`, or `make test-integration`.
- **CI Validation (`.github/workflows/ci.yml`):**
  - Add `redis:7-alpine` service (`REDIS_URL: redis://localhost:6379/0`) to `go-integration` so real Lua rate limiting, event catalog cache hit/miss/invalidation, and checkout idempotency execute in CI. Existing integration tests flush/isolate Redis keys or configure test thresholds per-test, while dedicated Redis tests assert strict rate-limit cutoffs (`429`) and cache invalidation on sweeper/webhook transitions.
  - Add Nginx config syntax pre-flight (`docker run --rm --add-host api:127.0.0.1 -v "${{ github.workspace }}/frontend/nginx.conf:/etc/nginx/conf.d/default.conf:ro" nginx:1.27-alpine nginx -t`) matching `frontend/Dockerfile`'s `nginx:1.27-alpine`.
  - Validate both `docker compose config --quiet` and `docker compose -f docker-compose.yml -f docker-compose.db-debug.yml config --quiet`.

### 3. Go Redis Client & `fail-open` Rate Limiter (`internal/config`, `internal/redisstore`, `internal/middleware`)
- **Dependency:** `github.com/redis/go-redis/v9`
- **Config (`internal/config/config.go`):**
  - `REDIS_URL` (`""` optional; redacted in `LogValue()`), `REDIS_TIMEOUT` (`500ms`), `EVENT_CACHE_TTL` (`3s`).
  - `RATE_LIMIT_ENABLED` (`true` default; active only when `REDIS_URL != ""` and `RATE_LIMIT_ENABLED=true`).
  - Limits: `RATE_LIMIT_REGISTER_MAX` (`5` / `15m` per IP), `RATE_LIMIT_LOGIN_MAX` (`10` / `1m` per `IP+sha256(email)` + `60` / `1m` IP ceiling), `RATE_LIMIT_ORDER_USER_MAX` (`20` / `1m` per `actor.ID`), `RATE_LIMIT_ORDER_IP_MAX` (`300` / `1m` IP ceiling), `RATE_LIMIT_DEMO_FLASH_SALE_MAX` (`5` / `1m` per IP).
- **Redis Atomic Limiter (`internal/redisstore`):**
  - Fixed-window Lua script (`INCR` + conditional `PEXPIRE` + `PTTL`) returning `(allowed bool, retryAfter time.Duration, err error)`.
  - On nil client, connection failure, or `REDIS_TIMEOUT`: log warning and return `allowed = true` (`fail-open`).
  - Graceful shutdown closes Redis client in `cmd/api/main.go`.
- **Client IP & Middleware Enforcement:**
  - Chi middleware `middleware.WithClientIP` uses `RemoteAddr` host by default; when `RemoteAddr` is a trusted proxy IP (`IsLoopback()` or `IsPrivate()`), it reads valid `X-Real-IP` (or rightmost valid `X-Forwarded-For` fallback), ignoring arbitrary client-supplied leftmost `X-Forwarded-For`, then stores it in `context.Context`.
  - Huma middleware registered **after** `NewHumaAuthMiddleware`:
    - `POST /api/v1/auth/register`: `rl:register:ip:<ip>` (`RATE_LIMIT_REGISTER_MAX / 15m`)
    - `POST /api/v1/auth/login`: IP ceiling `rl:login:ip:<ip>` (`60 / 1m`) in middleware + strict `rl:login:acct:<ip>:<sha256(lower(email))[:16]>` inside `AuthService.Login` before `bcrypt`
    - `POST /api/v1/orders` & `POST /api/v1/orders/{id}/cancel`: `rl:orders:user:<uuid>` (`RATE_LIMIT_ORDER_USER_MAX / 1m`) + `rl:orders:ip:<ip>` (`RATE_LIMIT_ORDER_IP_MAX / 1m`)
    - `POST /api/v1/demo/flash-sale`: `rl:demo:ip:<ip>` (`RATE_LIMIT_DEMO_FLASH_SALE_MAX / 1m`) on HTTP trigger only (internal goroutines call `OrderService.CreateOrder` directly).
  - Error mapping: `domain.ErrCodeRateLimited = "RATE_LIMITED"` (`429`) + `Retry-After` integer seconds header; `frontend/src/lib/api/errors.ts` maps `RATE_LIMITED` cleanly.

### 4. Short-TTL Event Catalog Cache (`GET /api/v1/events`, `GET /api/v1/events/{id}`)
- Optional `EventCatalogCache` interface (`internal/service/interfaces.go`), JSON payload in Redis keys `events:catalog:v1` and `events:detail:v1:<uuid>`, TTL `EVENT_CACHE_TTL` (`3s`), strict `fail-open` on error.
- Cache miss executes `ExpireDueOrders` + PostgreSQL query and populates cache best-effort.
- `GET /api/v1/organizer/events` and checkout flows remain uncached against PostgreSQL.
- Best-effort invalidation (`DEL`) on `CreateEvent`, `UpdateEvent`, `DeleteEvent`, `CreateOrder` (created only), `CancelOrder`, `ConfirmPayment`/`FailPayment` (quota transitions, returning affected `eventID` when transitioning), and `ExpirySweeper.RunOnce` (`ExpireDueOrders` returns touched distinct `eventID`s / count so both `events:catalog:v1` and each affected `events:detail:v1:<uuid>` invalidate immediately).

## Left to Builder
- Internal sub-file layout of `internal/redisstore`, in-memory mock implementation for unit tests, and exact Vite/Nginx port notes in `README.md` / `.env.example`.

## Verification Plan
- Unit tests (`go test -count=1 -race ./internal/... ./migrations/... ./cmd/...`) covering idempotency key validation, `429` + `Retry-After` middleware & login `email+IP` check, `fail-open` on nil/failing Redis, and cache hit/miss/invalidation.
- PostgreSQL integration tests (`go test -race -tags=integration ./tests/...`) including duplicate concurrent `Idempotency-Key` checkout replays.
- Edge & Compose checks: `nginx -t`, `docker compose config --quiet`, `docker compose -f docker-compose.yml -f docker-compose.db-debug.yml config --quiet`.
- Frontend & OpenAPI sync: `bun run openapi:generate`, `bun run lint`, `bun run typecheck`, `bun run test`, `bun run build`.
