CREATE TABLE market_maker_activation_requests (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    action VARCHAR(24) NOT NULL CHECK (action IN ('configure_dry_run','release_live')),
    payload JSONB NOT NULL,
    reason TEXT NOT NULL CHECK (length(reason) BETWEEN 8 AND 1000),
    proposed_by UUID NOT NULL REFERENCES users(id),
    approved_by UUID REFERENCES users(id),
    status VARCHAR(16) NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','rejected')),
    idempotency_key VARCHAR(255) NOT NULL,
    request_hash BYTEA NOT NULL CHECK (octet_length(request_hash) = 32),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    decided_at TIMESTAMPTZ,
    UNIQUE (proposed_by, idempotency_key),
    CHECK (approved_by IS NULL OR approved_by <> proposed_by)
);

CREATE UNIQUE INDEX market_maker_activation_pending_action_idx
    ON market_maker_activation_requests(action) WHERE status='pending';

CREATE TABLE market_maker_inventory_journals (
    request_id UUID NOT NULL REFERENCES market_maker_activation_requests(id),
    asset_id UUID NOT NULL REFERENCES assets(id),
    journal_id UUID NOT NULL REFERENCES journals(id),
    amount_atomic NUMERIC(78,0) NOT NULL CHECK (amount_atomic > 0),
    PRIMARY KEY (request_id, asset_id)
);

COMMENT ON TABLE market_maker_activation_requests IS 'Maker-checker requests for dry-run configuration and live release.';
COMMENT ON TABLE market_maker_inventory_journals IS 'Balanced treasury journals that seeded approved internal market-maker inventory.';
