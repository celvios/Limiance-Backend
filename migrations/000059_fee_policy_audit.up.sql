ALTER TABLE vip_tiers DROP CONSTRAINT vip_tiers_maker_fee_bps_check;
ALTER TABLE vip_tiers DROP CONSTRAINT vip_tiers_taker_fee_bps_check;
ALTER TABLE vip_tiers
    ADD CONSTRAINT vip_tiers_maker_fee_bps_check CHECK (maker_fee_bps BETWEEN -10000 AND 10000),
    ADD CONSTRAINT vip_tiers_taker_fee_bps_check CHECK (taker_fee_bps BETWEEN 0 AND 10000);

ALTER TABLE vip_tiers ALTER COLUMN min_30d_volume TYPE NUMERIC(24,8);
ALTER TABLE user_volume_30d ALTER COLUMN volume_usd TYPE NUMERIC(24,8);
ALTER TABLE trades ALTER COLUMN quantity TYPE NUMERIC(24,8);
ALTER TABLE trades ALTER COLUMN price TYPE NUMERIC(24,8);

CREATE TABLE fee_tier_audit_history (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id),
    previous_tier_level INTEGER NOT NULL,
    new_tier_level INTEGER NOT NULL REFERENCES vip_tiers(level),
    rolling_volume_usd NUMERIC(24,8) NOT NULL CHECK (rolling_volume_usd >= 0),
    reason TEXT NOT NULL DEFAULT 'rolling_30d_volume',
    changed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (previous_tier_level <> new_tier_level)
);

CREATE TABLE fee_policy_audit_history (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tier_level INTEGER NOT NULL REFERENCES vip_tiers(level),
    actor_id UUID REFERENCES users(id),
    maker_fee_bps INTEGER NOT NULL CHECK (maker_fee_bps BETWEEN -10000 AND 10000),
    taker_fee_bps INTEGER NOT NULL CHECK (taker_fee_bps BETWEEN 0 AND 10000),
    min_30d_volume NUMERIC(24,8) NOT NULL CHECK (min_30d_volume >= 0),
    reason TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX fee_tier_audit_user_changed_idx ON fee_tier_audit_history (user_id, changed_at DESC);
CREATE INDEX fee_policy_audit_tier_created_idx ON fee_policy_audit_history (tier_level, created_at DESC);
