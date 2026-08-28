UPDATE operational_controls
SET enabled = true,
    updated_at = now()
WHERE control_key = 'automatic_withdrawals';
