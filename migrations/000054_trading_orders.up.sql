CREATE TABLE trading_pairs (
    symbol VARCHAR(20) PRIMARY KEY,
    base_asset_id UUID NOT NULL REFERENCES assets(id),
    quote_asset_id UUID NOT NULL REFERENCES assets(id),
    price_scale SMALLINT NOT NULL DEFAULT 8 CHECK (price_scale BETWEEN 0 AND 18),
    quantity_scale SMALLINT NOT NULL DEFAULT 8 CHECK (quantity_scale BETWEEN 0 AND 18),
    min_quantity_atomic NUMERIC(20,0) NOT NULL DEFAULT 1 CHECK (min_quantity_atomic > 0),
    max_quantity_atomic NUMERIC(20,0) NOT NULL DEFAULT 18446744073709551615 CHECK (max_quantity_atomic > 0),
    price_tick_atomic NUMERIC(20,0) NOT NULL DEFAULT 1 CHECK (price_tick_atomic > 0),
    quantity_step_atomic NUMERIC(20,0) NOT NULL DEFAULT 1 CHECK (quantity_step_atomic > 0),
    status TEXT NOT NULL DEFAULT 'disabled' CHECK (status IN ('active', 'halted', 'disabled')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (base_asset_id <> quote_asset_id),
    CHECK (min_quantity_atomic <= max_quantity_atomic)
);

CREATE TABLE orders (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id),
    account_id UUID NOT NULL REFERENCES accounts(id),
    pair VARCHAR(20) NOT NULL REFERENCES trading_pairs(symbol),
    side VARCHAR(4) NOT NULL CHECK (side IN ('BUY', 'SELL')),
    type VARCHAR(20) NOT NULL CHECK (type IN ('LIMIT', 'MARKET', 'STOP_LIMIT')),
    price NUMERIC(20,0) CHECK (price > 0),
    quantity NUMERIC(20,0) NOT NULL CHECK (quantity > 0),
    filled_quantity NUMERIC(20,0) NOT NULL DEFAULT 0 CHECK (filled_quantity >= 0),
    remaining_quantity NUMERIC(20,0) NOT NULL CHECK (remaining_quantity >= 0),
    avg_price NUMERIC(20,0) NOT NULL DEFAULT 0 CHECK (avg_price >= 0),
    time_in_force VARCHAR(3) NOT NULL CHECK (time_in_force IN ('GTC', 'IOC', 'FOK')),
    status VARCHAR(20) NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'OPEN', 'PARTIALLY_FILLED', 'FILLED', 'CANCELED', 'REJECTED')),
    post_only BOOLEAN NOT NULL DEFAULT FALSE,
    reduce_only BOOLEAN NOT NULL DEFAULT FALSE,
    fee_tier SMALLINT NOT NULL CHECK (fee_tier BETWEEN 0 AND 255),
    idempotency_key VARCHAR(255) NOT NULL,
    request_hash BYTEA NOT NULL CHECK (octet_length(request_hash) = 32),
    hold_journal_id UUID UNIQUE REFERENCES journals(id),
    hold_asset_id UUID REFERENCES assets(id),
    hold_amount_atomic NUMERIC(20,0) CHECK (hold_amount_atomic > 0),
    engine_sequence_id NUMERIC(20,0) CHECK (engine_sequence_id > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, idempotency_key),
    CHECK (filled_quantity + remaining_quantity = quantity),
    CHECK ((hold_journal_id IS NULL) = (hold_asset_id IS NULL)),
    CHECK ((hold_asset_id IS NULL) = (hold_amount_atomic IS NULL))
);

CREATE INDEX orders_user_status_idx ON orders (user_id, status, created_at DESC);
CREATE INDEX orders_account_status_idx ON orders (account_id, status, created_at DESC);
CREATE INDEX orders_pair_status_idx ON orders (pair, status, created_at DESC);

CREATE TABLE engine_commands (
    order_id UUID PRIMARY KEY REFERENCES orders(id) ON DELETE CASCADE,
    payload BYTEA NOT NULL CHECK (octet_length(payload) > 0),
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'dispatched')),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error TEXT NOT NULL DEFAULT '',
    dispatched_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX engine_commands_pending_idx ON engine_commands (created_at) WHERE status = 'pending';

COMMENT ON COLUMN orders.price IS 'Unsigned integer atomic units with pair.price_scale implied.';
COMMENT ON COLUMN orders.quantity IS 'Unsigned integer atomic units with pair.quantity_scale implied.';
COMMENT ON COLUMN orders.filled_quantity IS 'Unsigned integer atomic units with pair.quantity_scale implied.';
COMMENT ON COLUMN orders.remaining_quantity IS 'Unsigned integer atomic units with pair.quantity_scale implied.';
COMMENT ON COLUMN orders.avg_price IS 'Unsigned integer atomic units with pair.price_scale implied.';
