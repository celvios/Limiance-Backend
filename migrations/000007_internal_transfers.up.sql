CREATE TABLE transfers (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    sender_user_id UUID NOT NULL REFERENCES users(id),
    source_account_id UUID NOT NULL REFERENCES accounts(id),
    destination_account_id UUID NOT NULL REFERENCES accounts(id),
    asset_id UUID NOT NULL REFERENCES assets(id),
    amount_atomic NUMERIC(78,0) NOT NULL CHECK (amount_atomic > 0),
    idempotency_key TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL DEFAULT 'posted',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX transfers_sender_created_idx ON transfers (sender_user_id, created_at DESC);
ALTER TABLE journals ADD COLUMN transfer_id UUID UNIQUE REFERENCES transfers(id);
