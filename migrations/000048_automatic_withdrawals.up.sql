INSERT INTO operational_controls (control_key, enabled)
VALUES ('automatic_withdrawals', false)
ON CONFLICT (control_key) DO NOTHING;
