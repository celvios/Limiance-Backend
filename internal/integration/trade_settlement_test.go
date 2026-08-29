package integration

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/limiance/backend/internal/trading"
	tradingpostgres "github.com/limiance/backend/internal/trading/postgres"
	"github.com/limiance/backend/internal/trading/protocol"
)

func TestTradeSettlementIsBalancedIdempotentAndReplaySafe(t *testing.T) {
	databaseURL := os.Getenv("LIMIANCE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set LIMIANCE_TEST_DATABASE_URL to run PostgreSQL trade settlement tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	defer pool.Close()

	fixture := fmt.Sprintf("settlement-%d", time.Now().UnixNano())
	ids := createSettlementFixture(t, ctx, pool, fixture)
	defer func() { cleanupSettlementFixture(pool, fixture, ids) }()
	store := tradingpostgres.New(pool)

	buyerHash := sha256.Sum256([]byte("buyer"))
	buyerOrderID := databaseUUID(t, ctx, pool)
	buyerOrder, err := store.CreateOrder(ctx, trading.CreateOrderCommand{
		OrderID: buyerOrderID, UserID: ids.buyerUserID, AccountID: ids.buyerAccountID, Pair: ids.pair, Side: "BUY", Type: "LIMIT",
		Price: 5000000000000, Quantity: 3000000, TimeInForce: "GTC", FeeTier: 0, IdempotencyKey: fixture + "-buyer",
		RequestHash: buyerHash, HoldAmount: "150150000000", EnginePayload: []byte{1},
	})
	if err != nil {
		t.Fatalf("create buyer order: %v", err)
	}
	sellerHash := sha256.Sum256([]byte("seller"))
	sellerOrderID := databaseUUID(t, ctx, pool)
	sellerOrder, err := store.CreateOrder(ctx, trading.CreateOrderCommand{
		OrderID: sellerOrderID, UserID: ids.sellerUserID, AccountID: ids.sellerAccountID, Pair: ids.pair, Side: "SELL", Type: "MARKET",
		Quantity: 3000000, TimeInForce: "IOC", FeeTier: 0, IdempotencyKey: fixture + "-seller",
		RequestHash: sellerHash, HoldAmount: "3000000", EnginePayload: []byte{1},
	})
	if err != nil {
		t.Fatalf("create seller order: %v", err)
	}
	if buyerOrder.Status != "PENDING" || sellerOrder.Status != "PENDING" {
		t.Fatalf("orders not pending before engine fills: buyer=%s seller=%s", buyerOrder.Status, sellerOrder.Status)
	}
	secondBuyerHash := sha256.Sum256([]byte("second-buyer-reservation"))
	secondBuyerOrderID := databaseUUID(t, ctx, pool)
	if _, err = store.CreateOrder(ctx, trading.CreateOrderCommand{
		OrderID: secondBuyerOrderID, UserID: ids.buyerUserID, AccountID: ids.buyerAccountID, Pair: ids.pair, Side: "BUY", Type: "LIMIT",
		Price: 5000000000000, Quantity: 200000, TimeInForce: "GTC", FeeTier: 0, IdempotencyKey: fixture + "-second-buyer",
		RequestHash: secondBuyerHash, HoldAmount: "10010000000", EnginePayload: []byte{1},
	}); err != nil {
		t.Fatalf("create independent buyer reservation: %v", err)
	}
	ids.orderIDs = []string{buyerOrderID, sellerOrderID, secondBuyerOrderID}

	events := make([]protocol.TradeEvent, 3)
	payloads := make([][]byte, 3)
	for index := range events {
		events[index] = protocol.TradeEvent{
			SequenceID: uint64(index + 1), TimestampNS: uint64(1724880000000000000 + index), Pair: ids.pair,
			MakerOrderID: buyerOrderID, TakerOrderID: sellerOrderID, MakerUserID: ids.buyerUserID, TakerUserID: ids.sellerUserID,
			Price: 5000000000000, Quantity: 1000000, MakerFeeBPS: 10, TakerFeeBPS: 10,
		}
		payloads[index], err = protocol.EncodeTradeEvent(events[index])
		if err != nil {
			t.Fatalf("encode event %d: %v", index+1, err)
		}
	}

	if _, err = store.SettleTrade(ctx, events[2], sha256.Sum256(payloads[2])); !errors.Is(err, trading.ErrSequenceGap) {
		t.Fatalf("expected sequence gap for event 3, got %v", err)
	}
	var mode string
	var lastSequence, replayThrough uint64
	if err = pool.QueryRow(ctx, `SELECT mode,last_sequence_id::bigint,replay_through_sequence_id::bigint FROM engine_symbol_sequences WHERE pair=$1`, ids.pair).Scan(&mode, &lastSequence, &replayThrough); err != nil {
		t.Fatalf("read gap state: %v", err)
	}
	if mode != "replay_required" || lastSequence != 0 || replayThrough != 3 {
		t.Fatalf("unexpected gap state: mode=%s last=%d through=%d", mode, lastSequence, replayThrough)
	}
	var pairStatus string
	if err = pool.QueryRow(ctx, `SELECT status FROM trading_pairs WHERE symbol=$1`, ids.pair).Scan(&pairStatus); err != nil || pairStatus != "halted" {
		t.Fatalf("sequence gap did not halt pair: status=%q err=%v", pairStatus, err)
	}

	var secondTrade trading.Trade
	for index, event := range events {
		settled, settleErr := store.SettleTrade(ctx, event, sha256.Sum256(payloads[index]))
		if settleErr != nil {
			t.Fatalf("settle sequence %d: %v", event.SequenceID, settleErr)
		}
		if index == 1 {
			secondTrade = settled
		}
	}
	duplicate, err := store.SettleTrade(ctx, events[1], sha256.Sum256(payloads[1]))
	if err != nil || !duplicate.Duplicate || duplicate.ID != secondTrade.ID {
		t.Fatalf("duplicate replay changed trade: trade=%+v err=%v", duplicate, err)
	}
	start := make(chan struct{})
	replayErrors := make(chan error, 8)
	var wait sync.WaitGroup
	for attempt := 0; attempt < 8; attempt++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			replayed, replayErr := store.SettleTrade(ctx, events[1], sha256.Sum256(payloads[1]))
			if replayErr != nil {
				replayErrors <- replayErr
				return
			}
			if !replayed.Duplicate || replayed.ID != secondTrade.ID {
				replayErrors <- fmt.Errorf("unexpected concurrent replay: %+v", replayed)
			}
		}()
	}
	close(start)
	wait.Wait()
	close(replayErrors)
	for replayErr := range replayErrors {
		t.Fatal(replayErr)
	}
	conflictingHash := sha256.Sum256([]byte("conflicting-event"))
	if _, err = store.SettleTrade(ctx, events[1], conflictingHash); !errors.Is(err, trading.ErrSequenceConflict) {
		t.Fatalf("expected conflicting duplicate rejection, got %v", err)
	}

	if err = pool.QueryRow(ctx, `SELECT mode,last_sequence_id::bigint,replay_through_sequence_id::bigint FROM engine_symbol_sequences WHERE pair=$1`, ids.pair).Scan(&mode, &lastSequence, &replayThrough); err != nil {
		t.Fatalf("read resumed sequence: %v", err)
	}
	if mode != "active" || lastSequence != 3 || replayThrough != 0 {
		t.Fatalf("symbol did not resume: mode=%s last=%d through=%d", mode, lastSequence, replayThrough)
	}
	if err = pool.QueryRow(ctx, `SELECT status FROM trading_pairs WHERE symbol=$1`, ids.pair).Scan(&pairStatus); err != nil || pairStatus != "active" {
		t.Fatalf("replayed pair did not resume: status=%q err=%v", pairStatus, err)
	}

	assertSettlementBalance(t, ctx, pool, ids.buyerAccountID, ids.quoteAssetID, "available", 39840000000)
	assertSettlementBalance(t, ctx, pool, ids.buyerAccountID, ids.quoteAssetID, "held", 10010000000)
	assertSettlementBalance(t, ctx, pool, ids.buyerAccountID, ids.baseAssetID, "available", 3000000)
	assertSettlementBalance(t, ctx, pool, ids.sellerAccountID, ids.baseAssetID, "available", 3000000)
	assertSettlementBalance(t, ctx, pool, ids.sellerAccountID, ids.baseAssetID, "held", 0)
	assertSettlementBalance(t, ctx, pool, ids.sellerAccountID, ids.quoteAssetID, "available", 149850000000)
	var feeAccountID string
	if err = pool.QueryRow(ctx, `SELECT account_id::text FROM trading_fee_accounts WHERE asset_id=$1`, ids.quoteAssetID).Scan(&feeAccountID); err != nil {
		t.Fatalf("find fee account: %v", err)
	}
	ids.feeAccountID = feeAccountID
	assertSettlementBalance(t, ctx, pool, feeAccountID, ids.quoteAssetID, "available", 300000000)

	var buyerStatus, buyerFilled, sellerStatus, sellerFilled string
	if err = pool.QueryRow(ctx, `SELECT status,filled_quantity::text FROM orders WHERE id=$1`, buyerOrderID).Scan(&buyerStatus, &buyerFilled); err != nil {
		t.Fatalf("read buyer order: %v", err)
	}
	if err = pool.QueryRow(ctx, `SELECT status,filled_quantity::text FROM orders WHERE id=$1`, sellerOrderID).Scan(&sellerStatus, &sellerFilled); err != nil {
		t.Fatalf("read seller order: %v", err)
	}
	if buyerStatus != "FILLED" || sellerStatus != "FILLED" || buyerFilled != "3000000" || sellerFilled != "3000000" {
		t.Fatalf("orders not settled once: buyer=%s/%s seller=%s/%s", buyerStatus, buyerFilled, sellerStatus, sellerFilled)
	}
	var trades, inbox, accruals int
	if err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM trades WHERE pair=$1 AND sequence_id IS NOT NULL`, ids.pair).Scan(&trades); err != nil {
		t.Fatalf("count trades: %v", err)
	}
	if err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM engine_trade_inbox WHERE pair=$1 AND processed_at IS NOT NULL`, ids.pair).Scan(&inbox); err != nil {
		t.Fatalf("count inbox: %v", err)
	}
	if err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM trade_fee_accruals WHERE trade_id IN (SELECT id FROM trades WHERE pair=$1)`, ids.pair).Scan(&accruals); err != nil {
		t.Fatalf("count fee accruals: %v", err)
	}
	if trades != 3 || inbox != 3 || accruals != 6 {
		t.Fatalf("unexpected persistence counts: trades=%d inbox=%d accruals=%d", trades, inbox, accruals)
	}
	rows, err := pool.Query(ctx, `SELECT p.asset_id::text,
		SUM(CASE WHEN p.direction='debit' THEN p.amount_atomic ELSE 0 END)::text,
		SUM(CASE WHEN p.direction='credit' THEN p.amount_atomic ELSE 0 END)::text
		FROM postings p JOIN journals j ON j.id=p.journal_id
		WHERE j.reference_type='trade_settlement' AND j.reference_id IN (SELECT id::text FROM trades WHERE pair=$1)
		GROUP BY p.asset_id`, ids.pair)
	if err != nil {
		t.Fatalf("reconcile settlement journals: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var assetID, debits, credits string
		if err = rows.Scan(&assetID, &debits, &credits); err != nil {
			t.Fatalf("scan reconciliation: %v", err)
		}
		if debits != credits {
			t.Fatalf("unbalanced asset %s: debits=%s credits=%s", assetID, debits, credits)
		}
	}
}

type settlementFixture struct {
	pair                                                       string
	buyerUserID, sellerUserID, buyerAccountID, sellerAccountID string
	systemAccountID, baseAssetID, quoteAssetID, feeAccountID   string
	orderIDs                                                   []string
}

func createSettlementFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture string) settlementFixture {
	t.Helper()
	var ids settlementFixture
	if err := pool.QueryRow(ctx, `INSERT INTO users(email,password_hash,country_code,status) VALUES($1,'test-only','NG','active') RETURNING id::text`, fixture+"-buyer@example.test").Scan(&ids.buyerUserID); err != nil {
		t.Fatalf("create buyer: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users(email,password_hash,country_code,status) VALUES($1,'test-only','NG','active') RETURNING id::text`, fixture+"-seller@example.test").Scan(&ids.sellerUserID); err != nil {
		t.Fatalf("create seller: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO accounts(user_id,kind,name) VALUES($1,'uta',$2) RETURNING id::text`, ids.buyerUserID, fixture+"-buyer").Scan(&ids.buyerAccountID); err != nil {
		t.Fatalf("create buyer account: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO accounts(user_id,kind,name) VALUES($1,'uta',$2) RETURNING id::text`, ids.sellerUserID, fixture+"-seller").Scan(&ids.sellerAccountID); err != nil {
		t.Fatalf("create seller account: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO accounts(kind,name) VALUES('system',$1) RETURNING id::text`, fixture+"-system").Scan(&ids.systemAccountID); err != nil {
		t.Fatalf("create system account: %v", err)
	}
	baseSymbol := fmt.Sprintf("B%08X", time.Now().UnixNano()&0xffffffff)
	quoteSymbol := fmt.Sprintf("Q%08X", (time.Now().UnixNano()+1)&0xffffffff)
	ids.pair = baseSymbol + quoteSymbol
	if err := pool.QueryRow(ctx, `INSERT INTO assets(symbol,network,decimals,status) VALUES($1,$3,8,'enabled'),($2,$3,8,'enabled') RETURNING id::text`, baseSymbol, quoteSymbol, fixture).Scan(&ids.baseAssetID); err != nil {
		t.Fatalf("create settlement assets: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT id::text FROM assets WHERE symbol=$1 AND network=$2`, quoteSymbol, fixture).Scan(&ids.quoteAssetID); err != nil {
		t.Fatalf("find quote asset: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO trading_pairs(symbol,base_asset_id,quote_asset_id,min_quantity_atomic,max_quantity_atomic,price_tick_atomic,quantity_step_atomic,status) VALUES($1,$2,$3,1,10000000,1,1,'active')`, ids.pair, ids.baseAssetID, ids.quoteAssetID); err != nil {
		t.Fatalf("create settlement pair: %v", err)
	}
	seedSettlementBalance(t, ctx, pool, fixture+"-quote", ids.systemAccountID, ids.buyerAccountID, ids.quoteAssetID, "200000000000")
	seedSettlementBalance(t, ctx, pool, fixture+"-base", ids.systemAccountID, ids.sellerAccountID, ids.baseAssetID, "6000000")
	return ids
}

func seedSettlementBalance(t *testing.T, ctx context.Context, pool *pgxpool.Pool, key, systemAccountID, customerAccountID, assetID, amount string) {
	t.Helper()
	var journalID string
	if err := pool.QueryRow(ctx, `INSERT INTO journals(idempotency_key,reference_type,reference_id) VALUES($1,'test_opening',$1) RETURNING id::text`, key).Scan(&journalID); err != nil {
		t.Fatalf("create opening journal: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO postings(journal_id,account_id,asset_id,bucket,direction,amount_atomic) VALUES
		($1,$2,$4,'available','debit',$5::numeric),($1,$3,$4,'available','credit',$5::numeric)`, journalID, systemAccountID, customerAccountID, assetID, amount); err != nil {
		t.Fatalf("seed settlement balance: %v", err)
	}
}

func assertSettlementBalance(t *testing.T, ctx context.Context, pool *pgxpool.Pool, accountID, assetID, bucket string, want int64) {
	t.Helper()
	var got int64
	if err := pool.QueryRow(ctx, `SELECT COALESCE(SUM(CASE direction WHEN 'credit' THEN amount_atomic ELSE -amount_atomic END),0)::bigint FROM postings WHERE account_id=$1 AND asset_id=$2 AND bucket=$3`, accountID, assetID, bucket).Scan(&got); err != nil {
		t.Fatalf("read %s balance: %v", bucket, err)
	}
	if got != want {
		t.Fatalf("balance account=%s asset=%s bucket=%s: got %d want %d", accountID, assetID, bucket, got, want)
	}
}

func cleanupSettlementFixture(pool *pgxpool.Pool, fixture string, ids settlementFixture) {
	ctx := context.Background()
	tradeIDs := make([]string, 0)
	if rows, err := pool.Query(ctx, `SELECT id::text FROM trades WHERE pair=$1 AND sequence_id IS NOT NULL`, ids.pair); err == nil {
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				tradeIDs = append(tradeIDs, id)
			}
		}
		rows.Close()
	}
	accountIDs := []string{ids.buyerAccountID, ids.sellerAccountID, ids.systemAccountID}
	if ids.feeAccountID == "" {
		_ = pool.QueryRow(ctx, `SELECT account_id::text FROM trading_fee_accounts WHERE asset_id=$1`, ids.quoteAssetID).Scan(&ids.feeAccountID)
	}
	if ids.feeAccountID != "" {
		accountIDs = append(accountIDs, ids.feeAccountID)
	}
	_, _ = pool.Exec(ctx, `DELETE FROM outbox_events WHERE (aggregate_type='trade' AND aggregate_id=ANY($1::text[])) OR (aggregate_type='order' AND aggregate_id=ANY($2::text[]))`, tradeIDs, ids.orderIDs)
	_, _ = pool.Exec(ctx, `DELETE FROM postings WHERE account_id=ANY($1::uuid[])`, accountIDs)
	_, _ = pool.Exec(ctx, `DELETE FROM trade_fee_accruals WHERE trade_id=ANY($1::uuid[])`, tradeIDs)
	_, _ = pool.Exec(ctx, `DELETE FROM trade_participants WHERE trade_id=ANY($1::uuid[])`, tradeIDs)
	_, _ = pool.Exec(ctx, `DELETE FROM trades WHERE id=ANY($1::uuid[])`, tradeIDs)
	_, _ = pool.Exec(ctx, `DELETE FROM engine_trade_inbox WHERE pair=$1`, ids.pair)
	_, _ = pool.Exec(ctx, `DELETE FROM engine_symbol_sequences WHERE pair=$1`, ids.pair)
	_, _ = pool.Exec(ctx, `DELETE FROM engine_control_commands WHERE order_id=ANY($1::uuid[])`, ids.orderIDs)
	_, _ = pool.Exec(ctx, `DELETE FROM orders WHERE id=ANY($1::uuid[])`, ids.orderIDs)
	_, _ = pool.Exec(ctx, `DELETE FROM journals WHERE reference_id=ANY($1::text[]) OR idempotency_key LIKE $2`, append(tradeIDs, ids.orderIDs...), fixture+"%")
	_, _ = pool.Exec(ctx, `DELETE FROM trading_fee_accounts WHERE asset_id=$1`, ids.quoteAssetID)
	_, _ = pool.Exec(ctx, `DELETE FROM trading_pairs WHERE symbol=$1`, ids.pair)
	_, _ = pool.Exec(ctx, `DELETE FROM assets WHERE id=ANY($1::uuid[])`, []string{ids.baseAssetID, ids.quoteAssetID})
	_, _ = pool.Exec(ctx, `DELETE FROM accounts WHERE id=ANY($1::uuid[])`, accountIDs)
	_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=ANY($1::uuid[])`, []string{ids.buyerUserID, ids.sellerUserID})
}
