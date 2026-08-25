CREATE TABLE notification_deliveries (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    notification_id UUID NOT NULL REFERENCES notifications(id) ON DELETE CASCADE,
    channel TEXT NOT NULL CHECK (channel IN ('email', 'push')),
    destination TEXT NOT NULL,
    delivered_at TIMESTAMPTZ,
    attempts INT NOT NULL DEFAULT 0,
    last_attempt_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (notification_id, channel, destination)
);

CREATE INDEX notification_deliveries_pending_idx
    ON notification_deliveries (channel, delivered_at)
    WHERE delivered_at IS NULL;
