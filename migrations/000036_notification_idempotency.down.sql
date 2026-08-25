DROP INDEX IF EXISTS notifications_user_event_reference_key;

ALTER TABLE notifications
    DROP CONSTRAINT IF EXISTS notifications_priority_check;

UPDATE notifications SET priority = 'security' WHERE priority = 'urgent';

ALTER TABLE notifications
    ADD CONSTRAINT notifications_priority_check
    CHECK (priority IN ('security', 'high', 'normal'));

ALTER TABLE notifications
    DROP COLUMN IF EXISTS reference_id;
