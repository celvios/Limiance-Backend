ALTER TABLE market_trade_events DROP CONSTRAINT IF EXISTS market_trade_events_payload_hash_check;
ALTER TABLE market_trade_events DROP COLUMN IF EXISTS payload_hash;

UPDATE market_stream_sequences SET last_sequence_id=1 WHERE last_sequence_id=0;
ALTER TABLE market_stream_sequences
    DROP CONSTRAINT market_stream_sequences_last_sequence_id_check,
    ADD CONSTRAINT market_stream_sequences_last_sequence_id_check CHECK (last_sequence_id > 0);
