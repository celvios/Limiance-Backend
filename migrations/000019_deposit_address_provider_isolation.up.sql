DROP INDEX IF EXISTS deposit_addresses_active_user_asset_unique;
CREATE UNIQUE INDEX deposit_addresses_active_user_asset_wallet_unique
    ON deposit_addresses (user_id, asset_id, custody_wallet_id)
    WHERE status = 'active';
