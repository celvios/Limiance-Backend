CREATE TABLE engine_symbol_sequences (
    pair VARCHAR(20) PRIMARY KEY REFERENCES trading_pairs(symbol),
    last_sequence_id NUMERIC(20,0) NOT NULL DEFAULT 0 CHECK (last_sequence_id >= 0),
    mode TEXT NOT NULL DEFAULT 'active' CHECK (mode IN ('active','replay_required')),
    replay_through_sequence_id NUMERIC(20,0) NOT NULL DEFAULT 0 CHECK (replay_through_sequence_id >= 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE engine_trade_inbox (
    pair VARCHAR(20) NOT NULL REFERENCES trading_pairs(symbol),
    sequence_id NUMERIC(20,0) NOT NULL CHECK (sequence_id > 0),
    payload_hash BYTEA NOT NULL CHECK (octet_length(payload_hash) = 32),
    received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    processed_at TIMESTAMPTZ,
    PRIMARY KEY (pair, sequence_id)
);

ALTER TABLE trades
    ADD COLUMN sequence_id NUMERIC(20,0) CHECK (sequence_id > 0),
    ADD COLUMN engine_timestamp_ns NUMERIC(20,0) CHECK (engine_timestamp_ns > 0),
    ADD COLUMN maker_order_id UUID REFERENCES orders(id),
    ADD COLUMN taker_order_id UUID REFERENCES orders(id),
    ADD COLUMN price_atomic NUMERIC(20,0) CHECK (price_atomic > 0),
    ADD COLUMN quantity_atomic NUMERIC(20,0) CHECK (quantity_atomic > 0),
    ADD COLUMN quote_amount_atomic NUMERIC(78,0) CHECK (quote_amount_atomic > 0),
    ADD CONSTRAINT trades_engine_fields_check CHECK (
        (sequence_id IS NULL) = (engine_timestamp_ns IS NULL)
        AND (sequence_id IS NULL) = (maker_order_id IS NULL)
        AND (sequence_id IS NULL) = (taker_order_id IS NULL)
        AND (sequence_id IS NULL) = (price_atomic IS NULL)
        AND (sequence_id IS NULL) = (quantity_atomic IS NULL)
        AND (sequence_id IS NULL) = (quote_amount_atomic IS NULL)
        AND (sequence_id IS NULL OR maker_order_id <> taker_order_id)
    );

CREATE UNIQUE INDEX trades_pair_sequence_idx ON trades (pair, sequence_id) WHERE sequence_id IS NOT NULL;

CREATE TABLE trade_participants (
    trade_id UUID NOT NULL REFERENCES trades(id) ON DELETE CASCADE,
    order_id UUID NOT NULL REFERENCES orders(id),
    user_id UUID NOT NULL REFERENCES users(id),
    account_id UUID NOT NULL REFERENCES accounts(id),
    role TEXT NOT NULL CHECK (role IN ('maker','taker')),
    side VARCHAR(4) NOT NULL CHECK (side IN ('BUY','SELL')),
    fee_bps SMALLINT NOT NULL,
    fee_amount_atomic NUMERIC(78,0) NOT NULL,
    PRIMARY KEY (trade_id, order_id),
    UNIQUE (trade_id, role)
);

CREATE TABLE trading_fee_accounts (
    asset_id UUID PRIMARY KEY REFERENCES assets(id),
    account_id UUID NOT NULL UNIQUE REFERENCES accounts(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE trade_fee_accruals (
    trade_id UUID NOT NULL REFERENCES trades(id) ON DELETE CASCADE,
    order_id UUID NOT NULL REFERENCES orders(id),
    user_id UUID NOT NULL REFERENCES users(id),
    asset_id UUID NOT NULL REFERENCES assets(id),
    fee_bps SMALLINT NOT NULL,
    amount_atomic NUMERIC(78,0) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (trade_id, order_id)
);

CREATE INDEX trades_pair_settled_idx ON trades (pair, traded_at DESC) WHERE sequence_id IS NOT NULL;
CREATE INDEX trade_participants_account_created_idx ON trade_participants (account_id, trade_id);
CREATE INDEX engine_trade_inbox_unprocessed_idx ON engine_trade_inbox (received_at) WHERE processed_at IS NULL;

COMMENT ON COLUMN trades.price_atomic IS 'Unsigned integer atomic units with trading_pairs.price_scale implied.';
COMMENT ON COLUMN trades.quantity_atomic IS 'Unsigned integer atomic units with trading_pairs.quantity_scale implied.';
COMMENT ON COLUMN trades.quote_amount_atomic IS 'Integer quote atomic units calculated without floating point.';
COMMENT ON COLUMN trade_participants.fee_amount_atomic IS 'Signed quote atomic units: positive is a fee, negative is a rebate.';
