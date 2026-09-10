CREATE TABLE staging_market_maker_treasury_requests (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    proposed_by UUID NOT NULL REFERENCES users(id),
    asset_id UUID NOT NULL REFERENCES assets(id),
    amount_atomic NUMERIC(78,0) NOT NULL CHECK (amount_atomic > 0),
    reason TEXT NOT NULL CHECK (length(reason) BETWEEN 8 AND 1000),
    idempotency_key TEXT NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 255),
    request_hash BYTEA NOT NULL CHECK (octet_length(request_hash) = 32),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (proposed_by,idempotency_key)
);

CREATE TABLE staging_market_maker_treasury_grants (
    request_id UUID PRIMARY KEY REFERENCES staging_market_maker_treasury_requests(id),
    approved_by UUID NOT NULL REFERENCES users(id),
    approval_key TEXT NOT NULL CHECK (length(approval_key) BETWEEN 1 AND 255),
    journal_id UUID NOT NULL UNIQUE REFERENCES journals(id),
    value_usdt_atomic NUMERIC(78,0) NOT NULL CHECK (value_usdt_atomic > 0),
    evidence JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (approved_by,approval_key)
);

CREATE FUNCTION enforce_staging_market_maker_checker() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM staging_market_maker_treasury_requests
        WHERE id=NEW.request_id AND proposed_by=NEW.approved_by
    ) THEN
        RAISE EXCEPTION 'staging market-maker treasury proposer and approver must differ';
    END IF;
    RETURN NEW;
END $$;

CREATE TRIGGER staging_market_maker_treasury_checker
BEFORE INSERT ON staging_market_maker_treasury_grants
FOR EACH ROW EXECUTE FUNCTION enforce_staging_market_maker_checker();

CREATE FUNCTION reject_staging_market_maker_treasury_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'staging market-maker treasury history is immutable';
END $$;

CREATE TRIGGER staging_market_maker_treasury_request_immutable
BEFORE UPDATE OR DELETE ON staging_market_maker_treasury_requests
FOR EACH ROW EXECUTE FUNCTION reject_staging_market_maker_treasury_mutation();

CREATE TRIGGER staging_market_maker_treasury_request_no_truncate
BEFORE TRUNCATE ON staging_market_maker_treasury_requests
FOR EACH STATEMENT EXECUTE FUNCTION reject_staging_market_maker_treasury_mutation();

CREATE TRIGGER staging_market_maker_treasury_grant_immutable
BEFORE UPDATE OR DELETE ON staging_market_maker_treasury_grants
FOR EACH ROW EXECUTE FUNCTION reject_staging_market_maker_treasury_mutation();

CREATE TRIGGER staging_market_maker_treasury_grant_no_truncate
BEFORE TRUNCATE ON staging_market_maker_treasury_grants
FOR EACH STATEMENT EXECUTE FUNCTION reject_staging_market_maker_treasury_mutation();

COMMENT ON TABLE staging_market_maker_treasury_grants IS
'Staging-only internal issuance; never proof of deposits or custody backing and never withdrawal entitlement.';
