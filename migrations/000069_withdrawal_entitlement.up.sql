ALTER TABLE test_money_control ADD COLUMN withdrawal_limits_ready BOOLEAN NOT NULL DEFAULT FALSE;

-- A withdrawal has a hold and a later settlement/release, all append-only.
ALTER TABLE journals DROP CONSTRAINT journals_withdrawal_id_key;
CREATE UNIQUE INDEX journals_withdrawal_reference_unique
    ON journals(withdrawal_id,reference_type) WHERE withdrawal_id IS NOT NULL;

CREATE TABLE withdrawal_entitlement_checks (
    withdrawal_id UUID PRIMARY KEY REFERENCES withdrawals(id),
    user_id UUID NOT NULL REFERENCES users(id),
    asset_id UUID NOT NULL REFERENCES assets(id),
    amount_atomic NUMERIC(78,0) NOT NULL CHECK(amount_atomic>0),
    credited_deposits_atomic NUMERIC(78,0) NOT NULL CHECK(credited_deposits_atomic>=0),
    prior_commitments_atomic NUMERIC(78,0) NOT NULL CHECK(prior_commitments_atomic>=0),
    checked_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK(prior_commitments_atomic+amount_atomic<=credited_deposits_atomic)
);
CREATE FUNCTION reject_withdrawal_entitlement_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'withdrawal entitlement admission history is immutable';
END $$;
CREATE TRIGGER withdrawal_entitlement_immutable BEFORE UPDATE OR DELETE ON withdrawal_entitlement_checks
FOR EACH ROW EXECUTE FUNCTION reject_withdrawal_entitlement_mutation();
CREATE TRIGGER withdrawal_entitlement_no_truncate BEFORE TRUNCATE ON withdrawal_entitlement_checks
FOR EACH STATEMENT EXECUTE FUNCTION reject_withdrawal_entitlement_mutation();
CREATE INDEX withdrawals_entitlement_idx ON withdrawals(user_id,asset_id,status);
CREATE INDEX deposits_entitlement_idx ON deposits(user_id,asset_id) WHERE status='credited';
CREATE INDEX journals_deposit_entitlement_idx ON journals(reference_id)
    WHERE reference_type='deposit_credit' AND status='posted';
CREATE INDEX postings_entitlement_journal_idx ON postings(journal_id);
