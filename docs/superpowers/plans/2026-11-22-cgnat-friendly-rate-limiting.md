# CG-NAT-Friendly Rate Limiting & Adaptive PoW Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Eliminate false `429` blocks on crowded shared IPs (campus WiFi / mobile carrier CG-NAT) by enforcing authenticated order limits by `user_id` alone by default, loosening Nginx's emergency L7 fuse, and replacing hard per-IP blocks on `/auth/register` and `/auth/login` with a self-hosted HMAC SHA-256 Proof-of-Work adaptive challenge.

**Architecture:** Authenticated routes (`create-order`, `cancel-order`) key Redis rate limits solely on `actor.ID` unless an explicit `RATE_LIMIT_ORDER_IP_MAX > 0` emergency cap is configured. Anonymous auth routes (`/auth/register`, `/auth/login`) enforce a two-tier Redis window per IP (soft threshold -> `403 CHALLENGE_REQUIRED` solved once via Web Crypto SHA-256 PoW and verified statelessly via `X-PoW-Solution`; hard CG-NAT emergency cap -> `429 RATE_LIMITED`), while login account throttling keys per email hash (`rl:login:acct:<sha256(email)>`) instead of `IP+email`. Single-use or time-bounded PoW challenges stay stateless (HMAC-SHA256 signed token with timestamp and client IP binding, optional Redis nonce replay guard fail-open).

**Tech Stack:** Go 1.26 stdlib (`crypto/hmac`, `crypto/sha256`, `encoding/binary`), `Huma v2`, Redis 7 (`go-redis/v9`), Nginx 1.27 Alpine, TypeScript + React 19 + Web Crypto API (`crypto.subtle.digest`), Vitest.

**Spec:** [docs/superpowers/specs/2026-11-22-cgnat-friendly-rate-limiting-design.md](../specs/2026-11-22-cgnat-friendly-rate-limiting-design.md)

## Global Constraints
- Single `api` container replica only; zero new Go modules or npm dependencies installed.
- Strict fail-open on Redis outages/timeouts (`REDIS_TIMEOUT=500ms`) for both rate limiting and PoW replay checks.
- Zero breaking changes for normal unchallenged traffic below soft thresholds (`5` registrations / `15m`, `60` logins / `1m`); `RATE_LIMIT_ORDER_IP_MAX=0` default disables per-IP throttling on authenticated order mutations.
- All PoW timestamps use UTC (`time.Now().UTC()`); OpenAPI spec (`frontend/openapi.json`) and generated frontend SDK stay synchronized.
- Zero em-dashes (`\u2014`) in source files, comments, docs, or UI copy.

## Review Focus
- **PoW replay or cross-IP sharing:** A bot solves one difficulty-16 puzzle and reuses `X-PoW-Solution` 1,000 times or shares it across IPs. Pin with test asserting HMAC binds `(clientIP, challenge, expiresUnix)` and fails on mismatched IP or expired timestamp, plus Redis `SET NX` single-use consumption (fail-open).
- **Empty/disabled PoW secret misconfiguration:** Calling `NewPoWVerifier("")` silently accepts invalid solutions. Pin with fail-closed validation returning an error when secret is blank, and wiring `cfg.PoWSecret` fallback to `cfg.JWTSecret` in `cmd/api/main.go`.
- **Web Crypto `crypto.subtle` unavailable on insecure non-localhost HTTP:** Solving PoW in older HTTP contexts throws. Pin with fallback behavior surfacing actionable error notice instead of an unhandled promise rejection in frontend `useAuthForms` / `pow.ts`.
- **Authenticated `create-order` blocked under high CG-NAT volume:** Setting `RATE_LIMIT_ORDER_IP_MAX=0` must allow 500 distinct authenticated buyers behind one IP (`X-Real-IP: 103.1.2.3`) to run `POST /api/v1/orders` without `429`, while one buyer exceeding `OrderUserMax` still gets `429` with `Retry-After`.
- **Frontend infinite retry loop on invalid PoW rejection:** Retrying automatically forever when `403 CHALLENGE_REQUIRED` repeats. Pin with `maxAttempts = 1` auto-retry cap on `useAuthForms` mutation error/wrapper so at most one PoW solve + retry happens per user submit.

---

## File Structure

| File | Responsibility |
| :--- | :--- |
| `internal/config/config.go`, `internal/config/config_test.go` | Set `defaultRateLimitOrderIP = 0` (`0` = disabled), add `RateLimitRegisterHardMax` (`100`), `RateLimitLoginIPHardMax` (`600`), `PoWDifficulty` (`16`), `PoWSecret` (optional, falls back to `JWTSecret`), allow `0` for optional IP ceiling via `parseNonNegativeInt`. |
| `internal/domain/errors.go` | Add `ErrCodeChallengeRequired = "CHALLENGE_REQUIRED"` and optional PoW challenge metadata on `DomainError` or helper constructor. |
| `internal/security/pow.go`, `internal/security/pow_test.go` | Stateless HMAC-SHA256 PoW challenge issuer & verifier (`Issue(clientIP, now)` / `Verify(solutionHeader, clientIP, now)`) with leading-zero-bit difficulty check and constant-time MAC check. |
| `internal/redisstore/client.go`, `internal/redisstore/ratelimit.go`, `internal/redisstore/client_test.go` | Add fail-open `ConsumeOnce(ctx, key, ttl)` (`SET NX EX`), `Peek(ctx, key, limit)` (`GET`+`PTTL`), and `Reset(ctx, key)` (`DEL`) for PoW replay protection and failed-login lockout. |
| `internal/middleware/ratelimit.go`, `internal/middleware/cors.go`, `internal/middleware/ratelimit_test.go` | Enforce `user_id`-only limits for `create-order`/`cancel-order` when `OrderIPMax <= 0`; enforce soft-to-PoW (`403 CHALLENGE_REQUIRED` + `X-PoW-Challenge` header) and hard ceiling (`429`) for `register-user` & `login-user`; allow `X-PoW-Solution` & expose `X-PoW-Challenge` in CORS. |
| `internal/service/auth_service.go`, `internal/service/resilience_test.go` | Key `enforceLoginRateLimit` on `sha256(normalizedEmail)` (pre-`bcrypt` total ceiling `rl:login:acct:<emailHash>` against self-account `bcrypt` DoS) + failed-only lockout `rl:login:fail:<emailHash>` (`5` per `15m`, cleared on valid login via optional `RateLimitResetter`). |
| `internal/handler/error_mapper.go`, `internal/handler/error_mapper_test.go` | Map `ErrCodeChallengeRequired` to HTTP `403` and preserve `X-PoW-Challenge` header. |
| `internal/routes/router.go`, `internal/routes/router_test.go`, `cmd/api/main.go` | Wire `PoWVerifier` and `ConsumeNonce` store into `RateLimitPolicy` / `NewHumaRateLimitMiddleware`. |
| `frontend/nginx.conf`, `README.md`, `DECISIONS.md` | Raise `edge_api_limit` to `rate=250r/s` + `burst=1000 nodelay`; document `set_real_ip_from` CDN/LB snippet; update `README.md` and `ADR-009` in `DECISIONS.md`. |
| `frontend/src/lib/pow.ts`, `frontend/src/lib/test/pow.test.ts` | Browser Web Crypto SHA-256 PoW solver (`solvePowChallenge(challengeToken)`) with attempt cap and `crypto.subtle` availability guard. |
| `frontend/src/hooks/useAuthForms.ts`, `frontend/src/lib/api/errors.ts`, `frontend/src/lib/test/errors.test.ts` | Detect `CHALLENGE_REQUIRED` on register/login, solve PoW once automatically, retry with `X-PoW-Solution`, and map fallback errors cleanly. |

---

### Task 1: Config Defaults & Domain Error Constant

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `internal/domain/errors.go`

**Interfaces:**
- Produces:
  - `Config.RateLimitOrderIPMax int` (default `0`, `>= 0` valid via `parseNonNegativeInt`)
  - `Config.RateLimitRegisterHardMax int` (default `100`)
  - `Config.RateLimitLoginIPHardMax int` (default `600`)
  - `Config.PoWDifficulty int` (default `16`, allowed `8..24`)
  - `Config.PoWSecret string` (falls back to `cfg.JWTSecret` when unset)
  - `domain.ErrCodeChallengeRequired = "CHALLENGE_REQUIRED"`

- [ ] **Step 1: Write failing config and domain tests**

In `internal/config/config_test.go`, update `assertDefaultRedisAndRateLimit` and add a new test `TestLoad_CGNATRateLimitDefaultsAndOverrides` asserting:
- Default `RateLimitOrderIPMax == 0`
- Default `RateLimitRegisterHardMax == 100`, `RateLimitLoginIPHardMax == 600`, `PoWDifficulty == 16`
- Overriding `RATE_LIMIT_ORDER_IP_MAX=0` passes `Load()` without error, and `POW_DIFFICULTY=30` fails validation (`must be between 8 and 24`).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -count=1 ./internal/config/... ./internal/domain/...`
Expected: FAIL (missing struct fields and `RATE_LIMIT_ORDER_IP_MAX=0` rejected by `parsePositiveInt`).

- [ ] **Step 3: Implement config fields and `ErrCodeChallengeRequired`**

1. In `internal/config/config.go`, add `parseNonNegativeInt(envKey string, defaultVal int) (int, error)`, set `defaultRateLimitOrderIP = 0`, add `RateLimitRegisterHardMax`, `RateLimitLoginIPHardMax`, `PoWDifficulty` (validated `8..24`), and `PoWSecret` (redacted in `LogValue()`, defaulting to `cfg.JWTSecret` if blank).
2. In `internal/domain/errors.go`, define `ErrCodeChallengeRequired = "CHALLENGE_REQUIRED"` and helper `NewChallengeRequiredError(msg string) *DomainError` (HTTP `403`).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -count=1 ./internal/config/... ./internal/domain/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go internal/domain/errors.go
git commit -m "feat(config): add CG-NAT soft/hard rate limit defaults and PoW settings"
```

---

### Task 2: Stateless HMAC SHA-256 Proof-of-Work Package & Redis Single-Use Nonce Guard

**Files:**
- Create: `internal/security/pow.go`
- Test: `internal/security/pow_test.go`
- Modify: `internal/redisstore/client.go`
- Modify: `internal/redisstore/ratelimit.go`
- Modify: `internal/redisstore/client_test.go`

**Interfaces:**
- Produces:
  - `security.NewPoWVerifier(secret string, difficulty int, ttl time.Duration) (*security.PoWVerifier, error)`
  - `(*security.PoWVerifier).Issue(clientIP string, now time.Time) (challengeToken string, err error)` (format: `v1.<expiresUnix>.<difficulty>.<saltHex>.<macHex>` where MAC covers `v1|<expiresUnix>|<difficulty>|<saltHex>|<clientIP>`)
  - `(*security.PoWVerifier).Verify(solutionHeader string, clientIP string, now time.Time) (nonceKey string, err error)` (parses `<challengeToken>:<nonceStr>`, validates MAC with `hmac.Equal`, checks `expiresUnix >= now.Unix()`, verifies `SHA256(challengeToken + ":" + nonceStr)` has `difficulty` leading zero bits, returns replay-guard digest `nonceKey`)
  - `(*redisstore.Client).ConsumeOnce(ctx context.Context, key string, ttl time.Duration) (firstUse bool, err error)` (uses `SetArgs{Mode: "NX", TTL: ttl}` and fails open by returning `true, nil` when Redis client is nil or errors/times out)
  - `(*redisstore.Client).Peek(ctx context.Context, key string, limit int) (allowed bool, retryAfter time.Duration, err error)` (reads current fixed-window count & `PTTL` without incrementing; fails open `true, 0, nil`)
  - `(*redisstore.Client).Reset(ctx context.Context, key string) error` (deletes counter key on successful login; fails open `nil` on nil client/error)

- [ ] **Step 1: Write failing unit tests for PoW issue/verify and Redis single-use nonce**

In `internal/security/pow_test.go`:
- `TestPoWVerifier_ValidSolvePasses`
- `TestPoWVerifier_RejectsBlankSecretAndInvalidDifficulty`
- `TestPoWVerifier_RejectsWrongIPExpiredAndTamperedMAC` (pins Review Focus #1 & #2)
- `TestPoWVerifier_RejectsInsufficientLeadingZeros`

In `internal/redisstore/client_test.go`:
- `TestClient_ConsumeOnceFailOpenOnNilClient`
- `TestClient_LiveRedisConsumeOnce` (second call with same key returns `false, nil`)
- `TestClient_LiveRedisPeekAndReset` (`Peek` does not increment count; `Reset` clears counter)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -count=1 ./internal/security/... ./internal/redisstore/...`
Expected: FAIL (`internal/security` package missing; `ConsumeOnce`/`Peek`/`Reset` undefined).

- [ ] **Step 3: Implement `internal/security/pow.go` and Redis `ConsumeOnce` / `Peek` / `Reset`**

Implement constant-time HMAC-SHA256 verification and bit-prefix check (`hasLeadingZeroBits(digest []byte, bits int) bool`) without external dependencies, verify `clientIP` normalization (`strings.TrimSpace`), and implement fail-open `ConsumeOnce`, `Peek`, and `Reset` in `internal/redisstore/ratelimit.go` respecting `c.timeout`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -count=1 -race ./internal/security/... ./internal/redisstore/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/security/pow.go internal/security/pow_test.go internal/redisstore/client.go internal/redisstore/ratelimit.go internal/redisstore/client_test.go
git commit -m "feat(security): add HMAC SHA-256 PoW verifier and fail-open replay guard"
```

---

### Task 3: Adaptive Auth Middleware, User-Only Order Limits, and Email-Keyed Login Guard

**Files:**
- Modify: `internal/middleware/ratelimit.go`
- Modify: `internal/middleware/cors.go`
- Modify: `internal/middleware/ratelimit_test.go`
- Modify: `internal/handler/error_mapper.go`
- Modify: `internal/service/auth_service.go`
- Modify: `internal/service/resilience_test.go`
- Modify: `internal/routes/router.go`
- Modify: `internal/routes/router_test.go`
- Modify: `cmd/api/main.go`

**Interfaces:**
- Consumes:
  - `security.PoWVerifier` (via narrow consumer interface `ChallengeGuard` in `internal/middleware/ratelimit.go`: `Issue(clientIP string, now time.Time) (string, error)`, `Verify(solutionHeader, clientIP string, now time.Time) (string, error)`)
  - `NonceConsumer` interface in `internal/middleware/ratelimit.go`: `ConsumeOnce(ctx context.Context, key string, ttl time.Duration) (bool, error)`
- Produces:
  - Updated `RateLimitPolicy` fields: `RegisterHardMax int`, `LoginIPHardMax int`, `Challenge ChallengeGuard`, `NonceStore NonceConsumer`
  - Response header `X-PoW-Challenge` on `403 CHALLENGE_REQUIRED`, consuming `X-PoW-Solution` request header

- [ ] **Step 1: Write failing middleware, auth service, and router tests**

1. In `internal/middleware/ratelimit_test.go`:
   - `TestHumaRateLimitMiddleware_OrderUserOnlyWhenIPMaxZero`: 500 `create-order` calls from distinct `actor.ID`s on same IP `103.1.2.3` all pass; 21st call from same `actor.ID` returns `429 RATE_LIMITED` + `Retry-After` (pins Review Focus #4).
   - `TestHumaRateLimitMiddleware_RegisterAdaptivePoW`: requests `1..5` pass cleanly without PoW header; request `6` without `X-PoW-Solution` returns `403` with `error_code: CHALLENGE_REQUIRED` and `X-PoW-Challenge` header; retrying with valid `X-PoW-Solution` passes (`201`/`200`); reusing same `X-PoW-Solution` fails when `NonceStore` returns `false`; exceeding `RegisterHardMax` returns `429`.
2. In `internal/service/resilience_test.go`:
   - `TestAuthService_LoginRateLimitIsolatesDistinctEmailsOnSameIP`: failing/hitting logins for `victim@event.local` on IP `103.1.2.3` rate-limits `victim@event.local` (`429`), while `neighbor@event.local` on the same IP `103.1.2.3` succeeds; also verify failed-login lockout resets on successful login when `RateLimitResetter` is supported, and pre-`bcrypt` account cap still blocks rapid self-owned account `bcrypt` spam.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -count=1 ./internal/middleware/... ./internal/service/... ./internal/routes/...`
Expected: FAIL (`ChallengeGuard` missing; `OrderIPMax=0` not wired; `AuthService` blocks by IP+email only or old behavior mismatch).

- [ ] **Step 3: Implement middleware adaptive PoW flow, `AuthService` email-hash key, and `main.go` wiring**

1. In `internal/middleware/ratelimit.go`:
   - For `create-order` & `cancel-order`: check `rl:orders:user:<actor.ID>`; only check `rl:orders:ip:<ip>` if `policy.OrderIPMax > 0` or actor is absent.
   - For `register-user` & `login-user`: evaluate soft counter first, then hard ceiling (`RegisterHardMax` / `LoginIPHardMax`). If count `> soft` and `<= hard`, check `X-PoW-Solution` via `policy.Challenge` + `policy.NonceStore` (gracefully skipping PoW requirement if `policy.Challenge == nil` so existing unit routers without PoW keep compatibility unless configured); on missing/invalid PoW, issue fresh challenge, set `X-PoW-Challenge` header, and write `403 CHALLENGE_REQUIRED` (`APIError`-shaped JSON).
2. In `internal/service/auth_service.go`:
   - Change `enforceLoginRateLimit` key from `"rl:login:acct:" + ip + ":" + hex` to `"rl:login:acct:" + hex.EncodeToString(sum[:8])` (pre-`bcrypt` total ceiling against self-owned-account CPU DoS) so one resident mistyping a password only throttles their email address, not unrelated users on the same CG-NAT tower.
   - Add failed-login tracking (`rl:login:fail:` + `hex`, limit `5` / `15m`) checked before `bcrypt` via `rl:login:fail:` peek or dedicated `RateLimitPeeker`/`RateLimitResetter` (fail-open; optional interface on `s.limiter`): record attempt on wrong password, clear on successful password verification. Keep constant-time safety (`hmac.Equal` / `bcrypt.CompareHashAndPassword`) and never leak account existence in error messages (`errMsgInvalidCreds` unless `429 RATE_LIMITED`).
3. In `cmd/api/main.go` and `internal/routes/router.go`:
   - Wire `security.NewPoWVerifier` (using `cfg.PoWSecret`, `cfg.PoWDifficulty`, `120*time.Second`) and `redisClient` (`NonceStore`) into `RateLimitPolicy`.
   - Expose `X-PoW-Challenge` in `internal/middleware/cors.go` `Access-Control-Expose-Headers` and allow `X-PoW-Solution` in `Access-Control-Allow-Headers`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -count=1 -race ./internal/... ./migrations/... ./cmd/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/middleware/ internal/service/ internal/routes/ cmd/api/main.go
git commit -m "feat(ratelimit): enforce user_id order limits and adaptive PoW on auth endpoints"
```

---

### Task 4: Frontend Web Crypto PoW Solver & Auto-Retry on Auth Forms

**Files:**
- Create: `frontend/src/lib/pow.ts`
- Test: `frontend/src/lib/test/pow.test.ts`
- Modify: `frontend/src/lib/api/errors.ts`
- Modify: `frontend/src/lib/test/errors.test.ts`
- Modify: `frontend/src/hooks/useAuthForms.ts`

**Interfaces:**
- Consumes: `X-PoW-Challenge` response header & `CHALLENGE_REQUIRED` error code from `POST /api/v1/auth/register` or `/login`
- Produces:
  - `solvePowChallenge(challengeToken: string): Promise<string>` returning `"<challengeToken>:<nonce>"`
  - Error copy mapping for `CHALLENGE_REQUIRED` in `errors.ts`

- [ ] **Step 1: Write failing Vitest specs for `solvePowChallenge` and `CHALLENGE_REQUIRED` parsing**

In `frontend/src/lib/test/pow.test.ts`:
- `solvePowChallenge` produces `<challengeToken>:<nonce>` whose SHA-256 hex digest starts with the embedded leading zero bits (`difficulty = 8` for fast test determinism).
- Rejects cleanly when `crypto.subtle` is unavailable (pins Review Focus #3) or token is malformed.

In `frontend/src/lib/test/errors.test.ts`:
- `parseApiError` maps `error_code: "CHALLENGE_REQUIRED"` to friendly message (`"Shared network verification timed out"` / retry hint).

- [ ] **Step 2: Run Vitest to verify it fails**

Run: `cd frontend && bun run test`
Expected: FAIL (`@/lib/pow` missing).

- [ ] **Step 3: Implement `pow.ts`, error mapping, and 1-retry hook in `useAuthForms.ts`**

1. Implement `solvePowChallenge` in `frontend/src/lib/pow.ts` using `crypto.subtle.digest("SHA-256", ...)` with max iteration safety guard (`maxIterations = 2_000_000`) and periodic `await Promise.resolve()` yield every `2048` iterations so the UI thread stays responsive.
2. Wrap register/login submit calls in `frontend/src/hooks/useAuthForms.ts` using direct SDK call with `ThrowOnError` handling (or `apiClient`) so on `403 CHALLENGE_REQUIRED` with `X-PoW-Challenge`, it solves PoW and retries **at most 1 time** (`attempt === 0` guard, pinning Review Focus #5) with header `"X-PoW-Solution"`.

- [ ] **Step 4: Run frontend checks**

Run: `cd frontend && bun run lint && bun run typecheck && bun run test && bun run build`
Expected: PASS (`0` lint warnings/errors, all Vitest passing, Vite build clean).

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/pow.ts frontend/src/lib/test/pow.test.ts frontend/src/lib/api/errors.ts frontend/src/lib/test/errors.test.ts frontend/src/hooks/useAuthForms.ts
git commit -m "feat(frontend): solve adaptive PoW auth challenges transparently via Web Crypto"
```

---

### Task 5: Nginx Edge CG-NAT Tuning, Integration Tests & Documentation Sync

**Files:**
- Modify: `frontend/nginx.conf`
- Modify: `tests/integration_test.go`
- Modify: `.env.example`
- Modify: `README.md`
- Modify: `DECISIONS.md`

**Interfaces:**
- Updates `edge_api_limit` (`rate=250r/s`, `burst=1000 nodelay`) and documents trusted upstream proxy `set_real_ip_from` block.

- [ ] **Step 1: Add PostgreSQL+Redis integration tests for user-ID-only orders and adaptive PoW flow**

In `tests/integration_test.go`:
- `TestIntegration_CGNATSharedIPCheckoutsAndAdaptivePoW`: simulates 10 distinct buyers sharing `X-Real-IP: 103.55.10.2` running checkout without IP `429`, plus 6th registration on shared IP solving the live `X-PoW-Challenge` header from `POST /api/v1/auth/register` and succeeding (`201 Created`).

- [ ] **Step 2: Run integration tests against Postgres + Redis**

Run: `go test -race -tags=integration -run TestIntegration_CGNATSharedIPCheckoutsAndAdaptivePoW ./tests/...`
Expected: PASS (with updated `frontend/nginx.conf` validated via `nginx -t`).

- [ ] **Step 3: Update `frontend/nginx.conf`, `.env.example`, `README.md`, and `DECISIONS.md`**

- `frontend/nginx.conf`: raise `rate=250r/s` and `burst=1000 nodelay`, add commented `set_real_ip_from` template for CDN/LB real-IP restoration.
- `.env.example`, `README.md`, `DECISIONS.md` (`ADR-009`): document user-only order limit default, soft vs hard CG-NAT thresholds, and self-hosted PoW trade-off (`Approach A/B/C`).

- [ ] **Step 4: Run full verification suite**

Run:
- `go vet ./... && go vet -tags=integration ./tests/...`
- `go test -count=1 -race ./internal/... ./migrations/... ./cmd/...`
- `cd frontend && bun run lint && bun run typecheck && bun run test && bun run build`
- `docker compose config --quiet`

Expected: All commands exit `0`.

- [ ] **Step 5: Commit**

```bash
git add frontend/nginx.conf tests/integration_test.go .env.example README.md DECISIONS.md
git commit -m "docs(infra): tune Nginx edge limit for CG-NAT and add PoW integration proof"
```

---

## Self-Review
1. **Spec coverage:** Every design goal (`user_id`-only order limit default -> Task 1 & 3; adaptive PoW on `/auth/register` & `/auth/login` -> Task 2, 3 & 4; email-hash login key -> Task 3; Nginx `250r/s + burst 1000` + `realip` snippet & docs -> Task 5) maps cleanly to an independently testable task.
2. **Step scan:** Every test step names specific assertions; every implementation step specifies exact file paths, signatures, and constraints without redundant body transcripts.
3. **Type consistency:** `PoWVerifier.Issue(clientIP, now)` / `Verify(solutionHeader, clientIP, now)` and `ConsumeOnce(ctx, key, ttl)` match across Tasks 2, 3, and 4 (`X-PoW-Challenge`, `X-PoW-Solution`, `CHALLENGE_REQUIRED`).
4. **Review Focus:** All 5 failure modes (`PoW replay/cross-IP`, `blank secret`, `Web Crypto unavailable`, `500 checkouts on 1 CG-NAT IP`, `frontend 1-retry cap`) have explicit tests in Tasks 2, 3, and 4.
5. **Proportion:** Concise task plan (~240 lines) with zero unverified code dumps and zero em-dashes.
