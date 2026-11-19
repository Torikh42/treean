# Design Spec: Production Web App RBAC, Auth Pages, & Organizer Event CRUD

Date: 2026-11-19
Branch: `feat/web-app-rbac-crud`
Product: Treean (`github.com/torikh42/treean`)

## Goal
Transform Treean from a demo-header testing console into a production-ready web application with dedicated authentication pages (`/login`, `/register`), strict role-based navigation and route guards (`BUYER` vs `ORGANIZER`), complete Organizer Event CRUD (list, create, update, safe delete), and an interactive `/flash-sale` lab without the top-bar Demo Header Auth drawer.

## Design & Architecture Decisions
1. **Authentication Mode (JWT-First Production Session):**
   - Remove the top-bar `Demo Header Auth` / identity switcher button and `AuthPanel` drawer from `AppShell`.
   - Default `useAuthStore` to pure JWT mode (`accessToken` + `user`). Remove `demoUserId` / `demoRole` client-side switching from active web UX.
   - Add `GET /api/v1/auth/me` (protected via JWT) so browser reloads can validate the persisted JWT against PostgreSQL and clear expired/deleted sessions cleanly.
2. **Backend API Additions & Concurrency/FK Invariants:**
   - `GET /api/v1/auth/me`: returns `*domain.User` for `actor.ID`. Returns `401 UNAUTHORIZED` if token invalid/missing or user deleted.
   - `GET /api/v1/organizer/events`: requires `RoleOrganizer`, runs `ExpireDueOrders` lazy sweep, and returns all events owned by `actor.ID` sorted by `event_date ASC, created_at ASC`.
   - `PUT /api/v1/events/{id}`: requires `RoleOrganizer`, verifies `ev.OrganizerID == actor.ID` (`403 FORBIDDEN` otherwise, `404 EVENT_NOT_FOUND` if missing). Atomically inside a transaction (`SELECT ... FOR UPDATE` on `events` and `ticket_types`):
     - Updates event `title`, `description`, `venue`, `event_date`.
     - Upserts ticket tiers (`2..10` distinct case-insensitive names):
       - Existing tier (`id` present and belongs to event): updates `name`, `price_idr`, and `total_quota` **only if `newTotalQuota >= (oldTotalQuota - oldRemainingQuota)`** (`409 Conflict` if `total_quota` shrinks below already-held/sold tickets), adjusting `remaining_quota = newTotalQuota - soldOrHeld` atomically so `remaining_quota >= 0 AND remaining_quota <= total_quota` always holds.
       - New tier (`id` omitted): inserted with `remaining_quota = total_quota`.
       - Removed tier (`id` omitted from payload): deleted **only if no `order_items` reference it** (`409 CONFLICT` / `TIER_HAS_ORDERS` if referenced).
   - `DELETE /api/v1/events/{id}`: requires `RoleOrganizer`, verifies `ev.OrganizerID == actor.ID` (`403 FORBIDDEN` otherwise). Checks `SELECT EXISTS(SELECT 1 FROM orders WHERE event_id = $1)` inside tx (`FOR UPDATE` lock on event row): if orders exist, returns `409 Conflict` (`EVENT_HAS_ORDERS`); otherwise deletes the event (`ticket_types` cascades via `ON DELETE CASCADE`) and returns `204 No Content`.
   - Keep `POST /api/v1/demo/flash-sale` and `POST /api/v1/webhooks/payment` unchanged so `/flash-sale` continues working seamlessly.
3. **Frontend Routes & Role Guards (`beforeLoad`):**
   - **Public Routes:**
     - `/` (Homepage / Live Box Office): public event discovery catalog, search/filter by title or venue, clear login/register or role CTA.
     - `/events/$eventId` (Event Detail & Checkout): public event & live tier view; checkout mutation is restricted to authenticated `BUYER` users (unauthenticated users see `Sign in as Buyer to Reserve` CTA redirecting to `/login?redirect=/events/$eventId`; `ORGANIZER` users see an informative notice that Organizer accounts cannot purchase buyer tickets).
     - `/flash-sale` (Flash Sale & Webhook Lab): preserved per user request for verifying 200-concurrent row-lock serialization and SHA-512 webhook idempotency.
     - `/forbidden` (`403 Forbidden`): dedicated RBAC error page with contextual back link.
   - **Guest-Only Routes:**
     - `/login`: real email + password form (`useForm` + `zod`), quick-fill preset hints shown only when desired or standard email/password form, optional `redirect` query param returning user to intended page, redirects authenticated `BUYER` -> `/` (or `redirect`) and `ORGANIZER` -> `/organizer`.
     - `/register`: full account registration (`name`, `email`, `password`, role selector `BUYER` | `ORGANIZER`).
   - **Buyer-Only Routes (`beforeLoad` checks `isAuthenticated` -> `/login`, `role === "BUYER"` -> `/forbidden`):**
     - `/orders` (My Orders & Tickets)
     - `/orders/$orderId` (Order Detail, 15m Hold Timer, Pay/Cancel, Issued Tickets)
   - **Organizer-Only Routes (`beforeLoad` checks `isAuthenticated` -> `/login`, `role === "ORGANIZER"` -> `/forbidden`):**
     - `/organizer` (Organizer Console): displays Organizer's own events (`GET /api/v1/organizer/events`) alongside Create Event & Edit Event forms and Delete Event action with explicit conflict/guardrail messaging (`EVENT_HAS_ORDERS`).
4. **UI & Antislop Compliance (`DESIGN.md`):**
   - Preserve Signal Yellow Web-Brutalism (`#F6F3EB` canvas, `#FFFFFF` card, `#111111` 2px/3px borders & zero-blur hard shadows, `#FFE500` primary CTA & active tab accent, `Space Grotesk Variable` + `IBM Plex Mono`).
   - Dynamic role-based navbar links in `AppShell`:
     - Common: `Events` (`/`), `Flash Sale Lab` (`/flash-sale`)
     - Guest: `Login` (`/login`), `Register` (`/register`)
     - Buyer: `My Orders` (`/orders`), user badge (`Name · BUYER`), `Logout`
     - Organizer: `Organizer` (`/organizer`), user badge (`Name · ORGANIZER`), `Logout`
   - Remove `Demo Accounts` hardcoded switcher button in top header (`AuthPanel` removed completely).

## Verification Plan
- Go: `gofmt -s -l .`, `go build ./...`, `go vet ./...`, `go vet -tags=integration ./tests/...`, `go test -count=1 -race ./internal/... ./migrations/... ./cmd/...`.
- OpenAPI sync: regenerate `frontend/openapi.json` from Huma router and run `bun run openapi:generate`.
- Frontend: `bun run lint`, `bun run typecheck` (`tsc -b --noEmit`), `bun run test` (Vitest unit tests including auth store, RBAC guards, route redirects, and organizer CRUD helpers), `bun run build`.
- Config & Compose: `docker compose config --quiet`.
