CREATE TABLE trades (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    pair VARCHAR(64) NOT NULL,
    maker_user_id UUID NOT NULL REFERENCES users(id),
    taker_user_id UUID NOT NULL REFERENCES users(id),
    quantity NUMERIC(36,18) NOT NULL CHECK (quantity > 0),
    price NUMERIC(36,18) NOT NULL CHECK (price > 0),
    traded_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX trades_traded_at_idx ON trades (traded_at);
CREATE INDEX trades_maker_user_idx ON trades (maker_user_id, traded_at);
CREATE INDEX trades_taker_user_idx ON trades (taker_user_id, traded_at);

CREATE TABLE vip_tiers (
    level INT PRIMARY KEY,
    name VARCHAR(50) NOT NULL,
    maker_fee_bps INT NOT NULL CHECK (maker_fee_bps >= 0),
    taker_fee_bps INT NOT NULL CHECK (taker_fee_bps >= 0),
    min_30d_volume NUMERIC(36,18) NOT NULL DEFAULT 0 CHECK (min_30d_volume >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE user_volume_30d (
    user_id UUID PRIMARY KEY REFERENCES users(id),
    volume_usd NUMERIC(36,18) NOT NULL DEFAULT 0 CHECK (volume_usd >= 0),
    window_start TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE user_fee_tier (
    user_id UUID PRIMARY KEY REFERENCES users(id),
    tier_level INT NOT NULL REFERENCES vip_tiers(level),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO vip_tiers (level, name, maker_fee_bps, taker_fee_bps, min_30d_volume) VALUES
    (0, 'VIP 0', 10, 10, 0),
    (1, 'VIP 1', 8, 10, 50000),
    (2, 'VIP 2', 6, 10, 250000),
    (3, 'VIP 3', 4, 8, 1000000),
    (4, 'VIP 4', 2, 8, 5000000),
    (5, 'VIP 5', 0, 6, 20000000);