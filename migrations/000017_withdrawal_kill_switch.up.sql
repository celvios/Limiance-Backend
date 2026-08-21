CREATE TABLE operational_controls (
    control_key TEXT PRIMARY KEY,
    enabled BOOLEAN NOT NULL,
    updated_by UUID REFERENCES users(id),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO operational_controls (control_key, enabled)
VALUES ('withdrawals_enabled', true);
