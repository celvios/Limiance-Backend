CREATE TABLE user_roles (
    user_id UUID NOT NULL REFERENCES users(id),
    role TEXT NOT NULL CHECK (role IN ('support', 'compliance', 'treasury_operator', 'treasury_approver', 'auditor', 'platform_administrator')),
    granted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, role)
);

CREATE UNIQUE INDEX accounts_system_name_unique ON accounts (name) WHERE kind = 'system';
