DO $$ BEGIN
    IF EXISTS(SELECT 1 FROM withdrawal_dispatches) THEN
        RAISE EXCEPTION 'cannot remove recorded withdrawal dispatch history';
    END IF;
END $$;
DROP TABLE withdrawal_dispatches;
DROP FUNCTION reject_withdrawal_dispatch_mutation();
