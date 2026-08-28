ALTER TABLE withdrawals
    DROP COLUMN IF EXISTS destination_tag,
    DROP COLUMN IF EXISTS destination_address;

ALTER TABLE withdrawals
    ALTER COLUMN withdrawal_address_id SET NOT NULL;
