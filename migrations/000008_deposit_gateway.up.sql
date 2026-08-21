-- Fireblocks asset identifiers are provider configuration, not application
-- defaults. An asset/network pair cannot issue a deposit address until an
-- administrator enables it and records the verified Fireblocks asset ID.
ALTER TABLE assets ADD COLUMN custody_asset_id TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX deposit_addresses_active_user_asset_unique
    ON deposit_addresses (user_id, asset_id)
    WHERE status = 'active';
