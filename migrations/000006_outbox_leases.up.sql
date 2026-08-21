ALTER TABLE outbox_events
    ADD COLUMN lease_token UUID,
    ADD COLUMN lease_expires_at TIMESTAMPTZ;

CREATE INDEX outbox_lease_idx ON outbox_events (occurred_at) WHERE published_at IS NULL;
