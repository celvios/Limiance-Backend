ALTER TABLE orders DROP CONSTRAINT orders_type_check;
ALTER TABLE orders ADD CONSTRAINT orders_type_check
    CHECK (type IN ('LIMIT','MARKET','STOP_LIMIT','STOP_MARKET','TAKE_PROFIT_LIMIT'));

ALTER TABLE orders DROP CONSTRAINT orders_status_check;
ALTER TABLE orders ADD CONSTRAINT orders_status_check
    CHECK (status IN ('CONDITIONAL','PENDING','PENDING_CANCEL','OPEN','PARTIALLY_FILLED','FILLED','CANCELED','REJECTED'));

ALTER TABLE orders
	ADD COLUMN trigger_price NUMERIC(20,0) CHECK (trigger_price > 0),
	ADD COLUMN trigger_direction TEXT CHECK (trigger_direction IN ('UP','DOWN')),
	ADD COLUMN triggered_at TIMESTAMPTZ,
	ADD COLUMN hold_released_at TIMESTAMPTZ,
    ADD CONSTRAINT orders_conditional_trigger_check CHECK (
        (type IN ('STOP_LIMIT','STOP_MARKET','TAKE_PROFIT_LIMIT')) = (trigger_price IS NOT NULL)
        AND (trigger_price IS NULL) = (trigger_direction IS NULL)
    );

ALTER TABLE engine_commands DROP CONSTRAINT engine_commands_status_check;
ALTER TABLE engine_commands ADD CONSTRAINT engine_commands_status_check
    CHECK (status IN ('blocked','pending','dispatched','canceled'));

CREATE TABLE engine_control_commands (
    id UUID PRIMARY KEY,
    order_id UUID NOT NULL REFERENCES orders(id),
    user_id UUID NOT NULL REFERENCES users(id),
    idempotency_key VARCHAR(255) NOT NULL,
    payload BYTEA NOT NULL CHECK (octet_length(payload) > 0),
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','dispatched','rejected')),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error TEXT NOT NULL DEFAULT '',
    sequence_id NUMERIC(20,0) CHECK (sequence_id > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, idempotency_key)
);

CREATE INDEX engine_control_commands_pending_idx
    ON engine_control_commands (created_at) WHERE status = 'pending';
CREATE INDEX orders_conditional_trigger_idx
    ON orders (pair, trigger_direction, trigger_price, created_at)
    WHERE status = 'CONDITIONAL';

COMMENT ON COLUMN orders.trigger_price IS 'Unsigned integer atomic units with trading_pairs.price_scale implied.';
