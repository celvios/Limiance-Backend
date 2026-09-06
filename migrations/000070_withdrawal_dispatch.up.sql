-- A committed intent means custody MAY have received the request. It is never
-- expired, deleted or made eligible for blind resubmission after a lost ACK.
CREATE TABLE withdrawal_dispatches (
    withdrawal_id UUID PRIMARY KEY REFERENCES withdrawals(id),
    provider TEXT NOT NULL CHECK(provider<>''),
    request JSONB NOT NULL CHECK(jsonb_typeof(request)='object'),
    entitlement_enforced BOOLEAN NOT NULL,
    credited_deposits_atomic NUMERIC(78,0),
    committed_withdrawals_atomic NUMERIC(78,0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (NOT entitlement_enforced OR
        (credited_deposits_atomic IS NOT NULL AND committed_withdrawals_atomic IS NOT NULL
         AND credited_deposits_atomic>=committed_withdrawals_atomic
         AND committed_withdrawals_atomic>=0))
);
CREATE FUNCTION reject_withdrawal_dispatch_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'withdrawal dispatch history is immutable';
END $$;
CREATE TRIGGER withdrawal_dispatch_immutable BEFORE UPDATE OR DELETE ON withdrawal_dispatches
FOR EACH ROW EXECUTE FUNCTION reject_withdrawal_dispatch_mutation();
CREATE TRIGGER withdrawal_dispatch_no_truncate BEFORE TRUNCATE ON withdrawal_dispatches
FOR EACH STATEMENT EXECUTE FUNCTION reject_withdrawal_dispatch_mutation();
