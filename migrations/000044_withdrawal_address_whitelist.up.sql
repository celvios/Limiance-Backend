ALTER TABLE withdrawal_addresses
    ADD COLUMN whitelisted BOOLEAN NOT NULL DEFAULT FALSE;

CREATE INDEX withdrawal_addresses_active_whitelist_idx
    ON withdrawal_addresses (user_id, asset_id, address, tag)
    WHERE status = 'active' AND whitelisted = TRUE;
