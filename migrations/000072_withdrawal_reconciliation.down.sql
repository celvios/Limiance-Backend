DO $$ BEGIN
    IF EXISTS(SELECT 1 FROM withdrawal_reconciliation_observations) THEN
        RAISE EXCEPTION 'cannot remove withdrawal reconciliation history';
    END IF;
END $$;
DROP TABLE withdrawal_reconciliation_observations;
DROP FUNCTION reject_withdrawal_reconciliation_mutation();
