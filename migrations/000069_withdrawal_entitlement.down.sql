DO $$ BEGIN
    IF EXISTS(SELECT 1 FROM withdrawal_entitlement_checks) OR EXISTS(SELECT 1 FROM test_money_grants)
       OR EXISTS(SELECT 1 FROM journals WHERE withdrawal_id IS NOT NULL GROUP BY withdrawal_id HAVING count(*)>1) THEN
        RAISE EXCEPTION 'cannot remove withdrawal enforcement with recorded issuance or admissions';
    END IF;
END $$;
DROP TABLE withdrawal_entitlement_checks;
DROP FUNCTION reject_withdrawal_entitlement_mutation();
DROP INDEX withdrawals_entitlement_idx;
DROP INDEX deposits_entitlement_idx;
DROP INDEX journals_deposit_entitlement_idx;
DROP INDEX postings_entitlement_journal_idx;
ALTER TABLE test_money_control DROP COLUMN withdrawal_limits_ready;
DROP INDEX journals_withdrawal_reference_unique;
ALTER TABLE journals ADD CONSTRAINT journals_withdrawal_id_key UNIQUE(withdrawal_id);
