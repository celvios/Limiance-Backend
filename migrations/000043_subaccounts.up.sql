CREATE UNIQUE INDEX accounts_active_subaccount_name_idx
    ON accounts (user_id, lower(name))
    WHERE kind = 'subaccount' AND status = 'active';
