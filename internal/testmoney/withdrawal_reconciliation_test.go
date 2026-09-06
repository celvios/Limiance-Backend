package testmoney

import (
	"os"
	"testing"

	"github.com/limiance/backend/internal/datamanager"
)

func TestWithdrawalReconciliationNotFoundRemainsReservedAndImmutable(t *testing.T) {
	f, m, item := dispatchFixture(t)
	if ok, err := m.BeginWithdrawalDispatch(f.ctx, "fireblocks", item, approvedCapacityEvidence(t, f)); err != nil || !ok {
		t.Fatalf("dispatch=%v err=%v", ok, err)
	}
	candidates, err := m.WithdrawalReconciliationCandidates(f.ctx, "fireblocks", 25)
	if err != nil || len(candidates) != 1 || candidates[0].WithdrawalID != item.ID {
		t.Fatalf("candidates=%+v err=%v", candidates, err)
	}
	lookup := datamanager.WithdrawalReconciliationObservation{ExternalID: item.ID}
	if recorded, err := m.RecordWithdrawalReconciliationObservation(f.ctx, candidates[0], lookup); err != nil || !recorded {
		t.Fatalf("recorded=%v err=%v", recorded, err)
	}
	if f.count(t, "SELECT COUNT(*) FROM custody_capacity_terminal_events") != 0 ||
		f.count(t, "SELECT COUNT(*) FROM custody_capacity_reservations") != 1 {
		t.Fatal("not-found observation released unresolved capacity")
	}
	if candidates, err = m.WithdrawalReconciliationCandidates(f.ctx, "fireblocks", 25); err != nil || len(candidates) != 0 {
		t.Fatalf("fresh observation ignored cooldown: candidates=%+v err=%v", candidates, err)
	}
	for _, statement := range []string{
		"DELETE FROM withdrawal_reconciliation_observations",
		"UPDATE withdrawal_reconciliation_observations SET found=TRUE",
		"TRUNCATE withdrawal_reconciliation_observations",
	} {
		if _, err := f.pool.Exec(f.ctx, statement); err == nil {
			t.Fatalf("reconciliation history mutation accepted: %s", statement)
		}
	}
	down, err := os.ReadFile("../../migrations/000072_withdrawal_reconciliation.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(f.ctx, string(down)); err == nil {
		t.Fatal("rollback erased reconciliation evidence")
	}
}

func TestWithdrawalTerminalCapacityClosureRollsBackWithOutbox(t *testing.T) {
	f, m, item := dispatchFixture(t)
	if ok, err := m.BeginWithdrawalDispatch(f.ctx, "fireblocks", item, approvedCapacityEvidence(t, f)); err != nil || !ok {
		t.Fatalf("dispatch=%v err=%v", ok, err)
	}
	if err := m.MarkWithdrawalSubmitted(f.ctx, item.ID, "provider-rollback"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `CREATE FUNCTION fail_terminal_outbox() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'injected terminal outbox failure'; END $$;
		CREATE TRIGGER fail_terminal_outbox BEFORE INSERT ON outbox_events FOR EACH ROW EXECUTE FUNCTION fail_terminal_outbox()`); err != nil {
		t.Fatal(err)
	}
	update := datamanager.WithdrawalCustodyUpdate{ProviderTransactionID: "provider-rollback", Status: "FAILED"}
	if changed, err := m.RecordWithdrawalCustodyUpdate(f.ctx, update); err == nil || changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if f.count(t, "SELECT COUNT(*) FROM custody_capacity_terminal_events") != 0 ||
		f.count(t, "SELECT COUNT(*) FROM journals WHERE reference_type='withdrawal_release'") != 0 ||
		f.count(t, "SELECT COUNT(*) FROM withdrawals WHERE status='submitted'") != 1 {
		t.Fatal("terminal transition was partially committed")
	}
	if _, err := f.pool.Exec(f.ctx, `DROP TRIGGER fail_terminal_outbox ON outbox_events`); err != nil {
		t.Fatal(err)
	}
	if changed, err := m.RecordWithdrawalCustodyUpdate(f.ctx, update); err != nil || !changed {
		t.Fatalf("safe retry changed=%v err=%v", changed, err)
	}
	if f.count(t, "SELECT COUNT(*) FROM custody_capacity_terminal_events WHERE outcome='released'") != 1 {
		t.Fatal("safe retry did not close capacity")
	}
}

func TestWithdrawalTerminalOutcomeAtomicallyClosesCapacity(t *testing.T) {
	for _, tc := range []struct{ status, outcome string }{{"COMPLETED", "consumed"}, {"FAILED", "released"}} {
		t.Run(tc.status, func(t *testing.T) {
			f, m, item := dispatchFixture(t)
			if ok, err := m.BeginWithdrawalDispatch(f.ctx, "fireblocks", item, approvedCapacityEvidence(t, f)); err != nil || !ok {
				t.Fatalf("dispatch=%v err=%v", ok, err)
			}
			if err := m.MarkWithdrawalSubmitted(f.ctx, item.ID, "provider-terminal"); err != nil {
				t.Fatal(err)
			}
			if changed, err := m.RecordWithdrawalCustodyUpdate(f.ctx, datamanager.WithdrawalCustodyUpdate{
				ProviderTransactionID: "provider-terminal", Status: tc.status, TransactionHash: "confirmed-hash",
			}); err != nil || !changed {
				t.Fatalf("changed=%v err=%v", changed, err)
			}
			var outcome string
			if err := f.pool.QueryRow(f.ctx, `SELECT outcome FROM custody_capacity_terminal_events WHERE withdrawal_id=$1`, item.ID).Scan(&outcome); err != nil || outcome != tc.outcome {
				t.Fatalf("outcome=%s err=%v", outcome, err)
			}
		})
	}
}
