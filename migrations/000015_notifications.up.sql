CREATE TABLE notifications (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id),
    event_type TEXT NOT NULL,
    title TEXT NOT NULL,
    body TEXT NOT NULL,
    priority TEXT NOT NULL CHECK (priority IN ('security', 'high', 'normal')),
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    read_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX notifications_user_created_idx ON notifications (user_id, created_at DESC);

CREATE TABLE notification_devices (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id),
    platform TEXT NOT NULL CHECK (platform IN ('fcm', 'apns')),
    token TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX notification_devices_user_idx ON notification_devices (user_id, status);
