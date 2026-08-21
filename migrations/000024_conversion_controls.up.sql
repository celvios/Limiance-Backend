INSERT INTO operational_controls (control_key, enabled)
VALUES ('conversions_enabled', true)
ON CONFLICT (control_key) DO NOTHING;
