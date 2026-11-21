CREATE SEQUENCE IF NOT EXISTS invoice_seq START 1;

CREATE TABLE IF NOT EXISTS users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    password_hash TEXT NOT NULL DEFAULT '',
    role TEXT NOT NULL CHECK (role IN ('ORGANIZER', 'BUYER')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organizer_id UUID NOT NULL REFERENCES users(id),
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    venue TEXT NOT NULL,
    event_date TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_events_event_date_created
    ON events (event_date ASC, created_at ASC);

CREATE INDEX IF NOT EXISTS idx_events_organizer_date
    ON events (organizer_id, event_date ASC, created_at ASC);

CREATE TABLE IF NOT EXISTS ticket_types (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    event_id UUID NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    price_idr BIGINT NOT NULL CHECK (price_idr >= 0),
    total_quota INTEGER NOT NULL CHECK (total_quota > 0),
    remaining_quota INTEGER NOT NULL CHECK (remaining_quota >= 0 AND remaining_quota <= total_quota),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (event_id, name)
);

CREATE UNIQUE INDEX IF NOT EXISTS uidx_ticket_types_event_lower_name
    ON ticket_types (event_id, LOWER(name));

CREATE TABLE IF NOT EXISTS orders (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    invoice_number TEXT NOT NULL UNIQUE,
    event_id UUID NOT NULL REFERENCES events(id),
    buyer_id UUID NOT NULL REFERENCES users(id),
    total_tickets INTEGER NOT NULL CHECK (total_tickets BETWEEN 1 AND 4),
    total_amount_idr BIGINT NOT NULL CHECK (total_amount_idr >= 0),
    status TEXT NOT NULL CHECK (status IN ('WAITING_PAYMENT', 'PAID', 'CANCELLED', 'EXPIRED')),
    expires_at TIMESTAMPTZ NOT NULL,
    paid_at TIMESTAMPTZ NULL,
    cancelled_at TIMESTAMPTZ NULL,
    expired_at TIMESTAMPTZ NULL,
    cancelled_reason TEXT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_orders_buyer_event_status
    ON orders (event_id, buyer_id)
    INCLUDE (total_tickets)
    WHERE status IN ('WAITING_PAYMENT', 'PAID');

CREATE INDEX IF NOT EXISTS idx_orders_buyer_created
    ON orders (buyer_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_orders_active_expiry
    ON orders (event_id, expires_at)
    WHERE status = 'WAITING_PAYMENT';

CREATE INDEX IF NOT EXISTS idx_orders_global_expiry
    ON orders (expires_at)
    WHERE status = 'WAITING_PAYMENT';

CREATE INDEX IF NOT EXISTS idx_orders_event_id
    ON orders (event_id);

CREATE TABLE IF NOT EXISTS order_items (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id UUID NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    ticket_type_id UUID NOT NULL REFERENCES ticket_types(id),
    quantity INTEGER NOT NULL CHECK (quantity > 0),
    unit_price_idr BIGINT NOT NULL CHECK (unit_price_idr >= 0),
    subtotal_idr BIGINT NOT NULL CHECK (subtotal_idr >= 0),
    CHECK (subtotal_idr = unit_price_idr * quantity::bigint),
    UNIQUE (order_id, ticket_type_id)
);

CREATE INDEX IF NOT EXISTS idx_order_items_ticket_type_id
    ON order_items (ticket_type_id);

CREATE TABLE IF NOT EXISTS order_logs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id UUID NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    from_status TEXT NULL,
    to_status TEXT NOT NULL,
    actor_id UUID NULL,
    actor_type TEXT NOT NULL,
    note TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (from_status IS NULL OR from_status IN ('WAITING_PAYMENT', 'PAID', 'CANCELLED', 'EXPIRED')),
    CHECK (to_status IN ('WAITING_PAYMENT', 'PAID', 'CANCELLED', 'EXPIRED')),
    CHECK (actor_type IN ('buyer', 'webhook', 'scheduler', 'lazy-check'))
);

CREATE INDEX IF NOT EXISTS idx_order_logs_order_id
    ON order_logs (order_id, created_at);

CREATE TABLE IF NOT EXISTS tickets (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    ticket_code TEXT NOT NULL UNIQUE,
    order_id UUID NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    order_item_id UUID NOT NULL REFERENCES order_items(id) ON DELETE CASCADE,
    event_id UUID NOT NULL REFERENCES events(id),
    ticket_type_id UUID NOT NULL REFERENCES ticket_types(id),
    owner_id UUID NOT NULL REFERENCES users(id),
    seat_sequence INTEGER NOT NULL CHECK (seat_sequence > 0),
    issued_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (order_item_id, seat_sequence)
);

CREATE INDEX IF NOT EXISTS idx_tickets_order_id
    ON tickets (order_id);

CREATE INDEX IF NOT EXISTS idx_tickets_owner_id
    ON tickets (owner_id);

CREATE TABLE IF NOT EXISTS payment_webhooks (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    transaction_id TEXT NOT NULL,
    raw_order_id TEXT NOT NULL,
    parsed_order_id UUID NULL,
    status_code TEXT NOT NULL,
    gross_amount BIGINT NOT NULL,
    transaction_status TEXT NOT NULL,
    outcome TEXT NOT NULL,
    requires_refund BOOLEAN NOT NULL DEFAULT FALSE,
    raw_payload JSONB NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_payment_webhooks_tx_id
    ON payment_webhooks (transaction_id);

CREATE INDEX IF NOT EXISTS idx_payment_webhooks_parsed_order_id
    ON payment_webhooks (parsed_order_id, received_at DESC)
    WHERE parsed_order_id IS NOT NULL;
