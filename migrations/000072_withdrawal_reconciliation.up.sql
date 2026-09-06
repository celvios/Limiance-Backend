CREATE TABLE withdrawal_reconciliation_observations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    withdrawal_id UUID NOT NULL REFERENCES withdrawal_dispatches(withdrawal_id),
    provider TEXT NOT NULL CHECK(provider<>''),
    found BOOLEAN NOT NULL,
    provider_transaction_id TEXT NOT NULL DEFAULT '',
    provider_status TEXT NOT NULL DEFAULT '',
    transaction_hash TEXT NOT NULL DEFAULT '',
    observed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK((found AND provider_transaction_id<>'') OR
          (NOT found AND provider_transaction_id='' AND provider_status='' AND transaction_hash=''))
);

CREATE INDEX withdrawal_reconciliation_open_idx
    ON withdrawal_reconciliation_observations(withdrawal_id,observed_at DESC);

CREATE FUNCTION reject_withdrawal_reconciliation_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'withdrawal reconciliation history is immutable';
END $$;
CREATE TRIGGER withdrawal_reconciliation_immutable
BEFORE UPDATE OR DELETE ON withdrawal_reconciliation_observations
FOR EACH ROW EXECUTE FUNCTION reject_withdrawal_reconciliation_mutation();
CREATE TRIGGER withdrawal_reconciliation_no_truncate
BEFORE TRUNCATE ON withdrawal_reconciliation_observations
FOR EACH STATEMENT EXECUTE FUNCTION reject_withdrawal_reconciliation_mutation();
