DROP TRIGGER IF EXISTS p2p_evidence_immutable ON p2p_evidence;
DROP TRIGGER IF EXISTS p2p_events_immutable ON p2p_events;
DROP FUNCTION IF EXISTS reject_p2p_immutable_mutation();
DROP TABLE IF EXISTS p2p_resolution_requests;
DROP TABLE IF EXISTS p2p_evidence;
DROP TABLE IF EXISTS p2p_events;
DROP TABLE IF EXISTS p2p_trades;
