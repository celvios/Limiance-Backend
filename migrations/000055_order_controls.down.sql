DROP TABLE IF EXISTS engine_control_commands;
DROP INDEX IF EXISTS orders_conditional_trigger_idx;

ALTER TABLE engine_commands DROP CONSTRAINT engine_commands_status_check;
ALTER TABLE engine_commands ADD CONSTRAINT engine_commands_status_check
    CHECK (status IN ('pending','dispatched'));

ALTER TABLE orders DROP CONSTRAINT orders_conditional_trigger_check;
ALTER TABLE orders DROP COLUMN IF EXISTS hold_released_at;
ALTER TABLE orders DROP COLUMN IF EXISTS triggered_at;
ALTER TABLE orders DROP COLUMN IF EXISTS trigger_direction;
ALTER TABLE orders DROP COLUMN IF EXISTS trigger_price;

ALTER TABLE orders DROP CONSTRAINT orders_status_check;
ALTER TABLE orders ADD CONSTRAINT orders_status_check
    CHECK (status IN ('PENDING','OPEN','PARTIALLY_FILLED','FILLED','CANCELED','REJECTED'));
ALTER TABLE orders DROP CONSTRAINT orders_type_check;
ALTER TABLE orders ADD CONSTRAINT orders_type_check
    CHECK (type IN ('LIMIT','MARKET','STOP_LIMIT'));
