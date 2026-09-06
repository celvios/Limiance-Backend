CREATE TABLE custody_withdrawal_routes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    asset_id UUID NOT NULL REFERENCES assets(id),
    provider TEXT NOT NULL CHECK(provider<>''),
    environment TEXT NOT NULL CHECK(environment IN ('staging','production')),
    network TEXT NOT NULL CHECK(network<>''),
    provider_asset_id TEXT NOT NULL CHECK(provider_asset_id<>''),
    asset_decimals SMALLINT NOT NULL CHECK(asset_decimals BETWEEN 0 AND 36),
    fee_provider_asset_id TEXT NOT NULL CHECK(fee_provider_asset_id<>''),
    fee_asset_decimals SMALLINT NOT NULL CHECK(fee_asset_decimals BETWEEN 0 AND 36),
    CHECK(provider_asset_id<>fee_provider_asset_id OR asset_decimals=fee_asset_decimals),
    max_fee_atomic NUMERIC(78,0) NOT NULL CHECK(max_fee_atomic>=0),
    max_observation_age_seconds INTEGER NOT NULL CHECK(max_observation_age_seconds BETWEEN 1 AND 30),
    created_by UUID NOT NULL REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE custody_withdrawal_route_requests (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    route_id UUID NOT NULL REFERENCES custody_withdrawal_routes(id),
    action TEXT NOT NULL CHECK(action IN ('enable','disable')),
    proposed_by UUID NOT NULL REFERENCES users(id),
    reason TEXT NOT NULL CHECK(length(reason) BETWEEN 8 AND 500),
    idempotency_key TEXT NOT NULL,
    payload_hash BYTEA NOT NULL CHECK(octet_length(payload_hash)=32),
    proposed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(proposed_by,idempotency_key),
    UNIQUE(id,route_id,proposed_by)
);

CREATE TABLE custody_withdrawal_route_approvals (
    request_id UUID PRIMARY KEY,
    route_id UUID NOT NULL,
    proposed_by UUID NOT NULL,
    approved_by UUID NOT NULL REFERENCES users(id),
    reason TEXT NOT NULL CHECK(length(reason) BETWEEN 8 AND 500),
    idempotency_key TEXT NOT NULL,
    approved_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY(request_id,route_id,proposed_by)
        REFERENCES custody_withdrawal_route_requests(id,route_id,proposed_by),
    CHECK(approved_by<>proposed_by),
    UNIQUE(approved_by,idempotency_key)
);

ALTER TABLE withdrawal_dispatches
    ADD COLUMN capacity_route_id UUID REFERENCES custody_withdrawal_routes(id);

CREATE TABLE custody_capacity_reservations (
    withdrawal_id UUID PRIMARY KEY REFERENCES withdrawal_dispatches(withdrawal_id),
    route_id UUID NOT NULL REFERENCES custody_withdrawal_routes(id),
    provider TEXT NOT NULL CHECK(provider<>''),
    source_vault_id TEXT NOT NULL CHECK(source_vault_id<>''),
    provider_asset_id TEXT NOT NULL CHECK(provider_asset_id<>''),
    asset_amount_atomic NUMERIC(78,0) NOT NULL CHECK(asset_amount_atomic>0),
    asset_available_atomic NUMERIC(78,0) NOT NULL CHECK(asset_available_atomic>=0),
    fee_provider_asset_id TEXT NOT NULL CHECK(fee_provider_asset_id<>''),
    fee_amount_atomic NUMERIC(78,0) NOT NULL CHECK(fee_amount_atomic>=0),
    fee_available_atomic NUMERIC(78,0) NOT NULL CHECK(fee_available_atomic>=0),
    observed_at TIMESTAMPTZ NOT NULL,
    asset_block_height TEXT NOT NULL DEFAULT '',
    asset_block_hash TEXT NOT NULL DEFAULT '',
    fee_block_height TEXT NOT NULL DEFAULT '',
    fee_block_hash TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE custody_capacity_terminal_events (
    withdrawal_id UUID PRIMARY KEY REFERENCES custody_capacity_reservations(withdrawal_id),
    outcome TEXT NOT NULL CHECK(outcome IN ('consumed','released')),
    provider_transaction_id TEXT NOT NULL CHECK(provider_transaction_id<>''),
    observed_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE FUNCTION reject_custody_capacity_history_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'custody capacity history is immutable';
END $$;
CREATE TRIGGER custody_routes_immutable BEFORE UPDATE OR DELETE ON custody_withdrawal_routes
FOR EACH ROW EXECUTE FUNCTION reject_custody_capacity_history_mutation();
CREATE TRIGGER custody_route_requests_immutable BEFORE UPDATE OR DELETE ON custody_withdrawal_route_requests
FOR EACH ROW EXECUTE FUNCTION reject_custody_capacity_history_mutation();
CREATE TRIGGER custody_route_approvals_immutable BEFORE UPDATE OR DELETE ON custody_withdrawal_route_approvals
FOR EACH ROW EXECUTE FUNCTION reject_custody_capacity_history_mutation();
CREATE TRIGGER custody_reservations_immutable BEFORE UPDATE OR DELETE ON custody_capacity_reservations
FOR EACH ROW EXECUTE FUNCTION reject_custody_capacity_history_mutation();
CREATE TRIGGER custody_terminal_events_immutable BEFORE UPDATE OR DELETE ON custody_capacity_terminal_events
FOR EACH ROW EXECUTE FUNCTION reject_custody_capacity_history_mutation();
CREATE TRIGGER custody_routes_no_truncate BEFORE TRUNCATE ON custody_withdrawal_routes
FOR EACH STATEMENT EXECUTE FUNCTION reject_custody_capacity_history_mutation();
CREATE TRIGGER custody_route_requests_no_truncate BEFORE TRUNCATE ON custody_withdrawal_route_requests
FOR EACH STATEMENT EXECUTE FUNCTION reject_custody_capacity_history_mutation();
CREATE TRIGGER custody_route_approvals_no_truncate BEFORE TRUNCATE ON custody_withdrawal_route_approvals
FOR EACH STATEMENT EXECUTE FUNCTION reject_custody_capacity_history_mutation();
CREATE TRIGGER custody_reservations_no_truncate BEFORE TRUNCATE ON custody_capacity_reservations
FOR EACH STATEMENT EXECUTE FUNCTION reject_custody_capacity_history_mutation();
CREATE TRIGGER custody_terminal_events_no_truncate BEFORE TRUNCATE ON custody_capacity_terminal_events
FOR EACH STATEMENT EXECUTE FUNCTION reject_custody_capacity_history_mutation();

CREATE INDEX custody_capacity_asset_open_idx
    ON custody_capacity_reservations(provider,source_vault_id,provider_asset_id);
CREATE INDEX custody_capacity_fee_open_idx
    ON custody_capacity_reservations(provider,source_vault_id,fee_provider_asset_id);
