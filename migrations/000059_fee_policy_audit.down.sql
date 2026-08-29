DROP TABLE IF EXISTS fee_policy_audit_history;
DROP TABLE IF EXISTS fee_tier_audit_history;

ALTER TABLE trades ALTER COLUMN price TYPE NUMERIC(36,18);
ALTER TABLE trades ALTER COLUMN quantity TYPE NUMERIC(36,18);
ALTER TABLE user_volume_30d ALTER COLUMN volume_usd TYPE NUMERIC(36,18);
ALTER TABLE vip_tiers ALTER COLUMN min_30d_volume TYPE NUMERIC(36,18);

ALTER TABLE vip_tiers DROP CONSTRAINT vip_tiers_maker_fee_bps_check;
ALTER TABLE vip_tiers DROP CONSTRAINT vip_tiers_taker_fee_bps_check;
UPDATE vip_tiers SET maker_fee_bps=0 WHERE maker_fee_bps<0;
ALTER TABLE vip_tiers
    ADD CONSTRAINT vip_tiers_maker_fee_bps_check CHECK (maker_fee_bps >= 0),
    ADD CONSTRAINT vip_tiers_taker_fee_bps_check CHECK (taker_fee_bps >= 0);
