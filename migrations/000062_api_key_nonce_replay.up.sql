CREATE TABLE api_key_nonces (
    key_hash BYTEA NOT NULL REFERENCES api_keys(key_hash),
    nonce_hash BYTEA NOT NULL CHECK (octet_length(nonce_hash) = 32),
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (key_hash, nonce_hash)
);

CREATE INDEX api_key_nonces_expiry_idx ON api_key_nonces (expires_at);
