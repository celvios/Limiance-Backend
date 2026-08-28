ALTER TABLE api_keys
    ADD COLUMN account_id UUID;

UPDATE api_keys k
SET account_id = a.id
FROM accounts a
WHERE a.user_id = k.user_id
  AND a.kind = 'funding'
  AND a.status = 'active';

ALTER TABLE api_keys
    ALTER COLUMN account_id SET NOT NULL,
    ADD CONSTRAINT api_keys_account_fk FOREIGN KEY (account_id) REFERENCES accounts(id);

CREATE INDEX api_keys_account_active_idx
    ON api_keys (account_id, created_at DESC)
    WHERE revoked_at IS NULL;

ALTER TABLE sessions
    ADD COLUMN principal_type TEXT NOT NULL DEFAULT 'session'
        CHECK (principal_type IN ('session', 'subaccount')),
    ADD COLUMN account_id UUID REFERENCES accounts(id);

CREATE INDEX sessions_account_active_idx
    ON sessions (account_id, expires_at)
    WHERE revoked_at IS NULL;