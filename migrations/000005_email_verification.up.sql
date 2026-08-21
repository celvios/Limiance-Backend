CREATE TABLE email_verification_challenges (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id),
    code_hash BYTEA NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    attempts SMALLINT NOT NULL DEFAULT 0 CHECK (attempts >= 0 AND attempts <= 5),
    consumed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX email_verification_active_user_idx ON email_verification_challenges (user_id, created_at DESC) WHERE consumed_at IS NULL;
