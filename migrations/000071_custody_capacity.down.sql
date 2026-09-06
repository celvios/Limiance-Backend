DO $$ BEGIN
    IF EXISTS(SELECT 1 FROM custody_capacity_reservations)
       OR EXISTS(SELECT 1 FROM custody_withdrawal_route_requests)
       OR EXISTS(SELECT 1 FROM custody_withdrawal_route_approvals) THEN
        RAISE EXCEPTION 'cannot remove recorded custody capacity history';
    END IF;
END $$;
DROP TABLE custody_capacity_terminal_events;
DROP TABLE custody_capacity_reservations;
ALTER TABLE withdrawal_dispatches DROP COLUMN capacity_route_id;
DROP TABLE custody_withdrawal_route_approvals;
DROP TABLE custody_withdrawal_route_requests;
DROP TABLE custody_withdrawal_routes;
DROP FUNCTION reject_custody_capacity_history_mutation();
