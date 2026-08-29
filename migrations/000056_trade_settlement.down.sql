DROP TABLE IF EXISTS trade_fee_accruals;
DROP TABLE IF EXISTS trading_fee_accounts;
DROP TABLE IF EXISTS trade_participants;
DROP TABLE IF EXISTS engine_trade_inbox;
DROP TABLE IF EXISTS engine_symbol_sequences;
DROP INDEX IF EXISTS trades_pair_settled_idx;
DROP INDEX IF EXISTS trades_pair_sequence_idx;
ALTER TABLE trades DROP CONSTRAINT IF EXISTS trades_engine_fields_check;
ALTER TABLE trades
    DROP COLUMN IF EXISTS quote_amount_atomic,
    DROP COLUMN IF EXISTS quantity_atomic,
    DROP COLUMN IF EXISTS price_atomic,
    DROP COLUMN IF EXISTS taker_order_id,
    DROP COLUMN IF EXISTS maker_order_id,
    DROP COLUMN IF EXISTS engine_timestamp_ns,
    DROP COLUMN IF EXISTS sequence_id;
