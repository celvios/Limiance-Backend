ALTER TABLE engine_symbol_sequences
    ADD COLUMN halted_by_sequence_gap BOOLEAN NOT NULL DEFAULT FALSE;

COMMENT ON COLUMN engine_symbol_sequences.halted_by_sequence_gap IS 'True only when settlement sequencing changed an active pair to halted.';
