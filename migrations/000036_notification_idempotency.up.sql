-- Notification delivery is at-least-once because it is driven by SQS. The
-- reference id is the stable business aggregate id, so this unique index makes
-- in-app notification creation safe across consumer retries.
ALTER TABLE notifications
    ADD COLUMN reference_id TEXT NOT NULL DEFAULT '';

ALTER TABLE notifications
    DROP CONSTRAINT notifications_priority_check;

UPDATE notifications SET priority = 'urgent' WHERE priority = 'security';

ALTER TABLE notifications
    ADD CONSTRAINT notifications_priority_check
    CHECK (priority IN ('urgent', 'high', 'normal', 'low'));

CREATE UNIQUE INDEX notifications_user_event_reference_key
    ON notifications (user_id, event_type, reference_id);
