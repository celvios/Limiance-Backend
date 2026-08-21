DROP INDEX IF EXISTS deposit_addresses_active_user_asset_unique;
ALTER TABLE assets DROP COLUMN IF EXISTS custody_asset_id;
