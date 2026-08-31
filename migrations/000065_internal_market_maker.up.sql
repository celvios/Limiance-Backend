CREATE TABLE market_maker_control (
    singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    enabled BOOLEAN NOT NULL DEFAULT FALSE,
    dry_run BOOLEAN NOT NULL DEFAULT TRUE,
    kill_switch BOOLEAN NOT NULL DEFAULT TRUE,
    user_id UUID REFERENCES users(id),
    account_id UUID REFERENCES accounts(id),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((user_id IS NULL) = (account_id IS NULL))
);

INSERT INTO market_maker_control(singleton) VALUES(TRUE);

CREATE TABLE market_maker_configs (
    pair VARCHAR(20) PRIMARY KEY REFERENCES trading_pairs(symbol),
    enabled BOOLEAN NOT NULL DEFAULT FALSE,
    spread_bps INTEGER NOT NULL DEFAULT 30 CHECK (spread_bps BETWEEN 2 AND 10000),
    quantity_atomic NUMERIC(20,0) NOT NULL DEFAULT 1 CHECK (quantity_atomic > 0),
    max_base_inventory_atomic NUMERIC(78,0) NOT NULL DEFAULT 1 CHECK (max_base_inventory_atomic > 0),
    max_quote_notional_atomic NUMERIC(78,0) NOT NULL DEFAULT 1 CHECK (max_quote_notional_atomic > 0),
    max_daily_loss_atomic NUMERIC(78,0) NOT NULL DEFAULT 1 CHECK (max_daily_loss_atomic > 0),
    max_divergence_bps INTEGER NOT NULL DEFAULT 100 CHECK (max_divergence_bps BETWEEN 1 AND 10000),
    stale_after_seconds INTEGER NOT NULL DEFAULT 10 CHECK (stale_after_seconds BETWEEN 1 AND 300),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO market_maker_configs(pair)
SELECT symbol FROM trading_pairs
ON CONFLICT (pair) DO NOTHING;

CREATE TABLE market_maker_risk_state (
    pair VARCHAR(20) PRIMARY KEY REFERENCES trading_pairs(symbol),
    base_inventory_atomic NUMERIC(78,0) NOT NULL DEFAULT 0,
    realized_pnl_quote_atomic NUMERIC(78,0) NOT NULL DEFAULT 0,
    risk_observed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_reference_price_atomic NUMERIC(20,0),
    last_decision VARCHAR(40) NOT NULL DEFAULT 'disabled',
    last_reason TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO market_maker_risk_state(pair)
SELECT symbol FROM trading_pairs
ON CONFLICT (pair) DO NOTHING;

CREATE TABLE market_maker_command_log (
    idempotency_key VARCHAR(255) PRIMARY KEY,
    pair VARCHAR(20) NOT NULL REFERENCES trading_pairs(symbol),
    side VARCHAR(4) NOT NULL CHECK (side IN ('BUY','SELL')),
    price_atomic NUMERIC(20,0) NOT NULL CHECK (price_atomic > 0),
    quantity_atomic NUMERIC(20,0) NOT NULL CHECK (quantity_atomic > 0),
    dry_run BOOLEAN NOT NULL,
    order_id UUID REFERENCES orders(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON TABLE market_maker_control IS 'Fail-closed control: disabled, dry-run, and kill switch enabled by default.';
COMMENT ON TABLE market_maker_command_log IS 'Idempotent audit of commands routed through the ordinary order gateway.';
