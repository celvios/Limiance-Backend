-- History cannot be erased by a rollback. An unused installation may be removed.
DO $$ BEGIN
    IF EXISTS(SELECT 1 FROM test_money_requests) OR EXISTS(SELECT 1 FROM test_money_grants) THEN
        RAISE EXCEPTION 'cannot remove recorded test-money history';
    END IF;
END $$;
DROP TABLE test_money_grants;
DROP TABLE test_money_requests;
DROP TABLE test_money_recipients;
DROP TABLE test_money_control;
DROP TABLE test_money_policies;
DROP FUNCTION reject_test_money_mutation();
