CREATE TABLE api_keys (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id),
    name TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 80),
    key_hash BYTEA NOT NULL UNIQUE,
    secret_ciphertext TEXT NOT NULL,
    scope TEXT NOT NULL CHECK (scope IN ('read_only', 'trade', 'withdraw')),
    ip_whitelist TEXT[] NOT NULL DEFAULT '{}',
    last_used_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at TIMESTAMPTZ
);

CREATE INDEX api_keys_user_active_idx
    ON api_keys (user_id, created_at DESC)
    WHERE revoked_at IS NULL;
