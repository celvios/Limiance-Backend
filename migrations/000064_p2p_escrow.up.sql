CREATE TABLE p2p_trades (
    id UUID PRIMARY KEY,
    seller_user_id UUID NOT NULL REFERENCES users(id),
    seller_account_id UUID NOT NULL REFERENCES accounts(id),
    buyer_user_id UUID REFERENCES users(id),
    buyer_account_id UUID REFERENCES accounts(id),
    asset_id UUID NOT NULL REFERENCES assets(id),
    amount_atomic NUMERIC(78,0) NOT NULL CHECK (amount_atomic > 0),
    fiat_currency VARCHAR(3) NOT NULL CHECK (fiat_currency = upper(fiat_currency)),
    fiat_amount DECIMAL(24,8) NOT NULL CHECK (fiat_amount > 0),
    payment_method VARCHAR(100) NOT NULL,
    status VARCHAR(24) NOT NULL CHECK (status IN (
        'open','accepted','paid','disputed','released','refunded','cancelled','expired'
    )),
    offer_expires_at TIMESTAMPTZ NOT NULL,
    payment_deadline TIMESTAMPTZ,
    accepted_at TIMESTAMPTZ,
    paid_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((buyer_user_id IS NULL) = (buyer_account_id IS NULL)),
    CHECK (buyer_user_id IS NULL OR buyer_user_id <> seller_user_id)
);

CREATE INDEX p2p_trades_status_expiry_idx
    ON p2p_trades (status, offer_expires_at, payment_deadline);
CREATE INDEX p2p_trades_seller_idx ON p2p_trades (seller_user_id, created_at DESC);
CREATE INDEX p2p_trades_buyer_idx ON p2p_trades (buyer_user_id, created_at DESC);

CREATE TABLE p2p_events (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    trade_id UUID NOT NULL REFERENCES p2p_trades(id),
    event_type VARCHAR(40) NOT NULL,
    actor_key TEXT NOT NULL,
    actor_user_id UUID REFERENCES users(id),
    idempotency_key VARCHAR(255) NOT NULL,
    request_hash BYTEA NOT NULL CHECK (octet_length(request_hash) = 32),
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (actor_key, idempotency_key)
);

CREATE INDEX p2p_events_trade_idx ON p2p_events (trade_id, id);

CREATE TABLE p2p_evidence (
    id UUID PRIMARY KEY,
    trade_id UUID NOT NULL REFERENCES p2p_trades(id),
    submitted_by UUID NOT NULL REFERENCES users(id),
    evidence_type VARCHAR(40) NOT NULL,
    object_key TEXT NOT NULL,
    sha256_hex CHAR(64) NOT NULL CHECK (sha256_hex ~ '^[0-9a-f]{64}$'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (trade_id, sha256_hex)
);

CREATE TABLE p2p_resolution_requests (
    id UUID PRIMARY KEY,
    trade_id UUID NOT NULL REFERENCES p2p_trades(id),
    action VARCHAR(20) NOT NULL CHECK (action IN ('force_release','refund')),
    reason TEXT NOT NULL,
    proposed_by UUID NOT NULL REFERENCES users(id),
    approved_by UUID REFERENCES users(id),
    status VARCHAR(20) NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','rejected')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    decided_at TIMESTAMPTZ,
    CHECK (approved_by IS NULL OR approved_by <> proposed_by)
);

CREATE UNIQUE INDEX p2p_resolution_pending_trade_idx
    ON p2p_resolution_requests (trade_id) WHERE status = 'pending';

CREATE OR REPLACE FUNCTION reject_p2p_immutable_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'P2P event and evidence records are immutable';
END;
$$;

CREATE TRIGGER p2p_events_immutable
BEFORE UPDATE OR DELETE ON p2p_events
FOR EACH ROW EXECUTE FUNCTION reject_p2p_immutable_mutation();

CREATE TRIGGER p2p_evidence_immutable
BEFORE UPDATE OR DELETE ON p2p_evidence
FOR EACH ROW EXECUTE FUNCTION reject_p2p_immutable_mutation();

COMMENT ON TABLE p2p_trades IS 'Crypto escrow only. Fiat payment occurs off-platform and is merely confirmed here.';
COMMENT ON COLUMN p2p_trades.amount_atomic IS 'Unsigned integer atomic units; never floating point.';

