# Design Spec: CG-NAT-Friendly User-Centric Rate Limiting & Adaptive Proof-of-Work Challenge

Date: 2026-11-22
Product: Treean (`github.com/Torikh42/treean`)
Target Branch: `feat/cgnat-rate-limit-adaptive-pow` (from `main`)

## Intent & Why
Shared public IPv4 networks (campus WiFi, corporate NAT, or rural mobile carrier CG-NAT towers where hundreds of residents share one public IP) hit false `429 RATE_LIMITED` blocks under strict per-IP ceilings:
- `POST /api/v1/auth/register` currently hard-blocks an entire shared IP after `5` registrations per `15m`.
- `POST /api/v1/orders` and `POST /api/v1/orders/{id}/cancel` check `rl:orders:ip` (`300/min`) even after `actor.ID` (`user_id`) is already authenticated.
- Nginx `edge_api_limit` (`50r/s`, `burst=250`) can reject legitimate high-density CG-NAT bursts before Go parses the JWT `user_id`.

This design removes shared-IP penalties from authenticated user flows and replaces brittle IP hard-blocks on anonymous auth endpoints with a zero-dependency adaptive Proof-of-Work (PoW) challenge.

## Goals, Non-Goals & Anti-Goals
- **Goals:**
  1. Enforce rate limits on authenticated order actions (`create-order`, `cancel-order`) purely by authenticated `user_id` (`actor.ID`) by default, keeping an opt-in IP safety cap (`RATE_LIMIT_ORDER_IP_MAX=0` default = disabled).
  2. Replace the strict 5-request hard IP block on `/auth/register` and `/auth/login` with a **two-tier adaptive policy**: zero friction below the soft threshold, then an automatic SHA-256 Proof-of-Work (`CHALLENGE_REQUIRED`) challenge between the soft threshold and a high emergency CG-NAT ceiling.
  3. Key account-level brute-force throttling in `AuthService.Login` primarily on normalized email (`sha256(email)`) instead of `IP+email` with two guards: a pre-`bcrypt` per-account total ceiling (`rl:login:acct:<emailHash>`) to stop bots burning `bcrypt` CPU with self-owned accounts, plus a failed-attempt lockout (`rl:login:fail:<emailHash>`, reset on valid login when the limiter supports `Reset`) inspired by `migadu/alps`. That way one wrong password attempt from User A never locks out User B's different email on the same CG-NAT IP, and legitimate colleagues signing in behind one shared IP never trigger an IP lockout.
  4. Raise Nginx's emergency L7 flood shield (`250r/s`, `burst=1000 nodelay`) and add trusted reverse-proxy `realip` configuration scaffolding for CDN/LB deployments.
  5. Keep frontend registration/login seamless: automatically detect `403 CHALLENGE_REQUIRED`, solve the Web Crypto SHA-256 puzzle (`~50-250ms` in browser), and retry once without user friction.
- **Non-Goals:**
  - Do NOT add external CAPTCHA SaaS dependencies (Turnstile, reCAPTCHA, hCaptcha) that require internet access or third-party API keys, preserving 1-command offline `docker compose up --build`.
  - Do NOT add new Go or npm third-party packages; use Go stdlib `crypto/hmac` + `crypto/sha256` and Web Crypto `crypto.subtle.digest`.
  - Do NOT alter PostgreSQL quota locking, `pg_advisory_xact_lock`, or `ExpirySweeper`.
- **Anti-Goals:**
  - Removing all anonymous endpoint throttling (would open Sybil bot registration + `bcrypt` CPU DoS).
  - Allowing unverified `Authorization: Bearer foo` strings at Nginx to bypass flood protection without signature verification.

## Constraints
- Single `api` container replica (`docker-compose.yml`).
- Fail-open Redis behavior preserved (`REDIS_TIMEOUT=500ms`, errors never block legitimate buyers).
- Existing API callers / `request.http` / `200`-concurrent tests under normal rates MUST continue working with zero PoW header required until the IP exceeds the soft threshold.
- Zero em-dashes in docs, comments, or UI copy.

## Approaches Considered
1. **Approach A: Remove IP Limits Everywhere (Pure `user_id` Only)**
   - Simple removal of `rl:*:ip` keys and Nginx `limit_req`.
   - *Rejected:* `/auth/register`, `/auth/login`, and `GET /events` have no `user_id`. Bots can create 10,000 fake accounts to bypass per-`user_id` checkout limits or exhaust CPU with parallel `bcrypt` calls.
2. **Approach B: External CAPTCHA (Cloudflare Turnstile / hCaptcha) on Register & Login**
   - Strong anti-bot protection when online behind Cloudflare.
   - *Rejected:* Breaks 1-command offline Docker grading (`final-project.md`), adds external SaaS credentials, and adds third-party frontend bundle scripts.
3. **Approach C: User-ID Primary + Adaptive Self-Hosted HMAC SHA-256 PoW Challenge (Chosen)**
   - **Authenticated order endpoints:** `user_id`-only by default (`RATE_LIMIT_ORDER_IP_MAX=0` default, opt-in `> 0` emergency override), plus PostgreSQL's 4-ticket/buyer cap as the business invariant.
   - **Anonymous auth endpoints:** Soft IP threshold (`5/15m` register, `60/1m` login) -> above soft threshold triggers signed HMAC SHA-256 PoW challenge (`GET /api/v1/auth/challenge` or challenge payload in `403 CHALLENGE_REQUIRED` error details) up to a high hard CG-NAT ceiling (`100/15m` register, `600/1m` login).
   - **Trade-off:** Legitimate humans on crowded CG-NAT pay a single ~100ms browser hash calculation when registering/logging in; automated curl scripts past the soft limit must solve PoW; zero external dependencies or SaaS keys required.

## Decisions Left to Implementation Plan
- Exact header / error extension contract for `403 CHALLENGE_REQUIRED` (`X-PoW-Solution` request header, challenge TTL `120s`, default difficulty `16` leading zero bits = ~65,536 hashes average = <150ms in browser JS Web Crypto, <1ms verification in Go).
- Reuse `JWT_SECRET` (or optional `POW_SECRET` fallback to `JWT_SECRET`) for HMAC-signing PoW challenges so zero new required env vars are introduced.
