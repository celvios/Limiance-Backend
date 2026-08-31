package integration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/limiance/backend/internal/p2p"
)

func TestP2PEscrowLifecycleConcurrencyAndMakerChecker(t *testing.T) {
	databaseURL := os.Getenv("LIMIANCE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set LIMIANCE_TEST_DATABASE_URL to run PostgreSQL P2P tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	fixture := fmt.Sprintf("P2P%X", time.Now().UnixNano())
	assetSymbol, assetNetwork := "PX"+fixture[len(fixture)-6:], "p2p-"+fixture
	var assetID string
	if err = pool.QueryRow(ctx, `INSERT INTO assets(symbol,network,decimals,status) VALUES($1,$2,6,'enabled') RETURNING id::text`, assetSymbol, assetNetwork).Scan(&assetID); err != nil {
		t.Fatal(err)
	}
	seller := seedP2PUser(t, ctx, pool, fixture+"-seller")
	buyerOne := seedP2PUser(t, ctx, pool, fixture+"-buyer-one")
	buyerTwo := seedP2PUser(t, ctx, pool, fixture+"-buyer-two")
	operator := seedP2PUser(t, ctx, pool, fixture+"-operator")
	approver := seedP2PUser(t, ctx, pool, fixture+"-approver")
	if _, err = pool.Exec(ctx, `INSERT INTO user_roles(user_id,role) VALUES($1,'treasury_operator'),($1,'treasury_approver'),($2,'treasury_approver')`, operator.userID, approver.userID); err != nil {
		t.Fatal(err)
	}
	var systemAccountID string
	if err = pool.QueryRow(ctx, `INSERT INTO accounts(kind,name,status) VALUES('system',$1,'active') RETURNING id::text`, fixture+"-system").Scan(&systemAccountID); err != nil {
		t.Fatal(err)
	}
	seedP2PBalance(t, ctx, pool, fixture+"-seed", systemAccountID, seller.accountID, assetID, "1000")

	service := p2p.NewService(p2p.NewPostgresStore(pool))
	create := func(key, amount string) p2p.Trade {
		trade, createErr := service.Create(ctx, p2p.CreateInput{UserID: seller.userID, AccountID: seller.accountID, AssetSymbol: assetSymbol, AssetNetwork: assetNetwork,
			AmountAtomic: amount, FiatCurrency: "NGN", FiatAmount: "150000.00000000", PaymentMethod: "bank_transfer", ExpiresInSeconds: 600, IdempotencyKey: key})
		if createErr != nil {
			t.Fatalf("create %s: %v", key, createErr)
		}
		return trade
	}

	normal := create(fixture+"-normal-create", "100")
	duplicate, err := service.Create(ctx, p2p.CreateInput{UserID: seller.userID, AccountID: seller.accountID, AssetSymbol: assetSymbol, AssetNetwork: assetNetwork,
		AmountAtomic: "100", FiatCurrency: "NGN", FiatAmount: "150000.00000000", PaymentMethod: "bank_transfer", ExpiresInSeconds: 600, IdempotencyKey: fixture + "-normal-create"})
	if err != nil || duplicate.ID != normal.ID || !duplicate.Duplicate {
		t.Fatalf("idempotent create duplicate=%+v err=%v", duplicate, err)
	}
	_, err = service.Create(ctx, p2p.CreateInput{UserID: seller.userID, AccountID: seller.accountID, AssetSymbol: assetSymbol, AssetNetwork: assetNetwork,
		AmountAtomic: "101", FiatCurrency: "NGN", FiatAmount: "150000.00000000", PaymentMethod: "bank_transfer", ExpiresInSeconds: 600, IdempotencyKey: fixture + "-normal-create"})
	if !errors.Is(err, p2p.ErrIdempotencyConflict) {
		t.Fatalf("conflicting create accepted: %v", err)
	}
	normal, err = service.Accept(ctx, p2p.ActionInput{UserID: buyerOne.userID, AccountID: buyerOne.accountID, TradeID: normal.ID, IdempotencyKey: fixture + "-normal-accept"})
	if err != nil || normal.Status != "accepted" {
		t.Fatalf("accept: %+v %v", normal, err)
	}
	normal, err = service.MarkPaid(ctx, p2p.ActionInput{UserID: buyerOne.userID, AccountID: buyerOne.accountID, TradeID: normal.ID, IdempotencyKey: fixture + "-normal-paid"})
	if err != nil || normal.Status != "paid" {
		t.Fatalf("mark paid: %+v %v", normal, err)
	}
	normal, err = service.Release(ctx, p2p.ActionInput{UserID: seller.userID, AccountID: seller.accountID, TradeID: normal.ID, IdempotencyKey: fixture + "-normal-release"})
	if err != nil || normal.Status != "released" {
		t.Fatalf("release: %+v %v", normal, err)
	}

	disputed := create(fixture+"-disputed-create", "100")
	openOffers, err := service.ListOpen(ctx, 200, 0)
	if err != nil || !containsP2PStatus(openOffers, disputed.ID, "open") {
		t.Fatalf("open offer discovery result=%+v err=%v", openOffers, err)
	}
	type acceptResult struct {
		trade p2p.Trade
		err   error
		user  p2pFixtureUser
	}
	results := make(chan acceptResult, 2)
	var wait sync.WaitGroup
	for index, buyer := range []p2pFixtureUser{buyerOne, buyerTwo} {
		wait.Add(1)
		go func(index int, buyer p2pFixtureUser) {
			defer wait.Done()
			trade, acceptErr := service.Accept(ctx, p2p.ActionInput{UserID: buyer.userID, AccountID: buyer.accountID, TradeID: disputed.ID, IdempotencyKey: fmt.Sprintf("%s-race-%d", fixture, index)})
			results <- acceptResult{trade: trade, err: acceptErr, user: buyer}
		}(index, buyer)
	}
	wait.Wait()
	close(results)
	var winner p2pFixtureUser
	successes := 0
	for result := range results {
		if result.err == nil {
			successes++
			winner = result.user
		} else if !errors.Is(result.err, p2p.ErrInvalidTransition) {
			t.Fatalf("unexpected accept race error: %v", result.err)
		}
	}
	if successes != 1 {
		t.Fatalf("accept race successes=%d", successes)
	}
	disputed, err = service.MarkPaid(ctx, p2p.ActionInput{UserID: winner.userID, AccountID: winner.accountID, TradeID: disputed.ID, IdempotencyKey: fixture + "-race-paid"})
	if err != nil {
		t.Fatal(err)
	}
	disputed, err = service.Dispute(ctx, p2p.DisputeInput{ActionInput: p2p.ActionInput{UserID: seller.userID, AccountID: seller.accountID, TradeID: disputed.ID, IdempotencyKey: fixture + "-dispute"}, Reason: "buyer payment requires operations review"})
	if err != nil || disputed.Status != "disputed" {
		t.Fatalf("dispute: %+v %v", disputed, err)
	}
	disputeQueue, err := service.ListDisputes(ctx, operator.userID, 200, 0)
	if err != nil || !containsP2PStatus(disputeQueue, disputed.ID, "disputed") {
		t.Fatalf("dispute queue result=%+v err=%v", disputeQueue, err)
	}
	if _, err = service.ListDisputes(ctx, seller.userID, 200, 0); !errors.Is(err, p2p.ErrApprovalRole) {
		t.Fatalf("customer read treasury dispute queue: %v", err)
	}
	disputed, err = service.AddEvidence(ctx, p2p.EvidenceInput{ActionInput: p2p.ActionInput{UserID: winner.userID, AccountID: winner.accountID, TradeID: disputed.ID, IdempotencyKey: fixture + "-evidence"}, EvidenceType: "payment_receipt", ObjectKey: "p2p/" + disputed.ID + "/receipt", SHA256Hex: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := service.ProposeResolution(ctx, p2p.ResolutionInput{ActorID: operator.userID, TradeID: disputed.ID, Action: "force_release", Reason: "verified receipt and counterparty confirmation", IdempotencyKey: fixture + "-propose"})
	if err != nil || resolution.Status != "pending" {
		t.Fatalf("propose: %+v %v", resolution, err)
	}
	if _, err = service.ApproveResolution(ctx, p2p.ApprovalInput{ActorID: operator.userID, ResolutionID: resolution.ID, IdempotencyKey: fixture + "-same-approver"}); !errors.Is(err, p2p.ErrSameApprover) {
		t.Fatalf("same maker approved resolution: %v", err)
	}
	disputed, err = service.ApproveResolution(ctx, p2p.ApprovalInput{ActorID: approver.userID, ResolutionID: resolution.ID, IdempotencyKey: fixture + "-approve"})
	if err != nil || disputed.Status != "released" {
		t.Fatalf("approve resolution: %+v %v", disputed, err)
	}
	approvedDuplicate, err := service.ApproveResolution(ctx, p2p.ApprovalInput{ActorID: approver.userID, ResolutionID: resolution.ID, IdempotencyKey: fixture + "-approve"})
	if err != nil || approvedDuplicate.ID != disputed.ID || !approvedDuplicate.Duplicate {
		t.Fatalf("duplicate approval: %+v %v", approvedDuplicate, err)
	}

	expired := create(fixture+"-expire-create", "25")
	if _, err = pool.Exec(ctx, `UPDATE p2p_trades SET offer_expires_at=now()-interval '1 second' WHERE id=$1`, expired.ID); err != nil {
		t.Fatal(err)
	}
	expiredTrades, err := service.Expire(ctx, 100)
	if err != nil || !containsP2PStatus(expiredTrades, expired.ID, "expired") {
		t.Fatalf("expire result=%+v err=%v", expiredTrades, err)
	}

	cancelled := create(fixture+"-cancel-create", "10")
	cancelled, err = service.Cancel(ctx, p2p.ActionInput{UserID: seller.userID, AccountID: seller.accountID, TradeID: cancelled.ID, IdempotencyKey: fixture + "-cancel"})
	if err != nil || cancelled.Status != "cancelled" {
		t.Fatalf("cancel: %+v %v", cancelled, err)
	}

	unpaid := create(fixture+"-unpaid-create", "15")
	unpaid, err = service.Accept(ctx, p2p.ActionInput{UserID: buyerTwo.userID, AccountID: buyerTwo.accountID, TradeID: unpaid.ID, IdempotencyKey: fixture + "-unpaid-accept"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE p2p_trades SET payment_deadline=now()-interval '1 second' WHERE id=$1`, unpaid.ID); err != nil {
		t.Fatal(err)
	}
	expiredTrades, err = service.Expire(ctx, 100)
	if err != nil || !containsP2PStatus(expiredTrades, unpaid.ID, "expired") {
		t.Fatalf("unpaid expiry result=%+v err=%v", expiredTrades, err)
	}

	assertP2PBalance(t, ctx, pool, seller.accountID, assetID, "available", "800")
	assertP2PBalance(t, ctx, pool, seller.accountID, assetID, "held", "0")
	var debit, credit string
	if err = pool.QueryRow(ctx, `SELECT COALESCE(SUM(amount_atomic) FILTER (WHERE direction='debit'),0)::text,COALESCE(SUM(amount_atomic) FILTER (WHERE direction='credit'),0)::text
		FROM postings p JOIN journals j ON j.id=p.journal_id WHERE j.reference_type LIKE 'p2p_%' AND j.reference_id IN (SELECT id::text FROM p2p_trades WHERE seller_user_id=$1)`, seller.userID).Scan(&debit, &credit); err != nil || debit != credit {
		t.Fatalf("P2P ledger imbalance debit=%s credit=%s err=%v", debit, credit, err)
	}
	var auditCount, outboxCount int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE resource_type='p2p_resolution' AND resource_id=$1`, resolution.ID).Scan(&auditCount); err != nil || auditCount != 2 {
		t.Fatalf("resolution audit count=%d err=%v", auditCount, err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE aggregate_type='p2p_trade' AND aggregate_id=$1`, disputed.ID).Scan(&outboxCount); err != nil || outboxCount < 6 {
		t.Fatalf("P2P outbox count=%d err=%v", outboxCount, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE p2p_evidence SET object_key='tampered' WHERE trade_id=$1`, disputed.ID); err == nil {
		t.Fatal("immutable evidence was updated")
	}
	if _, err = pool.Exec(ctx, `DELETE FROM p2p_events WHERE trade_id=$1`, disputed.ID); err == nil {
		t.Fatal("immutable event stream was deleted")
	}
	if _, err = service.Get(ctx, buyerTwo.userID, normal.ID); !errors.Is(err, p2p.ErrTradeNotFound) {
		t.Fatalf("nonparticipant read was not hidden: %v", err)
	}
}

type p2pFixtureUser struct{ userID, accountID string }

func seedP2PUser(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture string) p2pFixtureUser {
	t.Helper()
	var result p2pFixtureUser
	if err := pool.QueryRow(ctx, `INSERT INTO users(email,password_hash,country_code,status) VALUES($1,$2,'NG','active') RETURNING id::text`, fixture+"@example.test", "p2p-test-hash").Scan(&result.userID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO accounts(user_id,kind,name,status) VALUES($1,'funding',$2,'active') RETURNING id::text`, result.userID, fixture+"-funding").Scan(&result.accountID); err != nil {
		t.Fatal(err)
	}
	return result
}

func seedP2PBalance(t *testing.T, ctx context.Context, pool *pgxpool.Pool, key, systemAccountID, userAccountID, assetID, amount string) {
	t.Helper()
	var journalID string
	if err := pool.QueryRow(ctx, `INSERT INTO journals(idempotency_key,reference_type,reference_id) VALUES($1,'p2p_test_seed',$1) RETURNING id::text`, key).Scan(&journalID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO postings(journal_id,account_id,asset_id,bucket,direction,amount_atomic) VALUES
		($1,$2,$4,'available','debit',$5::numeric),($1,$3,$4,'available','credit',$5::numeric)`, journalID, systemAccountID, userAccountID, assetID, amount); err != nil {
		t.Fatal(err)
	}
}

func assertP2PBalance(t *testing.T, ctx context.Context, pool *pgxpool.Pool, accountID, assetID, bucket, want string) {
	t.Helper()
	var got string
	if err := pool.QueryRow(ctx, `SELECT COALESCE(SUM(CASE direction WHEN 'credit' THEN amount_atomic ELSE -amount_atomic END),0)::text FROM postings p JOIN journals j ON j.id=p.journal_id AND j.status='posted' WHERE account_id=$1 AND asset_id=$2 AND bucket=$3::balance_bucket`, accountID, assetID, bucket).Scan(&got); err != nil || got != want {
		t.Fatalf("balance %s got=%s want=%s err=%v", bucket, got, want, err)
	}
}

func containsP2PStatus(trades []p2p.Trade, id, status string) bool {
	for _, trade := range trades {
		if trade.ID == id && trade.Status == status {
			return true
		}
	}
	return false
}
