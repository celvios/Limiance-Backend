DROP INDEX IF EXISTS api_keys_account_active_idx;
DROP INDEX IF EXISTS sessions_account_active_idx;
ALTER TABLE sessions DROP COLUMN IF EXISTS account_id;
ALTER TABLE sessions DROP COLUMN IF EXISTS principal_type;
ALTER TABLE api_keys DROP CONSTRAINT IF EXISTS api_keys_account_fk;
ALTER TABLE api_keys DROP COLUMN IF EXISTS account_id;