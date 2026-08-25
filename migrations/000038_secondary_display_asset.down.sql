ALTER TABLE users
    DROP CONSTRAINT IF EXISTS users_secondary_display_asset_check;

ALTER TABLE users
    DROP COLUMN IF EXISTS secondary_display_asset;
