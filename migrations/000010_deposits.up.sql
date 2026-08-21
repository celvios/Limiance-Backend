CREATE TABLE deposits (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id),
    asset_id UUID NOT NULL REFERENCES assets(id),
    deposit_address_id UUID NOT NULL REFERENCES deposit_addresses(id),
    custody_wallet_id UUID NOT NULL REFERENCES custody_wallets(id),
    provider_transaction_id TEXT NOT NULL,
    transaction_hash TEXT NOT NULL DEFAULT '',
    blockchain_index TEXT NOT NULL DEFAULT '',
    amount_atomic NUMERIC(78,0) NOT NULL CHECK (amount_atomic > 0),
    confirmations INTEGER NOT NULL DEFAULT 0 CHECK (confirmations >= 0),
    confirmations_required INTEGER NOT NULL CHECK (confirmations_required > 0),
    status TEXT NOT NULL CHECK (status IN ('observed', 'confirming', 'pending_risk_review', 'credited', 'reorged', 'rejected')),
    risk_status TEXT NOT NULL DEFAULT 'not_started' CHECK (risk_status IN ('not_started', 'pending', 'approved', 'rejected')),
    block_hash TEXT NOT NULL DEFAULT '',
    block_height TEXT NOT NULL DEFAULT '',
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    credited_at TIMESTAMPTZ,
    UNIQUE (provider_transaction_id, blockchain_index)
);

CREATE INDEX deposits_user_created_idx ON deposits (user_id, first_seen_at DESC);
CREATE INDEX deposits_risk_queue_idx ON deposits (status, risk_status, updated_at)
    WHERE status = 'pending_risk_review';
