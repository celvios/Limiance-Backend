ALTER TABLE market_maker_activation_requests
    DROP CONSTRAINT market_maker_activation_requests_action_check;

ALTER TABLE market_maker_activation_requests
    ADD CONSTRAINT market_maker_activation_requests_action_check
    CHECK (action IN ('configure_reference_only','configure_dry_run','release_live'));

COMMENT ON TABLE market_maker_activation_requests IS 'Maker-checker requests for reference-only evaluation, funded dry-run configuration, and live release.';
