CREATE TABLE conversion_pairs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    from_asset_id UUID NOT NULL REFERENCES assets(id),
    to_asset_id UUID NOT NULL REFERENCES assets(id),
    market_symbol TEXT NOT NULL,
    spread_bps INTEGER NOT NULL DEFAULT 0 CHECK (spread_bps BETWEEN 0 AND 1000),
    fee_bps INTEGER NOT NULL DEFAULT 0 CHECK (fee_bps BETWEEN 0 AND 1000),
    status TEXT NOT NULL DEFAULT 'disabled' CHECK (status IN ('enabled','disabled')),
    CHECK (from_asset_id <> to_asset_id),
    UNIQUE (from_asset_id, to_asset_id)
);

CREATE TABLE conversion_quotes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id),
    source_account_id UUID NOT NULL REFERENCES accounts(id),
    from_asset_id UUID NOT NULL REFERENCES assets(id),
    to_asset_id UUID NOT NULL REFERENCES assets(id),
    input_amount_atomic NUMERIC(78,0) NOT NULL CHECK (input_amount_atomic > 0),
    output_amount_atomic NUMERIC(78,0) NOT NULL CHECK (output_amount_atomic > 0),
    fee_amount_atomic NUMERIC(78,0) NOT NULL DEFAULT 0 CHECK (fee_amount_atomic >= 0),
    price TEXT NOT NULL,
    provider TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    confirmed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX conversion_quotes_user_created_idx ON conversion_quotes (user_id, created_at DESC);

CREATE TABLE conversions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    quote_id UUID NOT NULL UNIQUE REFERENCES conversion_quotes(id),
    user_id UUID NOT NULL REFERENCES users(id),
    idempotency_key TEXT NOT NULL UNIQUE,
    journal_id UUID UNIQUE REFERENCES journals(id),
    status TEXT NOT NULL CHECK (status IN ('settled','failed')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
