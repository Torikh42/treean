# Product Specification: Treean (`event-ticketing`)

> **Repository:** `https://github.com/Torikh42/treean`
> **Source Brief:** [`final-project.md`](../../../final-project.md)
> **Status:** Approved Design Baseline (2026-11-15)

---

## 1. Product Goal

Provide a high-concurrency REST API for limited-quota event ticket sales where multiple buyers compete for tickets simultaneously, orders reserve quota for 15 minutes before automatic release, buyer ticket limits are strictly enforced per event, and payment confirmations remain idempotent under duplicate or late webhook delivery.

---

## 2. User Roles & Identity

1. **Organizer (`ORGANIZER`)**:
   - Registers or authenticates via JWT, or acts via test/demo identity headers (`X-User-ID`, `X-User-Role`) when `ALLOW_DEMO_HEADER_AUTH=true`.
   - Creates events with at least two ticket tiers (for example `Reguler` and `VIP`), each with its own quota and price in IDR.
2. **Buyer (`BUYER`)**:
   - Registers or authenticates via JWT, or acts via test/demo identity headers (`X-User-ID`, `X-User-Role`) when `ALLOW_DEMO_HEADER_AUTH=true`.
   - Browses events and live remaining quota per ticket tier.
   - Places multi-item ticket orders (1 to 4 tickets), cancels unpaid orders, completes simulated payments, and views order history alongside issued unique ticket codes.
3. **Payment Provider (Simulated / Webhook Caller)**:
   - Sends signed `POST /api/v1/webhooks/payment` callbacks containing Midtrans-style SHA-512 payload signatures (`order_id`, `status_code`, `gross_amount`, `transaction_id`, `transaction_status`, `signature_key`).

---

## 3. Behavior Invariants (B1 to B10)

Every business rule and bonus requirement from `final-project.md` maps to a numbered invariant below. Both `TECH.md` and automated tests verify against these exact identifiers.

| ID | Rule / Challenge | Exact System Behavior |
| :--- | :--- | :--- |
| **B1** | Organizer creates event with 2+ ticket types | `POST /api/v1/events` (restricted to `ORGANIZER`) creates an event and at least 2 `ticket_types` (each `price_idr >= 0`, `total_quota > 0`, initialized with `remaining_quota = total_quota`) atomically inside one database transaction. |
| **B2** | Real-time event & remaining quota view | `GET /api/v1/events` and `GET /api/v1/events/{id}` sweep any due expired orders first (when the partial expiry index indicates due rows exist) and return live `remaining_quota` per ticket type with zero artificial delay. |
| **B3** | 1 to 4 tickets per order & 15-minute hold | `POST /api/v1/orders` validates total requested quantity is between `1` and `4` across one or more ticket types of the same event, decrements `remaining_quota`, inserts an order in `WAITING_PAYMENT` status with `expires_at = created_at + 15 minutes`, and records an `order_logs` entry. |
| **B4** | Max 4 tickets per buyer per event | For a given `(event_id, buyer_id)` pair, the sum of `total_tickets` across all orders in `WAITING_PAYMENT` or `PAID` status plus the new request must never exceed `4`. Concurrent checkout requests from the same buyer are serialized per `(event_id, buyer_id)` so parallel requests cannot bypass the limit. Orders in `CANCELLED` or `EXPIRED` status do not consume the buyer's 4-ticket allowance. |
| **B5** | Manual cancellation returns quota | `POST /api/v1/orders/{id}/cancel` verifies buyer ownership and `WAITING_PAYMENT` status (and non-expired timestamp), transitions the order to `CANCELLED`, immediately restores each item's `quantity` to `ticket_types.remaining_quota`, and writes an `order_logs` audit row. Cancelling an already `PAID`, `CANCELLED`, or `EXPIRED` order is rejected (`409`) without touching quota. |
| **B6** | Auto-expire after 15 minutes | An unpaid order past `expires_at` transitions automatically to `EXPIRED` both via a background goroutine ticker (`SKIP LOCKED` batches) and via lazy checks on reads/writes (`GET /events`, `POST /orders`, `POST /orders/{id}/cancel`, `POST /orders/{id}/pay`, `POST /webhooks/payment`). Its reserved quota returns atomically to `ticket_types.remaining_quota` exactly once, logged by `'scheduler'` or `'lazy-check'`. |
| **B7** | Simulated payment & webhook signature | Payment succeeds either through `POST /api/v1/webhooks/payment` (verified via constant-time `SHA-512(order_id + status_code + gross_amount + secret)` plus exact `gross_amount == total_amount_idr` integer comparison) or through convenience helper `POST /api/v1/orders/{id}/pay` (which builds a signed payment payload for demo use). Missing/wrong secret or mismatched signature fails closed with `401 UNAUTHORIZED_WEBHOOK`; wrong amount fails with `400 PAYMENT_AMOUNT_MISMATCH`. |
| **B8** | Unique ticket issuance & no duplicates | When an order transitions from `WAITING_PAYMENT` (unexpired) to `PAID`, the system emits exactly one unique `ticket_code` per ticket (`total_tickets` rows in `tickets`), protected by `UNIQUE (order_item_id, seat_sequence)` and `UNIQUE (ticket_code)`. Replaying or concurrently firing duplicate success webhooks for a `PAID` order returns `200 OK` (`idempotent_replay: true`) with the original tickets and zero additional inserts. |
| **B9** | Late webhook after expiry (Quota Safety First) | If a payment capture arrives after `expires_at` or when the order is already `EXPIRED` or `CANCELLED`, the order is ensured `EXPIRED` (quota returned if not yet released), zero tickets are issued, the callback payload is permanently saved in `payment_webhooks` with `outcome = REJECTED_LATE_EXPIRED` and `requires_refund = true`, and the endpoint responds with `409 ORDER_EXPIRED`. Quota never goes negative (`remaining_quota >= 0`). |
| **B10** | Order history & 200 concurrent buyers | `GET /api/v1/orders` and `GET /api/v1/orders/{id}` return buyer order history including items, status logs, and issued `tickets`. Under 200 simultaneous booking requests against 100 total quota, database row locking (`FOR UPDATE` in deterministic ascending ID order) yields exact accounting: 100 tickets reserved (`total_tickets` sum = 100), remaining requests cleanly rejected (`409 QUOTA_EXHAUSTED`), zero oversell, and zero deadlocks. |

---

## 4. Order State Machine

```text
                       (buyer cancels < 15m)
                  +-----------------------------> [CANCELLED] (quota restored)
                  |
[POST /orders] ---> [WAITING_PAYMENT]
                  |      |
                  |      +--> (expires_at <= NOW) --> [EXPIRED] (quota restored)
                  |                                      ^
                  | (valid capture < 15m)                | (late capture > 15m: rejected + refund log)
                  v                                      |
                [PAID] (tickets issued once) ------------X
```

Rules:
- Only `WAITING_PAYMENT` (while `now <= expires_at`) can transition to `PAID` or `CANCELLED`.
- `PAID`, `CANCELLED`, and `EXPIRED` are terminal states for quota allocation; transitioning into `CANCELLED` or `EXPIRED` restores quota atomically within the same transaction that moves out of `WAITING_PAYMENT`.
- Duplicate `PAID` confirmations on an already-`PAID` order are idempotent no-ops returning `200 OK`.

---

## 5. Domain Glossary

| Term | Definition |
| :--- | :--- |
| **Event** | A ticketed occasion created by an `ORGANIZER`, containing 2 or more distinct `TicketType` tiers. |
| **Ticket Type** | A named pricing/quota tier belonging to an event (e.g., `Reguler`, `VIP`) with immutable `total_quota` (at creation) and live `remaining_quota`. |
| **Quota Hold** | Temporary reservation of `remaining_quota` starting at order creation (`WAITING_PAYMENT`) and lasting 15 minutes (`expires_at`). |
| **Buyer Allowance Cap** | The invariant limiting each buyer to a maximum of 4 tickets per event across all active (`WAITING_PAYMENT`) and completed (`PAID`) orders. |
| **Order Log** | Immutable audit record (`order_logs`) capturing every order status transition, actor identity/type (`buyer`, `webhook`, `scheduler`, `lazy-check`), and reason. |
| **Payment Webhook Audit** | Persistent record (`payment_webhooks`) written outside the main quota rollback boundary so every invalid signature, amount mismatch, duplicate replay, and late payment stays traceable. |
| **Idempotent Replay** | Receiving the same payment confirmation multiple times produces identical database state and returns the existing issued tickets without duplicating rows. |
| **Late Payment Refund Flag** | Audit flag (`requires_refund = true` with outcome `REJECTED_LATE_EXPIRED`) recorded when money settles after the 15-minute reservation expired. |
| **Single-Command Run** | Bootstrapping PostgreSQL, schema migrations, seed data, expiry worker, API docs, and API server via one `docker compose up` command on a clean host. |
