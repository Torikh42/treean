ALTER TABLE orders
    ADD COLUMN IF NOT EXISTS idempotency_key TEXT NULL;

ALTER TABLE orders
    DROP CONSTRAINT IF EXISTS chk_orders_idempotency_key_len;

ALTER TABLE orders
    ADD CONSTRAINT chk_orders_idempotency_key_len
    CHECK (idempotency_key IS NULL OR char_length(idempotency_key) BETWEEN 1 AND 128);

CREATE UNIQUE INDEX IF NOT EXISTS uidx_orders_buyer_idempotency_key
    ON orders (buyer_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;
