CREATE TABLE staging_market_maker_treasury_limit_requests (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    proposed_by UUID NOT NULL REFERENCES users(id),
    limit_usdt_atomic NUMERIC(78,0) NOT NULL CHECK (limit_usdt_atomic > 0),
    reason TEXT NOT NULL CHECK (length(reason) BETWEEN 8 AND 1000),
    idempotency_key TEXT NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 255),
    request_hash BYTEA NOT NULL CHECK (octet_length(request_hash) = 32),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (proposed_by, idempotency_key)
);

CREATE TABLE staging_market_maker_treasury_limit_policies (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    version BIGINT NOT NULL UNIQUE CHECK (version > 0),
    request_id UUID UNIQUE REFERENCES staging_market_maker_treasury_limit_requests(id),
    limit_usdt_atomic NUMERIC(78,0) NOT NULL CHECK (limit_usdt_atomic > 0),
    approved_by UUID REFERENCES users(id),
    approval_key TEXT UNIQUE,
    reason TEXT NOT NULL CHECK (length(reason) BETWEEN 8 AND 1000),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((request_id IS NULL AND approved_by IS NULL AND approval_key IS NULL)
        OR (request_id IS NOT NULL AND approved_by IS NOT NULL AND length(approval_key) BETWEEN 1 AND 255))
);

CREATE TABLE staging_market_maker_treasury_limit_control (
    singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    policy_id UUID NOT NULL REFERENCES staging_market_maker_treasury_limit_policies(id),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

WITH bootstrap AS (
    INSERT INTO staging_market_maker_treasury_limit_policies(
        version, limit_usdt_atomic, reason
    ) VALUES (1, 1000000000000, 'bootstrap bounded staging market-maker treasury limit')
    RETURNING id
)
INSERT INTO staging_market_maker_treasury_limit_control(singleton, policy_id)
SELECT TRUE, id FROM bootstrap;

CREATE FUNCTION enforce_staging_market_maker_limit_checker() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.request_id IS NOT NULL AND EXISTS (
        SELECT 1 FROM staging_market_maker_treasury_limit_requests
        WHERE id=NEW.request_id AND proposed_by=NEW.approved_by
    ) THEN
        RAISE EXCEPTION 'staging market-maker treasury limit proposer and approver must differ';
    END IF;
    RETURN NEW;
END $$;

CREATE TRIGGER staging_market_maker_treasury_limit_checker
BEFORE INSERT ON staging_market_maker_treasury_limit_policies
FOR EACH ROW EXECUTE FUNCTION enforce_staging_market_maker_limit_checker();

CREATE FUNCTION reject_staging_market_maker_treasury_limit_history_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'staging market-maker treasury limit history is immutable';
END $$;

CREATE TRIGGER staging_market_maker_treasury_limit_request_immutable
BEFORE UPDATE OR DELETE ON staging_market_maker_treasury_limit_requests
FOR EACH ROW EXECUTE FUNCTION reject_staging_market_maker_treasury_limit_history_mutation();

CREATE TRIGGER staging_market_maker_treasury_limit_request_no_truncate
BEFORE TRUNCATE ON staging_market_maker_treasury_limit_requests
FOR EACH STATEMENT EXECUTE FUNCTION reject_staging_market_maker_treasury_limit_history_mutation();

CREATE TRIGGER staging_market_maker_treasury_limit_policy_immutable
BEFORE UPDATE OR DELETE ON staging_market_maker_treasury_limit_policies
FOR EACH ROW EXECUTE FUNCTION reject_staging_market_maker_treasury_limit_history_mutation();

CREATE TRIGGER staging_market_maker_treasury_limit_policy_no_truncate
BEFORE TRUNCATE ON staging_market_maker_treasury_limit_policies
FOR EACH STATEMENT EXECUTE FUNCTION reject_staging_market_maker_treasury_limit_history_mutation();

COMMENT ON TABLE staging_market_maker_treasury_limit_policies IS
'Immutable maker-checker versions of the aggregate staging-only treasury issuance ceiling.';
