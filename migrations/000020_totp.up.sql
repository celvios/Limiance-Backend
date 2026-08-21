CREATE TABLE totp_credentials (
    user_id UUID PRIMARY KEY REFERENCES users(id),
    secret_ciphertext TEXT NOT NULL,
    enabled_at TIMESTAMPTZ,
    disabled_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE mfa_login_challenges (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id),
    token_hash BYTEA NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX mfa_login_challenges_active_idx
    ON mfa_login_challenges (token_hash, expires_at)
    WHERE consumed_at IS NULL;
