CREATE TYPE kyc_status AS ENUM ('not_started', 'pending', 'approved', 'rejected', 'on_hold', 'expired');

CREATE TABLE kyc_profiles (
    user_id UUID PRIMARY KEY REFERENCES users(id),
    provider TEXT NOT NULL DEFAULT 'sumsub',
    applicant_id TEXT UNIQUE,
    level_name TEXT,
    tier SMALLINT NOT NULL DEFAULT 0 CHECK (tier BETWEEN 0 AND 2),
    status kyc_status NOT NULL DEFAULT 'not_started',
    review_answer TEXT,
    review_reject_type TEXT,
    reviewed_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE custody_wallets (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL UNIQUE REFERENCES users(id),
    provider TEXT NOT NULL DEFAULT 'fireblocks',
    external_vault_id TEXT UNIQUE,
    status TEXT NOT NULL DEFAULT 'pending',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE deposit_addresses (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id),
    asset_id UUID NOT NULL REFERENCES assets(id),
    custody_wallet_id UUID NOT NULL REFERENCES custody_wallets(id),
    provider_address_id TEXT,
    address TEXT NOT NULL,
    tag TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    retired_at TIMESTAMPTZ,
    UNIQUE (asset_id, address, tag)
);

CREATE TABLE webhook_receipts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    provider TEXT NOT NULL,
    delivery_id TEXT,
    payload_digest TEXT NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    processed_at TIMESTAMPTZ,
    UNIQUE (provider, payload_digest)
);

CREATE INDEX deposit_addresses_user_asset_idx ON deposit_addresses (user_id, asset_id) WHERE status = 'active';
