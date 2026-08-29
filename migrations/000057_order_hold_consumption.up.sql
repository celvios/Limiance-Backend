ALTER TABLE trade_participants
    ADD COLUMN hold_consumed_atomic NUMERIC(78,0) NOT NULL CHECK (hold_consumed_atomic > 0);

COMMENT ON COLUMN trade_participants.hold_consumed_atomic IS 'Atomic units consumed from this order reservation by this fill.';
