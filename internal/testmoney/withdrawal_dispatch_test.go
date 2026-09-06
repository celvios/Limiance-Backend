package testmoney

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/limiance/backend/internal/datamanager"
)

func dispatchFixture(t *testing.T) (*fixture, *datamanager.Manager, datamanager.WithdrawalForCustody) {
	t.Helper()
	f := withdrawalFixture(t)
	f.enable(t)
	creditedDeposit(t, f, "100", true)
	if _, err := f.pool.Exec(f.ctx, `UPDATE assets SET custody_asset_id='test-provider-token' WHERE id=$1`, f.asset); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `UPDATE operational_controls SET enabled=TRUE WHERE control_key='automatic_withdrawals'`); err != nil {
		t.Fatal(err)
	}
	m := datamanager.New(f.pool)
	if _, err := m.RequestWithdrawal(f.ctx, withdrawalInput(f, "dispatch", 100)); err != nil {
		t.Fatal(err)
	}
	items, err := m.ApprovedWithdrawals(f.ctx, "fireblocks", 25)
	if err != nil || len(items) != 1 {
		t.Fatalf("candidates=%+v err=%v", items, err)
	}
	return f, m, items[0]
}

func approvedCapacityEvidence(t *testing.T, f *fixture) *datamanager.WithdrawalCapacityEvidence {
	t.Helper()
	m := datamanager.New(f.pool)
	proposal, err := m.ProposeCustodyWithdrawalRoute(f.ctx, f.maker, datamanager.CustodyWithdrawalRouteInput{
		AssetID: f.asset, Provider: "fireblocks", Environment: "staging", Network: "internal_spot",
		ProviderAssetID: "test-provider-token", AssetDecimals: 18, FeeProviderAssetID: "ETH_TEST5",
		FeeAssetDecimals: 18, MaxFeeAtomic: "100000000000000000", MaxObservationAgeSeconds: 30,
		Reason: "enable tested withdrawal route", IdempotencyKey: "route-enable",
	})
	if err != nil {
		t.Fatal(err)
	}
	approval, err := m.ApproveCustodyWithdrawalRoute(f.ctx, f.checker, proposal.RequestID, "approve tested withdrawal route", "route-approval")
	if err != nil {
		t.Fatal(err)
	}
	return &datamanager.WithdrawalCapacityEvidence{
		RouteID: approval.RouteID, Environment: "staging", ObservedAt: time.Now().UTC(), AssetAvailable: "1000000000000000000000",
		FeeAvailable: "1000000000000000000000", FeeRequired: "1000000000000000",
		AssetBlockHeight: "100", AssetBlockHash: "asset-block", FeeBlockHeight: "100", FeeBlockHash: "fee-block",
	}
}

func TestWithdrawalDispatchConcurrentOneShotAndUnknownHold(t *testing.T) {
	f, m, item := dispatchFixture(t)
	evidence := approvedCapacityEvidence(t, f)
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan bool, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ok, err := m.BeginWithdrawalDispatch(f.ctx, "fireblocks", item, evidence)
			results <- ok
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	won := 0
	for ok := range results {
		if ok {
			won++
		}
	}
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if won != 1 {
		t.Fatalf("owners=%d", won)
	}
	for _, query := range []string{
		"SELECT COUNT(*) FROM withdrawal_dispatches",
		"SELECT COUNT(*) FROM custody_capacity_reservations",
		"SELECT COUNT(*) FROM audit_events WHERE action='withdrawal.dispatch_started'",
		"SELECT COUNT(*) FROM outbox_events WHERE event_type='withdrawal.dispatch_started'",
		"SELECT COUNT(*) FROM withdrawal_dispatches WHERE entitlement_enforced AND credited_deposits_atomic=100 AND committed_withdrawals_atomic=100",
	} {
		if f.count(t, query) != 1 {
			t.Fatal(query)
		}
	}
	// Simulate process loss with no provider acknowledgement. Restart cannot
	// select/reclaim the request, cancel it or spend its held balance/ceiling.
	m = datamanager.New(f.pool)
	if items, err := m.ApprovedWithdrawals(f.ctx, "fireblocks", 25); err != nil || len(items) != 0 {
		t.Fatalf("%+v %v", items, err)
	}
	if ok, err := m.BeginWithdrawalDispatch(f.ctx, "fireblocks", item, approvedCapacityEvidence(t, f)); err != nil || ok {
		t.Fatalf("%v %v", ok, err)
	}
	if _, err := m.CancelWithdrawal(f.ctx, f.user, item.ID); !errors.Is(err, datamanager.ErrWithdrawalNotCancellable) {
		t.Fatal(err)
	}
	if _, err := m.RequestWithdrawal(f.ctx, withdrawalInput(f, "no-extra", 1)); !errors.Is(err, datamanager.ErrWithdrawalDepositLimit) {
		t.Fatal(err)
	}
	var held string
	if err := f.pool.QueryRow(f.ctx, `SELECT SUM(CASE direction WHEN 'credit' THEN amount_atomic ELSE -amount_atomic END)::text
        FROM postings WHERE account_id=$1 AND asset_id=$2 AND bucket='held'`, f.account, f.asset).Scan(&held); err != nil || held != "100" {
		t.Fatalf("%s %v", held, err)
	}
	for _, sql := range []string{"DELETE FROM withdrawal_dispatches", "UPDATE withdrawal_dispatches SET provider='other'", "TRUNCATE withdrawal_dispatches",
		"DELETE FROM custody_capacity_reservations", "UPDATE custody_capacity_reservations SET asset_available_atomic=0", "TRUNCATE custody_capacity_reservations"} {
		if _, err := f.pool.Exec(f.ctx, sql); err == nil {
			t.Fatalf("history mutation accepted: %s", sql)
		}
	}
	down, err := os.ReadFile("../../migrations/000070_withdrawal_dispatch.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(f.ctx, string(down)); err == nil {
		t.Fatal("rollback erased dispatch")
	}
}

func TestWithdrawalDispatchCapacityFailsClosed(t *testing.T) {
	f, m, item := dispatchFixture(t)
	if ok, err := m.BeginWithdrawalDispatch(f.ctx, "fireblocks", item, nil); ok || !errors.Is(err, datamanager.ErrWithdrawalCapacityRouteUnavailable) {
		t.Fatalf("missing evidence: ok=%v err=%v", ok, err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*datamanager.WithdrawalCapacityEvidence)
		want   error
	}{
		{"missing_route", func(e *datamanager.WithdrawalCapacityEvidence) { e.RouteID = "00000000-0000-0000-0000-000000000000" }, datamanager.ErrWithdrawalCapacityRouteUnavailable},
		{"stale", func(e *datamanager.WithdrawalCapacityEvidence) { e.ObservedAt = time.Now().Add(-time.Minute) }, datamanager.ErrWithdrawalCapacityObservation},
		{"future", func(e *datamanager.WithdrawalCapacityEvidence) { e.ObservedAt = time.Now().Add(time.Minute) }, datamanager.ErrWithdrawalCapacityObservation},
		{"invalid_number", func(e *datamanager.WithdrawalCapacityEvidence) { e.AssetAvailable = "1.0" }, datamanager.ErrWithdrawalCapacityObservation},
		{"token_shortage", func(e *datamanager.WithdrawalCapacityEvidence) { e.AssetAvailable = "99" }, datamanager.ErrWithdrawalCustodyCapacity},
		{"gas_shortage", func(e *datamanager.WithdrawalCapacityEvidence) { e.FeeAvailable = "999" }, datamanager.ErrWithdrawalCustodyCapacity},
		{"fee_cap", func(e *datamanager.WithdrawalCapacityEvidence) { e.FeeRequired = "100000000000000001" }, datamanager.ErrWithdrawalCustodyCapacity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, m, item := dispatchFixture(t)
			evidence := approvedCapacityEvidence(t, f)
			tc.mutate(evidence)
			if ok, err := m.BeginWithdrawalDispatch(f.ctx, "fireblocks", item, evidence); ok || !errors.Is(err, tc.want) {
				t.Fatalf("ok=%v err=%v", ok, err)
			}
			if f.count(t, "SELECT COUNT(*) FROM withdrawal_dispatches") != 0 || f.count(t, "SELECT COUNT(*) FROM custody_capacity_reservations") != 0 {
				t.Fatal("failed capacity check persisted state")
			}
		})
	}
}

func TestCustodyCapacityRouteRequiresDistinctAuthorizedActors(t *testing.T) {
	f, m, item := dispatchFixture(t)
	input := datamanager.CustodyWithdrawalRouteInput{AssetID: f.asset, Provider: "fireblocks", Environment: "staging",
		Network: "internal_spot", ProviderAssetID: "test-provider-token", AssetDecimals: 18,
		FeeProviderAssetID: "ETH_TEST5", FeeAssetDecimals: 18, MaxFeeAtomic: "100000000000000000",
		MaxObservationAgeSeconds: 30, Reason: "enable tested withdrawal route", IdempotencyKey: "maker-checker-route"}
	if _, err := m.ProposeCustodyWithdrawalRoute(f.ctx, f.checker, input); !errors.Is(err, datamanager.ErrWithdrawalCapacityRouteActor) {
		t.Fatalf("unauthorized proposal: %v", err)
	}
	proposal, err := m.ProposeCustodyWithdrawalRoute(f.ctx, f.maker, input)
	if err != nil || proposal.Duplicate {
		t.Fatalf("proposal=%+v err=%v", proposal, err)
	}
	if _, err = m.WithdrawalCapacityRoute(f.ctx, "fireblocks", "staging", item); !errors.Is(err, datamanager.ErrWithdrawalCapacityRouteUnavailable) {
		t.Fatalf("unapproved route selected: %v", err)
	}
	if _, err = m.ApproveCustodyWithdrawalRoute(f.ctx, f.maker, proposal.RequestID, "self approval rejected", "self-approval"); !errors.Is(err, datamanager.ErrWithdrawalCapacityRouteActor) {
		t.Fatalf("self approval: %v", err)
	}
	approval, err := m.ApproveCustodyWithdrawalRoute(f.ctx, f.checker, proposal.RequestID, "independent route approval", "checker-approval")
	if err != nil || approval.Duplicate || approval.RouteID != proposal.RouteID {
		t.Fatalf("approval=%+v err=%v", approval, err)
	}
	replay, err := m.ApproveCustodyWithdrawalRoute(f.ctx, f.checker, proposal.RequestID, "independent route approval", "checker-approval")
	if err != nil || !replay.Duplicate || replay.RouteID != proposal.RouteID {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	route, err := m.WithdrawalCapacityRoute(f.ctx, "fireblocks", "staging", item)
	if err != nil || !route.Required || route.RouteID != proposal.RouteID {
		t.Fatalf("route=%+v err=%v", route, err)
	}
	if f.count(t, "SELECT COUNT(*) FROM audit_events WHERE action IN ('custody.route_proposed','custody.route_approved')") != 2 ||
		f.count(t, "SELECT COUNT(*) FROM outbox_events WHERE event_type IN ('custody.route_proposed','custody.route_approved')") != 2 {
		t.Fatal("route maker-checker evidence missing or duplicated")
	}
}

func TestWithdrawalDispatchCapacitySerializesConcurrentRequests(t *testing.T) {
	f := withdrawalFixture(t)
	f.enable(t)
	creditedDeposit(t, f, "200", true)
	if _, err := f.pool.Exec(f.ctx, `UPDATE assets SET custody_asset_id='test-provider-token' WHERE id=$1`, f.asset); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `UPDATE operational_controls SET enabled=TRUE WHERE control_key='automatic_withdrawals'`); err != nil {
		t.Fatal(err)
	}
	m := datamanager.New(f.pool)
	for i := 0; i < 2; i++ {
		if _, err := m.RequestWithdrawal(f.ctx, withdrawalInput(f, fmt.Sprintf("capacity-%d", i), 100)); err != nil {
			t.Fatal(err)
		}
	}
	items, err := m.ApprovedWithdrawals(f.ctx, "fireblocks", 25)
	if err != nil || len(items) != 2 {
		t.Fatalf("items=%d err=%v", len(items), err)
	}
	evidence := approvedCapacityEvidence(t, f)
	evidence.AssetAvailable = "150"
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, item := range items {
		wg.Add(1)
		go func(item datamanager.WithdrawalForCustody) {
			defer wg.Done()
			<-start
			ok, err := m.BeginWithdrawalDispatch(f.ctx, "fireblocks", item, evidence)
			if err == nil && !ok {
				err = errors.New("dispatch was not claimed")
			}
			results <- err
		}(item)
	}
	close(start)
	wg.Wait()
	close(results)
	succeeded, denied := 0, 0
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, datamanager.ErrWithdrawalCustodyCapacity):
			denied++
		default:
			t.Fatal(err)
		}
	}
	if succeeded != 1 || denied != 1 || f.count(t, "SELECT COUNT(*) FROM custody_capacity_reservations") != 1 {
		t.Fatalf("succeeded=%d denied=%d", succeeded, denied)
	}
}

func TestWithdrawalDispatchRevalidatesDepositAndEligibility(t *testing.T) {
	for _, tc := range []struct {
		name, sql string
		want      error
	}{
		{"deposit_reorg", "UPDATE deposits SET confirmations=0", datamanager.ErrWithdrawalDepositLimit},
		{"kill_switch", "UPDATE operational_controls SET enabled=FALSE WHERE control_key='withdrawals_enabled'", datamanager.ErrWithdrawalsDisabled},
		{"kyc_revoked", "UPDATE kyc_profiles SET status='rejected'", datamanager.ErrWithdrawalKYCRequired},
		{"kyc_expired", "UPDATE kyc_profiles SET expires_at=now()-interval '1 second'", datamanager.ErrWithdrawalKYCRequired},
		{"user_disabled", "UPDATE users SET status='suspended'", datamanager.ErrWithdrawalKYCRequired},
		{"account_frozen", "UPDATE accounts SET status='frozen' WHERE kind='funding'", datamanager.ErrWithdrawalAccountUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, m, item := dispatchFixture(t)
			evidence := approvedCapacityEvidence(t, f)
			if _, err := f.pool.Exec(f.ctx, tc.sql); err != nil {
				t.Fatal(err)
			}
			if ok, err := m.BeginWithdrawalDispatch(f.ctx, "fireblocks", item, evidence); ok || !errors.Is(err, tc.want) {
				t.Fatalf("%v %v", ok, err)
			}
			if f.count(t, "SELECT COUNT(*) FROM withdrawal_dispatches") != 0 {
				t.Fatal("invalid dispatch recorded")
			}
		})
	}
}

func TestWithdrawalDispatchBindsExactPayloadAndRoute(t *testing.T) {
	f, m, item := dispatchFixture(t)
	for _, mutate := range []func(*datamanager.WithdrawalForCustody){
		func(v *datamanager.WithdrawalForCustody) { v.AmountAtomic = "99" },
		func(v *datamanager.WithdrawalForCustody) { v.DestinationAddress = "different" },
		func(v *datamanager.WithdrawalForCustody) { v.DestinationTag = "different" },
		func(v *datamanager.WithdrawalForCustody) { v.SourceVaultID = "different" },
		func(v *datamanager.WithdrawalForCustody) { v.CustodyAssetID = "different" },
		func(v *datamanager.WithdrawalForCustody) { v.Network = "ethereum" },
		func(v *datamanager.WithdrawalForCustody) { v.AssetDecimals = 6 },
	} {
		changed := item
		mutate(&changed)
		if ok, err := m.BeginWithdrawalDispatch(f.ctx, "fireblocks", changed, approvedCapacityEvidence(t, f)); ok || !errors.Is(err, datamanager.ErrWithdrawalDispatchChanged) {
			t.Fatalf("%+v: %v %v", changed, ok, err)
		}
	}
	if ok, err := m.BeginWithdrawalDispatch(f.ctx, "other-provider", item, approvedCapacityEvidence(t, f)); ok || err != nil {
		t.Fatalf("wrong provider: %v %v", ok, err)
	}
	if f.count(t, "SELECT COUNT(*) FROM withdrawal_dispatches") != 0 {
		t.Fatal("stale payload recorded")
	}
	if _, err := f.pool.Exec(f.ctx, `UPDATE assets SET status='disabled' WHERE id=$1`, f.asset); err != nil {
		t.Fatal(err)
	}
	if ok, err := m.BeginWithdrawalDispatch(f.ctx, "fireblocks", item, approvedCapacityEvidence(t, f)); ok || err != nil {
		t.Fatalf("disabled asset: %v %v", ok, err)
	}
}

func TestWithdrawalDispatchRequiresPostedHold(t *testing.T) {
	f, m, item := dispatchFixture(t)
	// Simulate corrupt historical approval with its hold journal not posted.
	if _, err := f.pool.Exec(f.ctx, `UPDATE journals SET status='reversed' WHERE withdrawal_id=$1`, item.ID); err != nil {
		t.Fatal(err)
	}
	if ok, err := m.BeginWithdrawalDispatch(f.ctx, "fireblocks", item, approvedCapacityEvidence(t, f)); ok || !errors.Is(err, datamanager.ErrInsufficientWithdrawalBalance) {
		t.Fatalf("%v %v", ok, err)
	}
	if f.count(t, "SELECT COUNT(*) FROM withdrawal_dispatches") != 0 {
		t.Fatal("unfunded dispatch recorded")
	}
}

func TestWithdrawalDispatchOutboxFailureRollsBackPermission(t *testing.T) {
	f, m, item := dispatchFixture(t)
	evidence := approvedCapacityEvidence(t, f)
	if _, err := f.pool.Exec(f.ctx, `CREATE FUNCTION fail_dispatch_outbox() RETURNS trigger LANGUAGE plpgsql AS $$
        BEGIN RAISE EXCEPTION 'injected dispatch outbox failure'; END $$;
        CREATE TRIGGER fail_dispatch_outbox BEFORE INSERT ON outbox_events FOR EACH ROW EXECUTE FUNCTION fail_dispatch_outbox()`); err != nil {
		t.Fatal(err)
	}
	if ok, err := m.BeginWithdrawalDispatch(f.ctx, "fireblocks", item, evidence); ok || err == nil {
		t.Fatalf("%v %v", ok, err)
	}
	if f.count(t, "SELECT COUNT(*) FROM withdrawal_dispatches") != 0 || f.count(t, "SELECT COUNT(*) FROM audit_events WHERE action='withdrawal.dispatch_started'") != 0 {
		t.Fatal("partial dispatch persisted")
	}
	if f.count(t, "SELECT COUNT(*) FROM custody_capacity_reservations") != 0 {
		t.Fatal("capacity reservation survived rolled-back dispatch")
	}
	if _, err := f.pool.Exec(f.ctx, "DROP TRIGGER fail_dispatch_outbox ON outbox_events"); err != nil {
		t.Fatal(err)
	}
	if ok, err := m.BeginWithdrawalDispatch(f.ctx, "fireblocks", item, evidence); !ok || err != nil {
		t.Fatalf("safe retry: %v %v", ok, err)
	}
}

func TestWithdrawalDispatchWithoutIssuanceAndAcknowledgementReplay(t *testing.T) {
	f, m, item := dispatchFixture(t)
	// No grants have ever been issued. Preserve the existing legitimate
	// withdrawal path when the staging protection model is not enabled.
	if _, err := f.pool.Exec(f.ctx, "UPDATE test_money_control SET enabled=FALSE,withdrawal_limits_ready=FALSE"); err != nil {
		t.Fatal(err)
	}
	if ok, err := m.BeginWithdrawalDispatch(f.ctx, "fireblocks", item, nil); !ok || err != nil {
		t.Fatalf("%v %v", ok, err)
	}
	if f.count(t, "SELECT COUNT(*) FROM withdrawal_dispatches WHERE NOT entitlement_enforced") != 1 {
		t.Fatal("unexpected protection mode")
	}
	for i := 0; i < 2; i++ {
		if err := m.MarkWithdrawalSubmitted(f.ctx, item.ID, "ack-id"); err != nil {
			t.Fatal(err)
		}
	}
	// Legacy request creation shares this event name; count actual custody ACKs.
	if f.count(t, "SELECT COUNT(*) FROM outbox_events WHERE event_type='withdrawal.submitted' AND payload->>'provider_transaction_id'='ack-id'") != 1 {
		t.Fatal("duplicate acknowledgement event")
	}
	for _, ack := range []string{"", "other-id"} {
		if err := m.MarkWithdrawalSubmitted(f.ctx, item.ID, ack); !errors.Is(err, datamanager.ErrWithdrawalDispatchChanged) {
			t.Fatal(err)
		}
	}
	if _, err := m.RecordWithdrawalCustodyUpdate(f.ctx, datamanager.WithdrawalCustodyUpdate{ProviderTransactionID: "ack-id", Status: "COMPLETED"}); err != nil {
		t.Fatal(err)
	}
	if err := m.MarkWithdrawalSubmitted(f.ctx, item.ID, "ack-id"); err != nil {
		t.Fatal(err)
	}
	if f.count(t, "SELECT COUNT(*) FROM withdrawals WHERE status='completed'") != 1 {
		t.Fatal("ACK replay changed terminal state")
	}
}
