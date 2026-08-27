ALTER TABLE withdrawals
    ADD COLUMN provider_transaction_id TEXT,
    ADD COLUMN transaction_hash TEXT NOT NULL DEFAULT '',
    ADD COLUMN custody_submitted_at TIMESTAMPTZ,
    ADD COLUMN custody_updated_at TIMESTAMPTZ,
    ADD COLUMN custody_error TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX withdrawals_provider_transaction_idx
    ON withdrawals (provider_transaction_id)
    WHERE provider_transaction_id IS NOT NULL;