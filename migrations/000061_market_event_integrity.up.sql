ALTER TABLE market_stream_sequences
    DROP CONSTRAINT market_stream_sequences_last_sequence_id_check,
    ADD CONSTRAINT market_stream_sequences_last_sequence_id_check CHECK (last_sequence_id >= 0);

ALTER TABLE market_trade_events ADD COLUMN payload_hash BYTEA;
UPDATE market_trade_events SET payload_hash=decode(repeat('00',32),'hex') WHERE payload_hash IS NULL;
ALTER TABLE market_trade_events
    ALTER COLUMN payload_hash SET NOT NULL,
    ADD CONSTRAINT market_trade_events_payload_hash_check CHECK (octet_length(payload_hash) = 32);
