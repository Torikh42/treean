# Production Web App RBAC, Auth Pages, & Organizer CRUD Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `subagent-driven-development` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver real `/login` and `/register` pages, role-guarded Buyer (`/orders*`) and Organizer (`/organizer`) routes, complete Organizer Event CRUD (`GET /api/v1/organizer/events`, `PUT /api/v1/events/{id}`, `DELETE /api/v1/events/{id}`, `GET /api/v1/auth/me`), remove the header auth switcher button, and keep `/flash-sale` intact.

**Architecture:** Clean layered Go backend (`domain` -> `repository` -> `service` -> `handler` Huma v2) regenerating `frontend/openapi.json` + `@hey-api/openapi-ts` client/TanStack Query hooks, paired with TanStack Router `beforeLoad` RBAC route guards and Signal Yellow Web-Brutalist UI components.

**Tech Stack:** Go 1.26, pgx/v5, Huma v2, React 19, Vite, Bun, TanStack Router/Query/Form, `@hey-api/openapi-ts`, Zustand, Tailwind v4, Vitest.

---

### Task 1: Backend `GET /api/v1/auth/me` & Organizer Event CRUD (`ListByOrganizer`, `UpdateWithTiers`, `DeleteIfUnused`)

**Files:**
- Modify: `internal/domain/errors.go`
- Modify: `internal/service/interfaces.go`
- Modify: `internal/service/auth_service.go`
- Modify: `internal/service/event_service.go`
- Modify: `internal/repository/event_repo.go`
- Modify: `internal/handler/auth_handler.go`
- Modify: `internal/handler/event_handler.go`
- Modify: `internal/handler/router_test.go`
- Modify: `internal/service/service_test.go`
- Modify: `tests/integration_test.go` (if compile/interface mocks exist across packages)

- [ ] **Step 1: Add domain error codes (`EVENT_HAS_ORDERS`, `TIER_HAS_ORDERS`, `QUOTA_CONFLICT`) and repository/service method signatures:**
  - `EventRepository`:
    - `ListByOrganizer(ctx context.Context, organizerID uuid.UUID) ([]domain.Event, error)`
    - `UpdateWithTiers(ctx context.Context, organizerID uuid.UUID, ev *domain.Event) error`
    - `DeleteUnused(ctx context.Context, organizerID, eventID uuid.UUID) error`
  - `AuthService.GetProfile(ctx context.Context, userID uuid.UUID) (*domain.User, error)`
  - `EventService`:
    - `ListOrganizerEvents(ctx context.Context, actor domain.Actor) ([]domain.Event, error)`
    - `UpdateEvent(ctx context.Context, actor domain.Actor, eventID uuid.UUID, in UpdateEventInput) (*domain.Event, error)`
    - `DeleteEvent(ctx context.Context, actor domain.Actor, eventID uuid.UUID) error`
- [ ] **Step 2: Implement PostgreSQL `EventRepo` methods with `FOR UPDATE` locking and FK-safe 409 mapping:**
  - `ListByOrganizer`: `RepeatableRead`, `ReadOnly` tx filtering `WHERE organizer_id = $1`, fetching tiers in batch.
  - `UpdateWithTiers`: `Begin(ctx)` -> lock event row (`SELECT organizer_id FROM events WHERE id = $1 FOR UPDATE`) -> check not found (`404 EVENT_NOT_FOUND`) -> check ownership (`ev.OrganizerID != organizerID` -> `403 FORBIDDEN`) -> lock existing tiers (`FOR UPDATE`) -> update event columns -> validate tier updates (`newTotalQuota >= soldOrHeld`, else `409 Conflict` with `ErrCodeQuotaConflict`) -> delete removed tiers only if `NOT EXISTS(SELECT 1 FROM order_items WHERE ticket_type_id = $1)` (else `409 TIER_HAS_ORDERS`) -> insert new tiers -> re-read updated event + tiers inside tx and commit.
  - `DeleteUnused`: `Begin(ctx)` -> lock event row (`FOR UPDATE`) -> check existence (`404`) and ownership (`403`) -> check `SELECT EXISTS(SELECT 1 FROM orders WHERE event_id = $1)` (if true, return `409 EVENT_HAS_ORDERS`) -> `DELETE FROM events WHERE id = $1`.
- [ ] **Step 3: Register Huma endpoints & write tests:**
  - `GET /api/v1/auth/me` (`Security: bearerSecurity`, OperationID `get-me`)
  - `GET /api/v1/organizer/events` (`Security: bearerSecurity`, OperationID `list-organizer-events`)
  - `PUT /api/v1/events/{id}` (`Security: bearerSecurity`, OperationID `update-event`)
  - `DELETE /api/v1/events/{id}` (`Security: bearerSecurity`, `DefaultStatus: 204`, OperationID `delete-event`)
  - Update in-memory test doubles & add tests for ownership 403, missing 404, quota shrink conflict 409, delete-with-orders conflict 409, delete-unused 204, `get-me` 200/401.
- [ ] **Step 4: Run Go verification:**
  - `gofmt -s -l .`
  - `go test -count=1 -race ./internal/... ./migrations/... ./cmd/...`
  - `go vet -tags=integration ./tests/...`

---

### Task 2: Sync `frontend/openapi.json`, Auth Store Refactor (Remove Header Auth), RBAC Route Guards, & `/login`, `/register`, `/forbidden`

**Files:**
- Modify: `frontend/openapi.json` (regenerated from Go Huma OpenAPI spec)
- Regenerate: `frontend/src/lib/api/generated/*` via `bun run openapi:generate`
- Modify: `frontend/src/store/auth-store.ts`
- Modify: `frontend/src/lib/api/api-client.ts`
- Modify: `frontend/src/lib/api/errors.ts`
- Create: `frontend/src/lib/auth-guards.ts`
- Create: `frontend/src/hooks/useAuthForms.ts`
- Create: `frontend/src/hooks/useSessionSync.ts`
- Create: `frontend/src/components/auth/LoginView.tsx`
- Create: `frontend/src/components/auth/RegisterView.tsx`
- Create: `frontend/src/components/auth/ForbiddenView.tsx`
- Create: `frontend/src/routes/login.tsx`
- Create: `frontend/src/routes/register.tsx`
- Create: `frontend/src/routes/forbidden.tsx`
- Delete: `frontend/src/components/auth/AuthPanel.tsx`, `frontend/src/hooks/useAuthPanel.ts`, `frontend/src/lib/demo-accounts.ts`
- Modify: `frontend/src/components/layout/AppShell.tsx`
- Modify: `frontend/src/routes/__root.tsx`
- Modify: `frontend/src/lib/test/auth-store.test.ts` (add RBAC guard unit tests)

- [ ] **Step 1: Export exact `openapi.json` from Go router and run `bun run openapi:generate` in `frontend/`.**
- [ ] **Step 2: Simplify `useAuthStore` to JWT production session (`user`, `accessToken`, `setJwtSession`, `updateUser`, `clearSession`, `getActiveRole`, `isAuthenticated`). Update `api-client.ts` to attach `Authorization: Bearer <token>` and remove demo header logic + clear stale session cleanly on invalid `getMe` check.**
- [ ] **Step 3: Build RBAC guard helpers (`requireGuest`, `requireRole("BUYER" | "ORGANIZER")`) redirecting unauthenticated callers to `/login?redirect=...` and wrong-role callers to `/forbidden`.**
- [ ] **Step 4: Implement `/login`, `/register`, `/forbidden` in Signal Yellow Neo-Brutalist style (`DESIGN.md`) and remove the top-right Header Auth button from `AppShell.tsx`, replacing it with role-aware navigation (`Events`, `Flash Sale Lab`, `My Orders` for `BUYER`, `Organizer` for `ORGANIZER`, `Login` / `Register` for Guest, active user badge + `Logout` when signed in).**
- [ ] **Step 5: Run frontend verification (`bun run lint`, `bun run typecheck`, `bun run test`).**

---

### Task 3: Role-Protected Routes, Organizer Event CRUD Console, Buyer Gated Checkout, & Final Verification

**Files:**
- Modify: `frontend/src/routes/orders.index.tsx` (`beforeLoad: () => requireRole("BUYER")`)
- Modify: `frontend/src/routes/orders.$orderId.tsx` (`beforeLoad: () => requireRole("BUYER")`)
- Modify: `frontend/src/routes/organizer.tsx` (`beforeLoad: () => requireRole("ORGANIZER")`)
- Modify: `frontend/src/hooks/useOrdersList.ts`
- Modify: `frontend/src/hooks/useEventCheckout.ts`
- Modify: `frontend/src/components/events/EventCheckoutPanel.tsx`
- Modify: `frontend/src/components/events/EventCatalogList.tsx`
- Create: `frontend/src/hooks/useOrganizerConsole.ts`
- Create: `frontend/src/components/events/OrganizerConsoleView.tsx`
- Update/Refactor: `frontend/src/hooks/useCreateEventForm.ts` / `OrganizerEventForm.tsx` (support both Create and Edit modes cleanly)
- Update: `README.md`, `.env.example`, `.env` (`ALLOW_DEMO_HEADER_AUTH` documented cleanly — note `POST /api/v1/demo/flash-sale` does NOT require `ALLOW_DEMO_HEADER_AUTH` so `/flash-sale` works regardless!)
- Create/Update Vitest tests: `frontend/src/lib/test/auth-guards.test.ts`

- [ ] **Step 1: Add `beforeLoad` RBAC guards on `/orders/`, `/orders/$orderId`, `/organizer`, and update `EventCheckoutPanel` + `useEventCheckout` so unauthenticated/organizer visitors see a clean `Login as Buyer` / `Buyer Account Required` notice instead of a stale "header auth switcher" message.**
- [ ] **Step 2: Build Organizer CRUD Console at `/organizer` listing only `GET /api/v1/organizer/events` with:**
  - Create new event + 2..10 ticket tiers (`POST /api/v1/events`)
  - Select any owned event to **Edit** metadata + tiers (`PUT /api/v1/events/{id}`) showing sold/held count per tier as minimum total quota constraint
  - **Delete** unused event (`DELETE /api/v1/events/{id}`) with explicit `EVENT_HAS_ORDERS` error handling when orders exist
- [ ] **Step 3: Regenerate TanStack route tree (`vite build` or `tsr`) and run full verification suite:**
  - `gofmt -s -l .`
  - `go build ./...`
  - `go vet ./...`
  - `go vet -tags=integration ./tests/...`
  - `go test -count=1 -race ./internal/... ./migrations/... ./cmd/...`
  - Frontend: `bun run lint`, `bun run typecheck`, `bun run test`, `bun run build`
  - `docker compose config --quiet`
- [ ] **Step 4: Stage intentionally, commit cleanly on `feat/web-app-rbac-crud`, push to `origin feat/web-app-rbac-crud`, and create PR to `main` via `github-mcp-server` (`create_pull_request`).**
