DROP INDEX IF EXISTS withdrawal_addresses_active_whitelist_idx;
ALTER TABLE withdrawal_addresses DROP COLUMN IF EXISTS whitelisted;
