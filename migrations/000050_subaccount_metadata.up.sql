CREATE TABLE subaccounts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    main_account_id UUID NOT NULL REFERENCES users(id),
    account_id UUID NOT NULL UNIQUE REFERENCES accounts(id),
    nickname VARCHAR(20) NOT NULL CHECK (nickname ~ '^[A-Za-z0-9]{5,20}$'),
    type VARCHAR(20) NOT NULL DEFAULT 'standard' CHECK (type IN ('standard', 'custom')),
    account_mode VARCHAR(20) NOT NULL DEFAULT 'uta' CHECK (account_mode IN ('standard', 'uta')),
    username VARCHAR(50) UNIQUE,
    password_hash VARCHAR(255),
    status VARCHAR(20) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'frozen', 'deleted')),
    require_password_for_login BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ,
    CHECK (type = 'standard' OR (username IS NOT NULL AND password_hash IS NOT NULL)),
    CHECK (type = 'custom' OR (username IS NULL AND password_hash IS NULL AND require_password_for_login = FALSE))
);

CREATE UNIQUE INDEX subaccounts_main_nickname_active_idx
    ON subaccounts (main_account_id, lower(nickname))
    WHERE status <> 'deleted';
CREATE UNIQUE INDEX subaccounts_main_account_active_idx
    ON subaccounts (main_account_id, account_id)
    WHERE status <> 'deleted';
CREATE UNIQUE INDEX subaccounts_username_active_idx
    ON subaccounts (lower(username))
    WHERE username IS NOT NULL AND status <> 'deleted';

ALTER TABLE accounts
    ADD CONSTRAINT accounts_subaccount_owner_check
    CHECK (kind <> 'subaccount' OR user_id IS NOT NULL);

CREATE TABLE subaccount_limits (
    main_account_id UUID PRIMARY KEY REFERENCES users(id),
    max_standard_subaccounts INT NOT NULL DEFAULT 5 CHECK (max_standard_subaccounts BETWEEN 0 AND 20),
    current_standard_count INT NOT NULL DEFAULT 0 CHECK (current_standard_count >= 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE subaccount_permissions (
    subaccount_id UUID PRIMARY KEY REFERENCES subaccounts(id) ON DELETE CASCADE,
    can_trade_spot BOOLEAN NOT NULL DEFAULT TRUE,
    can_trade_derivatives BOOLEAN NOT NULL DEFAULT TRUE,
    can_trade_margin BOOLEAN NOT NULL DEFAULT TRUE,
    can_use_api BOOLEAN NOT NULL DEFAULT TRUE,
    max_leverage INT NOT NULL DEFAULT 100 CHECK (max_leverage BETWEEN 1 AND 100),
    daily_transfer_limit_usd NUMERIC(24,8) NOT NULL DEFAULT 100000 CHECK (daily_transfer_limit_usd >= 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO subaccount_limits (main_account_id)
SELECT id FROM users
ON CONFLICT (main_account_id) DO NOTHING;

INSERT INTO subaccounts (main_account_id, account_id, nickname, type, account_mode)
SELECT a.user_id, a.id, left(regexp_replace(a.name, '[^A-Za-z0-9]', '', 'g') || '00000', 20), 'standard', 'uta'
FROM accounts a
WHERE a.kind = 'subaccount'
ON CONFLICT (account_id) DO NOTHING;

INSERT INTO subaccount_permissions (subaccount_id)
SELECT id FROM subaccounts
ON CONFLICT (subaccount_id) DO NOTHING;

UPDATE subaccount_limits l
SET current_standard_count = counts.total,
    updated_at = now()
FROM (
    SELECT main_account_id, count(*)::int AS total
    FROM subaccounts
    WHERE type = 'standard' AND status = 'active'
    GROUP BY main_account_id
) counts
WHERE l.main_account_id = counts.main_account_id;
