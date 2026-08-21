CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS citext;

CREATE TYPE account_kind AS ENUM ('funding', 'uta', 'subaccount', 'system');
CREATE TYPE balance_bucket AS ENUM ('available', 'held', 'pending', 'locked');
CREATE TYPE journal_status AS ENUM ('posted', 'reversed');

CREATE TABLE users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    uid BIGINT GENERATED ALWAYS AS IDENTITY UNIQUE NOT NULL,
    email CITEXT UNIQUE,
    phone_e164 TEXT UNIQUE,
    password_hash TEXT NOT NULL,
    country_code CHAR(2) NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending_verification',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (email IS NOT NULL OR phone_e164 IS NOT NULL)
);

CREATE TABLE accounts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID REFERENCES users(id),
    kind account_kind NOT NULL,
    name TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE assets (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    symbol TEXT NOT NULL,
    network TEXT NOT NULL,
    contract_address TEXT NOT NULL DEFAULT '',
    decimals SMALLINT NOT NULL CHECK (decimals >= 0 AND decimals <= 36),
    status TEXT NOT NULL DEFAULT 'disabled',
    min_deposit_atomic NUMERIC(78,0),
    confirmations_required INTEGER NOT NULL DEFAULT 1,
    UNIQUE (symbol, network, contract_address)
);

CREATE TABLE journals (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    idempotency_key TEXT NOT NULL UNIQUE,
    reference_type TEXT NOT NULL,
    reference_id TEXT NOT NULL,
    status journal_status NOT NULL DEFAULT 'posted',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE postings (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    journal_id UUID NOT NULL REFERENCES journals(id),
    account_id UUID NOT NULL REFERENCES accounts(id),
    asset_id UUID NOT NULL REFERENCES assets(id),
    bucket balance_bucket NOT NULL,
    direction TEXT NOT NULL CHECK (direction IN ('debit', 'credit')),
    amount_atomic NUMERIC(78,0) NOT NULL CHECK (amount_atomic > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE outbox_events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    event_type TEXT NOT NULL,
    aggregate_type TEXT NOT NULL,
    aggregate_id TEXT NOT NULL,
    payload JSONB NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ,
    attempts INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE audit_events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    actor_id UUID REFERENCES users(id),
    actor_type TEXT NOT NULL,
    action TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    resource_id TEXT NOT NULL,
    reason TEXT,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX postings_account_asset_idx ON postings (account_id, asset_id, created_at);
CREATE INDEX outbox_unpublished_idx ON outbox_events (occurred_at) WHERE published_at IS NULL;
CREATE INDEX audit_resource_idx ON audit_events (resource_type, resource_id, occurred_at DESC);
