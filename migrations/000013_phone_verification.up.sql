ALTER TABLE users ADD COLUMN phone_verified_at TIMESTAMPTZ;

CREATE INDEX users_verified_phone_idx ON users (phone_e164) WHERE phone_verified_at IS NOT NULL;
