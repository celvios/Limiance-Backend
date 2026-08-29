CREATE TABLE market_stream_sequences (
    pair VARCHAR(20) NOT NULL REFERENCES trading_pairs(symbol),
    stream TEXT NOT NULL CHECK (stream IN ('orderbook','trades')),
    last_sequence_id NUMERIC(20,0) NOT NULL CHECK (last_sequence_id > 0),
    mode TEXT NOT NULL DEFAULT 'active' CHECK (mode IN ('active','replay_required')),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (pair, stream)
);

CREATE TABLE market_candles_1m (
    pair VARCHAR(20) NOT NULL REFERENCES trading_pairs(symbol),
    open_time TIMESTAMPTZ NOT NULL,
    open_atomic NUMERIC(20,0) NOT NULL CHECK (open_atomic > 0),
    high_atomic NUMERIC(20,0) NOT NULL CHECK (high_atomic > 0),
    low_atomic NUMERIC(20,0) NOT NULL CHECK (low_atomic > 0),
    close_atomic NUMERIC(20,0) NOT NULL CHECK (close_atomic > 0),
    volume_atomic NUMERIC(78,0) NOT NULL CHECK (volume_atomic >= 0),
    quote_volume_atomic NUMERIC(78,0) NOT NULL CHECK (quote_volume_atomic >= 0),
    trade_count BIGINT NOT NULL CHECK (trade_count >= 0),
    last_sequence_id NUMERIC(20,0) NOT NULL CHECK (last_sequence_id > 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (pair, open_time),
    CHECK (open_time = date_trunc('minute', open_time)),
    CHECK (high_atomic >= open_atomic AND high_atomic >= close_atomic),
    CHECK (low_atomic <= open_atomic AND low_atomic <= close_atomic)
);

CREATE TABLE market_trade_events (
    pair VARCHAR(20) NOT NULL REFERENCES trading_pairs(symbol),
    sequence_id NUMERIC(20,0) NOT NULL CHECK (sequence_id > 0),
    traded_at TIMESTAMPTZ NOT NULL,
    price_atomic NUMERIC(20,0) NOT NULL CHECK (price_atomic > 0),
    quantity_atomic NUMERIC(20,0) NOT NULL CHECK (quantity_atomic > 0),
    quote_amount_atomic NUMERIC(78,0) NOT NULL CHECK (quote_amount_atomic > 0),
    taker_side VARCHAR(4) NOT NULL CHECK (taker_side IN ('BUY','SELL')),
    PRIMARY KEY (pair, sequence_id)
);

CREATE INDEX market_candles_1m_pair_time_idx ON market_candles_1m (pair, open_time DESC);
CREATE INDEX market_trade_events_pair_time_idx ON market_trade_events (pair, traded_at DESC, sequence_id DESC);
CREATE INDEX trades_public_pair_time_idx ON trades (pair, traded_at DESC, sequence_id DESC) WHERE sequence_id IS NOT NULL;

COMMENT ON TABLE market_candles_1m IS 'Canonical one-minute candles. Larger intervals are derived with integer arithmetic.';
COMMENT ON TABLE market_trade_events IS 'Public market-data read model derived idempotently from engine trade events.';
