ALTER TABLE withdrawals
    ALTER COLUMN withdrawal_address_id DROP NOT NULL,
    ADD COLUMN destination_address TEXT,
    ADD COLUMN destination_tag TEXT NOT NULL DEFAULT '';

UPDATE withdrawals w
SET destination_address = wa.address,
    destination_tag = wa.tag
FROM withdrawal_addresses wa
WHERE wa.id = w.withdrawal_address_id;

ALTER TABLE withdrawals
    ALTER COLUMN destination_address SET NOT NULL;
