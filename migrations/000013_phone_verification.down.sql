DROP INDEX IF EXISTS users_verified_phone_idx;
ALTER TABLE users DROP COLUMN IF EXISTS phone_verified_at;
