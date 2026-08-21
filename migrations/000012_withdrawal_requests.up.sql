CREATE TABLE withdrawal_addresses (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id),
    asset_id UUID NOT NULL REFERENCES assets(id),
    address TEXT NOT NULL,
    tag TEXT NOT NULL DEFAULT '',
    label TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'active', 'disabled')),
    activated_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, asset_id, address, tag)
);

CREATE TABLE withdrawals (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id),
    account_id UUID NOT NULL REFERENCES accounts(id),
    asset_id UUID NOT NULL REFERENCES assets(id),
    withdrawal_address_id UUID NOT NULL REFERENCES withdrawal_addresses(id),
    amount_atomic NUMERIC(78,0) NOT NULL CHECK (amount_atomic > 0),
    idempotency_key TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL CHECK (status IN ('requested', 'pending_approval', 'approved', 'submitted', 'completed', 'rejected', 'cancelled', 'failed')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (id, user_id)
);

ALTER TABLE journals ADD COLUMN withdrawal_id UUID UNIQUE REFERENCES withdrawals(id);
CREATE INDEX withdrawals_user_created_idx ON withdrawals (user_id, created_at DESC);
