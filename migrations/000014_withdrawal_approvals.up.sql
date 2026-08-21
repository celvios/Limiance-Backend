CREATE TABLE withdrawal_approvals (
    withdrawal_id UUID NOT NULL REFERENCES withdrawals(id),
    approver_user_id UUID NOT NULL REFERENCES users(id),
    reason TEXT NOT NULL,
    approved_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (withdrawal_id, approver_user_id)
);

CREATE INDEX withdrawal_approvals_withdrawal_idx ON withdrawal_approvals (withdrawal_id, approved_at);
