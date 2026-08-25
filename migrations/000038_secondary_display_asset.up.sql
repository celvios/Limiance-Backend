ALTER TABLE users
    ADD COLUMN secondary_display_asset TEXT NOT NULL DEFAULT 'BTC';

ALTER TABLE users
    ADD CONSTRAINT users_secondary_display_asset_check
    CHECK (secondary_display_asset IN ('', 'BTC', 'ETH', 'USDT'));
