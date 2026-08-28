UPDATE operational_controls
SET enabled = false,
    updated_at = now()
WHERE control_key = 'automatic_withdrawals';
