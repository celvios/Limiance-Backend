CREATE TABLE session_account_context (
    session_id UUID PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
    account_id UUID NOT NULL REFERENCES accounts(id),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX session_account_context_account_idx ON session_account_context (account_id);
