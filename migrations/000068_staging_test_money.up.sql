CREATE TABLE test_money_policies (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    policy JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE test_money_control (
    singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    enabled BOOLEAN NOT NULL DEFAULT FALSE,
    environment TEXT NOT NULL DEFAULT '',
    policy_id UUID REFERENCES test_money_policies(id)
);
INSERT INTO test_money_control(singleton) VALUES(TRUE);
CREATE TABLE test_money_recipients (
    user_id UUID PRIMARY KEY REFERENCES users(id),
    enabled BOOLEAN NOT NULL DEFAULT FALSE
);
CREATE TABLE test_money_requests (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    proposed_by UUID NOT NULL REFERENCES users(id),
    recipient_id UUID NOT NULL REFERENCES users(id),
    account_id UUID NOT NULL REFERENCES accounts(id),
    asset_id UUID NOT NULL REFERENCES assets(id),
    amount_atomic NUMERIC(78,0) NOT NULL CHECK (amount_atomic>0),
    reason TEXT NOT NULL CHECK (length(reason) BETWEEN 8 AND 1000),
    policy_id UUID NOT NULL REFERENCES test_money_policies(id),
    idempotency_key TEXT NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 255),
    request_hash BYTEA NOT NULL CHECK (octet_length(request_hash)=32),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(proposed_by,idempotency_key)
);
CREATE TABLE test_money_grants (
    request_id UUID PRIMARY KEY REFERENCES test_money_requests(id),
    approved_by UUID NOT NULL REFERENCES users(id),
    approval_key TEXT NOT NULL CHECK (length(approval_key) BETWEEN 1 AND 255),
    journal_id UUID NOT NULL UNIQUE REFERENCES journals(id),
    value_usdt_atomic NUMERIC(78,0) NOT NULL CHECK (value_usdt_atomic>0),
    evidence JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(approved_by,approval_key)
);
CREATE FUNCTION reject_test_money_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'test-money history is immutable';
END $$;
CREATE TRIGGER test_money_policy_immutable BEFORE UPDATE OR DELETE ON test_money_policies
FOR EACH ROW EXECUTE FUNCTION reject_test_money_mutation();
CREATE TRIGGER test_money_request_immutable BEFORE UPDATE OR DELETE ON test_money_requests
FOR EACH ROW EXECUTE FUNCTION reject_test_money_mutation();
CREATE TRIGGER test_money_grant_immutable BEFORE UPDATE OR DELETE ON test_money_grants
FOR EACH ROW EXECUTE FUNCTION reject_test_money_mutation();
COMMENT ON TABLE test_money_grants IS 'Internal issuance; never proof of a deposit or custody backing. USDT value uses scale 8.';
