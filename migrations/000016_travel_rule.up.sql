CREATE TABLE withdrawal_travel_rules (
    withdrawal_id UUID PRIMARY KEY REFERENCES withdrawals(id),
    ciphertext TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
