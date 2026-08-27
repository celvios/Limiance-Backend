CREATE TABLE external_identities (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider TEXT NOT NULL,
    subject TEXT NOT NULL,
    email TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (provider, subject),
    UNIQUE (user_id, provider)
);

CREATE TABLE oauth_states (
    state_hash BYTEA PRIMARY KEY,
    provider TEXT NOT NULL,
    redirect_url TEXT NOT NULL,
    code_verifier TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ
);

CREATE INDEX oauth_states_expiry_idx ON oauth_states (expires_at) WHERE consumed_at IS NULL;