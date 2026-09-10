DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM staging_market_maker_treasury_requests) THEN
        RAISE EXCEPTION 'refusing to erase staging market-maker treasury history';
    END IF;
END $$;

DROP TRIGGER IF EXISTS staging_market_maker_treasury_grant_immutable ON staging_market_maker_treasury_grants;
DROP TRIGGER IF EXISTS staging_market_maker_treasury_grant_no_truncate ON staging_market_maker_treasury_grants;
DROP TRIGGER IF EXISTS staging_market_maker_treasury_checker ON staging_market_maker_treasury_grants;
DROP TRIGGER IF EXISTS staging_market_maker_treasury_request_immutable ON staging_market_maker_treasury_requests;
DROP TRIGGER IF EXISTS staging_market_maker_treasury_request_no_truncate ON staging_market_maker_treasury_requests;
DROP FUNCTION IF EXISTS reject_staging_market_maker_treasury_mutation();
DROP FUNCTION IF EXISTS enforce_staging_market_maker_checker();
DROP TABLE IF EXISTS staging_market_maker_treasury_grants;
DROP TABLE IF EXISTS staging_market_maker_treasury_requests;
