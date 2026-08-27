DROP INDEX IF EXISTS withdrawals_provider_transaction_idx;

ALTER TABLE withdrawals
    DROP COLUMN IF EXISTS provider_transaction_id,
    DROP COLUMN IF EXISTS transaction_hash,
    DROP COLUMN IF EXISTS custody_submitted_at,
    DROP COLUMN IF EXISTS custody_updated_at,
    DROP COLUMN IF EXISTS custody_error;