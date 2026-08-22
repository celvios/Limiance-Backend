-- Conversion is an internal treasury settlement. It must remain off until
-- selected pairs, fee/spread policies, funded treasury inventory, and ledger
-- acceptance tests have been explicitly signed off for the environment.
UPDATE operational_controls
SET enabled = false, updated_at = now()
WHERE control_key = 'conversions_enabled';
