DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM staging_market_maker_treasury_limit_requests)
       OR EXISTS (SELECT 1 FROM staging_market_maker_treasury_limit_policies WHERE request_id IS NOT NULL)
       OR EXISTS (SELECT 1 FROM staging_market_maker_treasury_grants) THEN
        RAISE EXCEPTION 'refusing to erase staging market-maker treasury limit or grant history';
    END IF;
END $$;

DROP TABLE staging_market_maker_treasury_limit_control;
DROP TRIGGER staging_market_maker_treasury_limit_policy_no_truncate ON staging_market_maker_treasury_limit_policies;
DROP TRIGGER staging_market_maker_treasury_limit_policy_immutable ON staging_market_maker_treasury_limit_policies;
DROP TRIGGER staging_market_maker_treasury_limit_request_no_truncate ON staging_market_maker_treasury_limit_requests;
DROP TRIGGER staging_market_maker_treasury_limit_request_immutable ON staging_market_maker_treasury_limit_requests;
DROP TRIGGER staging_market_maker_treasury_limit_checker ON staging_market_maker_treasury_limit_policies;
DROP FUNCTION reject_staging_market_maker_treasury_limit_history_mutation();
DROP FUNCTION enforce_staging_market_maker_limit_checker();
DROP TABLE staging_market_maker_treasury_limit_policies;
DROP TABLE staging_market_maker_treasury_limit_requests;
