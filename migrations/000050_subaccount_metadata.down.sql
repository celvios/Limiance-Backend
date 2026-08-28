DROP TABLE IF EXISTS subaccount_permissions;
DROP TABLE IF EXISTS subaccount_limits;
ALTER TABLE accounts DROP CONSTRAINT IF EXISTS accounts_subaccount_owner_check;
DROP TABLE IF EXISTS subaccounts;
