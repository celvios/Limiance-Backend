package testmoney

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/limiance/backend/internal/datamanager"
)

func withdrawalFixture(t *testing.T) *fixture {
	t.Helper()
	f := databaseFixture(t)
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO kyc_profiles(user_id,status) VALUES($1,'approved')`, f.user); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `UPDATE operational_controls SET enabled=TRUE WHERE control_key='withdrawals_enabled'`); err != nil {
		t.Fatal(err)
	}
	return f
}

func creditFixtureBalance(t *testing.T, f *fixture, account, asset, amount, reference, referenceID string) {
	t.Helper()
	var source, journal string
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO accounts(kind,name) VALUES('system',gen_random_uuid()::text) RETURNING id::text`).Scan(&source); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO journals(idempotency_key,reference_type,reference_id) VALUES(gen_random_uuid()::text,$1,$2) RETURNING id::text`, reference, referenceID).Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO postings(journal_id,account_id,asset_id,bucket,direction,amount_atomic)
 VALUES($1,$2,$3,'available','debit',$4::numeric),($1,$5,$3,'available','credit',$4::numeric)`, journal, source, asset, amount, account); err != nil {
		t.Fatal(err)
	}
}

func creditedDeposit(t *testing.T, f *fixture, amount string, post bool) string {
	t.Helper()
	var wallet, address, deposit string
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO custody_wallets(user_id,external_vault_id,status)
 VALUES($1,gen_random_uuid()::text,'active') RETURNING id::text`, f.user).Scan(&wallet); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO deposit_addresses(user_id,asset_id,custody_wallet_id,address)
 VALUES($1,$2,$3,gen_random_uuid()::text) RETURNING id::text`, f.user, f.asset, wallet).Scan(&address); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO deposits(user_id,asset_id,deposit_address_id,custody_wallet_id,provider_transaction_id,transaction_hash,
 amount_atomic,confirmations,confirmations_required,status,risk_status,credited_at)
 VALUES($1,$2,$3,$4,gen_random_uuid()::text,'confirmed-test-hash',$5::numeric,1,1,'credited','approved',now()) RETURNING id::text`, f.user, f.asset, address, wallet, amount).Scan(&deposit); err != nil {
		t.Fatal(err)
	}
	if post {
		creditFixtureBalance(t, f, f.account, f.asset, amount, "deposit_credit", deposit)
	}
	return deposit
}

func withdrawalInput(f *fixture, key string, amount int64) datamanager.WithdrawalInput {
	return datamanager.WithdrawalInput{UserID: f.user, SourceAccountID: f.account, AssetSymbol: "ISSUANCE_TEST", Network: "internal_spot",
		Address: "test-destination", AmountAtomic: amount, IdempotencyKey: key}
}

func TestWithdrawalLimitRejectsIssuedBalanceAndPersistsAfterDisable(t *testing.T) {
	f := withdrawalFixture(t)
	f.enable(t)
	r := f.propose(t, "grant")
	if _, err := f.service.Approve(f.ctx, f.checker, r.ID, "approval"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `UPDATE test_money_control SET enabled=FALSE,withdrawal_limits_ready=FALSE`); err != nil {
		t.Fatal(err)
	}
	if _, err := datamanager.New(f.pool).RequestWithdrawal(f.ctx, withdrawalInput(f, "withdraw", 1)); !errors.Is(err, datamanager.ErrWithdrawalDepositLimit) {
		t.Fatal(err)
	}
	if n := f.count(t, "SELECT COUNT(*) FROM withdrawals"); n != 0 {
		t.Fatal(n)
	}
}

func TestWithdrawalLimitUsesDepositsNotTradingOrIssuedBalance(t *testing.T) {
	f := withdrawalFixture(t)
	f.enable(t)
	creditedDeposit(t, f, "100", true)
	creditFixtureBalance(t, f, f.account, f.asset, "1000", "test_trading_proceeds", "trade-fixture")
	manager := datamanager.New(f.pool)
	if _, err := manager.RequestWithdrawal(f.ctx, withdrawalInput(f, "over", 101)); !errors.Is(err, datamanager.ErrWithdrawalDepositLimit) {
		t.Fatal(err)
	}
	result, err := manager.RequestWithdrawal(f.ctx, withdrawalInput(f, "within", 100))
	if err != nil {
		t.Fatal(err)
	}
	again, err := manager.RequestWithdrawal(f.ctx, withdrawalInput(f, "within", 100))
	if err != nil || !again.Duplicate || again.WithdrawalID != result.WithdrawalID {
		t.Fatalf("%+v %v", again, err)
	}
	if _, err = manager.RequestWithdrawal(f.ctx, withdrawalInput(f, "second", 1)); !errors.Is(err, datamanager.ErrWithdrawalDepositLimit) {
		t.Fatal(err)
	}
	if n := f.count(t, "SELECT COUNT(*) FROM withdrawal_entitlement_checks"); n != 1 {
		t.Fatal(n)
	}
	conflict := withdrawalInput(f, "within", 99)
	if _, err = manager.RequestWithdrawal(f.ctx, conflict); !errors.Is(err, datamanager.ErrWithdrawalIdempotencyConflict) {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(f.ctx, "DELETE FROM withdrawal_entitlement_checks"); err == nil {
		t.Fatal("mutable evidence")
	}
}

func TestWithdrawalLimitRejectsUnverifiedOrDifferentAssetDeposits(t *testing.T) {
	f := withdrawalFixture(t)
	f.enable(t)
	deposit := creditedDeposit(t, f, "100", false)
	creditFixtureBalance(t, f, f.account, f.asset, "1000", "test_opening", "unbacked-fixture")
	manager := datamanager.New(f.pool)
	if _, err := manager.RequestWithdrawal(f.ctx, withdrawalInput(f, "no-journal", 1)); !errors.Is(err, datamanager.ErrWithdrawalDepositLimit) {
		t.Fatal(err)
	}
	creditFixtureBalance(t, f, f.account, f.asset, "100", "deposit_credit", deposit)
	if _, err := f.pool.Exec(f.ctx, `UPDATE deposits SET status='reorged' WHERE id=$1`, deposit); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.RequestWithdrawal(f.ctx, withdrawalInput(f, "reorg", 1)); !errors.Is(err, datamanager.ErrWithdrawalDepositLimit) {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `UPDATE deposits SET status='credited' WHERE id=$1`, deposit); err != nil {
		t.Fatal(err)
	}
	var other string
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO assets(symbol,network,decimals,status) VALUES('ISSUANCE_TEST','ethereum_sepolia',18,'enabled') RETURNING id::text`).Scan(&other); err != nil {
		t.Fatal(err)
	}
	creditFixtureBalance(t, f, f.account, other, "1000", "test_opening", "different-network")
	in := withdrawalInput(f, "wrong-network", 1)
	in.Network = "ethereum_sepolia"
	if _, err := manager.RequestWithdrawal(f.ctx, in); !errors.Is(err, datamanager.ErrWithdrawalDepositLimit) {
		t.Fatal(err)
	}
}

func TestWithdrawalLimitSerializesAcrossUserAccounts(t *testing.T) {
	f := withdrawalFixture(t)
	f.enable(t)
	creditedDeposit(t, f, "100", true)
	var uta string
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO accounts(user_id,kind,name) VALUES($1,'uta','other account') RETURNING id::text`, f.user).Scan(&uta); err != nil {
		t.Fatal(err)
	}
	creditFixtureBalance(t, f, uta, f.asset, "1000", "test_opening", "issued-fixture")
	manager := datamanager.New(f.pool)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i, account := range []string{f.account, uta} {
		wg.Add(1)
		go func(i int, account string) {
			defer wg.Done()
			<-start
			in := withdrawalInput(f, fmt.Sprintf("parallel-%d", i), 80)
			in.SourceAccountID = account
			_, err := manager.RequestWithdrawal(f.ctx, in)
			results <- err
		}(i, account)
	}
	close(start)
	wg.Wait()
	close(results)
	success, denied := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, datamanager.ErrWithdrawalDepositLimit) {
			denied++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || denied != 1 {
		t.Fatalf("success=%d denied=%d", success, denied)
	}
}

func TestWithdrawalCancellationRestoresEntitlement(t *testing.T) {
	f := withdrawalFixture(t)
	f.enable(t)
	creditedDeposit(t, f, "100", true)
	if _, err := f.pool.Exec(f.ctx, `UPDATE operational_controls SET enabled=FALSE WHERE control_key='automatic_withdrawals'`); err != nil {
		t.Fatal(err)
	}
	manager := datamanager.New(f.pool)
	r, err := manager.RequestWithdrawal(f.ctx, withdrawalInput(f, "hold", 100))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.CancelWithdrawal(f.ctx, f.user, r.WithdrawalID); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.RequestWithdrawal(f.ctx, withdrawalInput(f, "again", 100)); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, "SELECT COUNT(*) FROM withdrawal_entitlement_checks"); n != 2 {
		t.Fatal(n)
	}
}

func TestWithdrawalCompletionConsumesLimitAndFailureRestoresIt(t *testing.T) {
	for _, status := range []string{"COMPLETED", "FAILED"} {
		t.Run(status, func(t *testing.T) {
			f := withdrawalFixture(t)
			f.enable(t)
			creditedDeposit(t, f, "100", true)
			manager := datamanager.New(f.pool)
			r, err := manager.RequestWithdrawal(f.ctx, withdrawalInput(f, "first", 100))
			if err != nil {
				t.Fatal(err)
			}
			if err = manager.MarkWithdrawalSubmitted(f.ctx, r.WithdrawalID, "provider-id"); err != nil {
				t.Fatal(err)
			}
			if _, err = manager.RequestWithdrawal(f.ctx, withdrawalInput(f, "unknown", 1)); !errors.Is(err, datamanager.ErrWithdrawalDepositLimit) {
				t.Fatalf("unknown outcome released allowance: %v", err)
			}
			// An ambiguous historical state without an ACK or release journal must
			// not restore entitlement merely because it is labelled failed.
			if _, err = f.pool.Exec(f.ctx, `UPDATE withdrawals SET status='failed',provider_transaction_id=NULL WHERE id=$1`, r.WithdrawalID); err != nil {
				t.Fatal(err)
			}
			if _, err = manager.RequestWithdrawal(f.ctx, withdrawalInput(f, "missing-ack", 1)); !errors.Is(err, datamanager.ErrWithdrawalDepositLimit) {
				t.Fatal(err)
			}
			if _, err = f.pool.Exec(f.ctx, `UPDATE withdrawals SET status='submitted',provider_transaction_id='provider-id' WHERE id=$1`, r.WithdrawalID); err != nil {
				t.Fatal(err)
			}
			update := datamanager.WithdrawalCustodyUpdate{ProviderTransactionID: "provider-id", Status: status, TransactionHash: "test-confirmation"}
			changed, err := manager.RecordWithdrawalCustodyUpdate(f.ctx, update)
			if err != nil || !changed {
				t.Fatalf("terminal: %v %v", changed, err)
			}
			changed, err = manager.RecordWithdrawalCustodyUpdate(f.ctx, update)
			if err != nil || changed {
				t.Fatalf("replay: %v %v", changed, err)
			}
			retry, err := manager.RequestWithdrawal(f.ctx, withdrawalInput(f, "first", 100))
			if err != nil || !retry.Duplicate || retry.JournalID != r.JournalID {
				t.Fatalf("post-terminal retry: %+v %v", retry, err)
			}
			creditFixtureBalance(t, f, f.account, f.asset, "100", "test_trading_proceeds", "bought-back")
			_, err = manager.RequestWithdrawal(f.ctx, withdrawalInput(f, "after", 100))
			if status == "COMPLETED" && !errors.Is(err, datamanager.ErrWithdrawalDepositLimit) {
				t.Fatalf("completed quota reset: %v", err)
			}
			if status == "FAILED" && err != nil {
				t.Fatalf("failed quota retained: %v", err)
			}
		})
	}
}

func TestIssuanceRequiresWithdrawalReadiness(t *testing.T) {
	f := withdrawalFixture(t)
	f.enable(t)
	if _, err := f.pool.Exec(f.ctx, "UPDATE test_money_control SET withdrawal_limits_ready=FALSE"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Propose(f.ctx, f.maker, f.input("not-ready")); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
}

func TestWithdrawalBoughtBackTokensRetainOriginalDepositCeiling(t *testing.T) {
	f := withdrawalFixture(t)
	f.enable(t)
	creditedDeposit(t, f, "100", true)
	// Balanced fixtures model the settled balance changes; this is not an engine test.
	var sink, journal string
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO accounts(kind,name) VALUES('system','sale fixture') RETURNING id::text`).Scan(&sink); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO journals(idempotency_key,reference_type,reference_id) VALUES('sell-fixture','test_sale','fixture') RETURNING id::text`).Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO postings(journal_id,account_id,asset_id,bucket,direction,amount_atomic)
 VALUES($1,$2,$3,'available','debit',100),($1,$4,$3,'available','credit',100)`, journal, f.account, f.asset, sink); err != nil {
		t.Fatal(err)
	}
	manager := datamanager.New(f.pool)
	if _, err := manager.RequestWithdrawal(f.ctx, withdrawalInput(f, "sold", 100)); !errors.Is(err, datamanager.ErrInsufficientWithdrawalBalance) {
		t.Fatalf("sold balance: %v", err)
	}
	creditFixtureBalance(t, f, f.account, f.asset, "1000", "test_purchase", "bought-back-fixture")
	if _, err := manager.RequestWithdrawal(f.ctx, withdrawalInput(f, "bought-back", 100)); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.RequestWithdrawal(f.ctx, withdrawalInput(f, "profit", 1)); !errors.Is(err, datamanager.ErrWithdrawalDepositLimit) {
		t.Fatal(err)
	}
}

func TestWithdrawalAdmissionEvidenceRollsBackWithOutboxFailure(t *testing.T) {
	f := withdrawalFixture(t)
	f.enable(t)
	creditedDeposit(t, f, "100", true)
	if _, err := f.pool.Exec(f.ctx, `ALTER TABLE outbox_events ADD CONSTRAINT reject_withdrawal_fixture CHECK(event_type<>'withdrawal.submitted')`); err != nil {
		t.Fatal(err)
	}
	manager := datamanager.New(f.pool)
	if _, err := manager.RequestWithdrawal(f.ctx, withdrawalInput(f, "retry", 100)); err == nil {
		t.Fatal("expected outbox failure")
	}
	if n := f.count(t, "SELECT COUNT(*) FROM withdrawals"); n != 0 {
		t.Fatal(n)
	}
	if n := f.count(t, "SELECT COUNT(*) FROM withdrawal_entitlement_checks"); n != 0 {
		t.Fatal(n)
	}
	if n := f.count(t, "SELECT COUNT(*) FROM journals WHERE reference_type='withdrawal_hold'"); n != 0 {
		t.Fatal(n)
	}
	if _, err := f.pool.Exec(f.ctx, "ALTER TABLE outbox_events DROP CONSTRAINT reject_withdrawal_fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.RequestWithdrawal(f.ctx, withdrawalInput(f, "retry", 100)); err != nil {
		t.Fatal(err)
	}
}
