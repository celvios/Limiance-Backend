package integration

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/limiance/backend/internal/trading"
	tradingpostgres "github.com/limiance/backend/internal/trading/postgres"
	"github.com/limiance/backend/internal/trading/protocol"
)

func TestOrderGatewayPostgresLifecycleAndConcurrentHolds(t *testing.T) {
	databaseURL := os.Getenv("LIMIANCE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set LIMIANCE_TEST_DATABASE_URL to run PostgreSQL order gateway tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	defer pool.Close()

	fixture := fmt.Sprintf("gateway-%d", time.Now().UnixNano())
	var userID, customerID, systemID, baseID, quoteID string
	if err = pool.QueryRow(ctx, `INSERT INTO users(email,password_hash,country_code,status) VALUES($1,$2,'NG','active') RETURNING id::text`, fixture+"@example.test", "test-only").Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err = pool.QueryRow(ctx, `INSERT INTO accounts(user_id,kind,name) VALUES($1,'uta',$2) RETURNING id::text`, userID, fixture+"-customer").Scan(&customerID); err != nil {
		t.Fatalf("create customer account: %v", err)
	}
	if err = pool.QueryRow(ctx, `INSERT INTO accounts(kind,name) VALUES('system',$1) RETURNING id::text`, fixture+"-system").Scan(&systemID); err != nil {
		t.Fatalf("create system account: %v", err)
	}
	baseSymbol := fmt.Sprintf("B%08X", time.Now().UnixNano()&0xffffffff)
	quoteSymbol := fmt.Sprintf("Q%08X", (time.Now().UnixNano()+1)&0xffffffff)
	pair := baseSymbol + quoteSymbol
	if err = pool.QueryRow(ctx, `INSERT INTO assets(symbol,network,decimals,status) VALUES($1,$3,8,'enabled'),($2,$3,8,'enabled') RETURNING id::text`, baseSymbol, quoteSymbol, fixture).Scan(&baseID); err != nil {
		t.Fatalf("create assets: %v", err)
	}
	if err = pool.QueryRow(ctx, `SELECT id::text FROM assets WHERE symbol=$1 AND network=$2`, quoteSymbol, fixture).Scan(&quoteID); err != nil {
		t.Fatalf("find quote asset: %v", err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO trading_pairs(symbol,base_asset_id,quote_asset_id,min_quantity_atomic,max_quantity_atomic,price_tick_atomic,quantity_step_atomic,status) VALUES($1,$2,$3,1,1000000,1,1,'active')`, pair, baseID, quoteID); err != nil {
		t.Fatalf("create trading pair: %v", err)
	}
	var openingJournal string
	if err = pool.QueryRow(ctx, `INSERT INTO journals(idempotency_key,reference_type,reference_id) VALUES($1,'test_opening',$2) RETURNING id::text`, fixture+"-opening", fixture).Scan(&openingJournal); err != nil {
		t.Fatalf("create opening journal: %v", err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO postings(journal_id,account_id,asset_id,bucket,direction,amount_atomic) VALUES
		($1,$2,$4,'available','debit',100),($1,$3,$4,'available','credit',100)`, openingJournal, systemID, customerID, quoteID); err != nil {
		t.Fatalf("seed quote balance: %v", err)
	}
	defer cleanupTradingGatewayFixture(pool, fixture, userID, customerID, systemID, baseID, quoteID, pair)

	store := tradingpostgres.New(pool)
	firstID := databaseUUID(t, ctx, pool)
	firstHash := sha256.Sum256([]byte("first-order"))
	first := trading.CreateOrderCommand{
		OrderID: firstID, UserID: userID, AccountID: customerID, Pair: pair, Side: "BUY", Type: "LIMIT",
		Price: 1, Quantity: 1, TimeInForce: "GTC", FeeTier: 0, IdempotencyKey: fixture + "-first",
		RequestHash: firstHash, HoldAmount: "20", EnginePayload: []byte{1},
	}
	created, err := store.CreateOrder(ctx, first)
	if err != nil || created.Status != "PENDING" {
		t.Fatalf("create order: status=%q err=%v", created.Status, err)
	}
	retried, err := store.CreateOrder(ctx, first)
	if err != nil || !retried.Duplicate || retried.ID != created.ID {
		t.Fatalf("idempotent retry: order=%+v err=%v", retried, err)
	}
	conflict := first
	conflict.OrderID = databaseUUID(t, ctx, pool)
	conflict.RequestHash = sha256.Sum256([]byte("different-body"))
	if _, err = store.CreateOrder(ctx, conflict); !errors.Is(err, trading.ErrIdempotencyConflict) {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}
	opened, err := store.ApplyEngineStatus(ctx, firstID, protocol.OrderStatusEvent{SequenceID: 1, OrderID: firstID, Status: protocol.OrderStatusOpen, RemainingQuantity: 1})
	if err != nil || opened.Status != "OPEN" {
		t.Fatalf("open order: status=%q err=%v", opened.Status, err)
	}
	listed, err := store.ListOrders(ctx, userID, customerID, trading.OrderFilter{Status: "OPEN", Limit: 10})
	if err != nil || len(listed) != 1 || listed[0].ID != firstID {
		t.Fatalf("list open orders: orders=%+v err=%v", listed, err)
	}
	commandID := databaseUUID(t, ctx, pool)
	prepared, err := store.PrepareCancel(ctx, trading.CancelOrderCommand{UserID: userID, AccountID: customerID, OrderID: firstID, CommandID: commandID, IdempotencyKey: fixture + "-cancel", Payload: []byte{2}})
	if err != nil || prepared.Status != "PENDING_CANCEL" {
		t.Fatalf("prepare cancellation: status=%q err=%v", prepared.Status, err)
	}
	canceled, err := store.ApplyCancelAck(ctx, userID, firstID, protocol.ControlAck{CommandID: commandID, SequenceID: 2, Accepted: true})
	if err != nil || canceled.Status != "CANCELED" {
		t.Fatalf("apply cancellation: status=%q err=%v", canceled.Status, err)
	}

	start := make(chan struct{})
	var succeeded atomic.Int32
	var insufficient atomic.Int32
	var wait sync.WaitGroup
	for attempt := 0; attempt < 2; attempt++ {
		wait.Add(1)
		go func(attempt int) {
			defer wait.Done()
			<-start
			orderID := databaseUUID(t, ctx, pool)
			hash := sha256.Sum256([]byte(fmt.Sprintf("concurrent-%d", attempt)))
			_, createErr := store.CreateOrder(ctx, trading.CreateOrderCommand{
				OrderID: orderID, UserID: userID, AccountID: customerID, Pair: pair, Side: "BUY", Type: "LIMIT",
				Price: 1, Quantity: 1, TimeInForce: "GTC", FeeTier: 0, IdempotencyKey: fmt.Sprintf("%s-concurrent-%d", fixture, attempt),
				RequestHash: hash, HoldAmount: "80", EnginePayload: []byte{1},
			})
			switch {
			case createErr == nil:
				succeeded.Add(1)
			case errors.Is(createErr, trading.ErrInsufficientBalance):
				insufficient.Add(1)
			default:
				t.Errorf("concurrent order %d: %v", attempt, createErr)
			}
		}(attempt)
	}
	close(start)
	wait.Wait()
	if succeeded.Load() != 1 || insufficient.Load() != 1 {
		t.Fatalf("expected one hold and one insufficient balance, got success=%d insufficient=%d", succeeded.Load(), insufficient.Load())
	}
	var available, held int64
	if err = pool.QueryRow(ctx, `SELECT
		COALESCE(SUM(CASE WHEN bucket='available' THEN CASE direction WHEN 'credit' THEN amount_atomic ELSE -amount_atomic END ELSE 0 END),0)::bigint,
		COALESCE(SUM(CASE WHEN bucket='held' THEN CASE direction WHEN 'credit' THEN amount_atomic ELSE -amount_atomic END ELSE 0 END),0)::bigint
		FROM postings WHERE account_id=$1 AND asset_id=$2`, customerID, quoteID).Scan(&available, &held); err != nil {
		t.Fatalf("read balances: %v", err)
	}
	if available != 20 || held != 80 {
		t.Fatalf("expected available=20 held=80, got available=%d held=%d", available, held)
	}
}

func databaseUUID(t *testing.T, ctx context.Context, pool *pgxpool.Pool) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		t.Fatalf("generate database UUID: %v", err)
	}
	return id
}

func cleanupTradingGatewayFixture(pool *pgxpool.Pool, fixture, userID, customerID, systemID, baseID, quoteID, pair string) {
	ctx := context.Background()
	orderIDs := make([]string, 0)
	if rows, err := pool.Query(ctx, `SELECT id::text FROM orders WHERE user_id=$1`, userID); err == nil {
		for rows.Next() {
			var orderID string
			if rows.Scan(&orderID) == nil {
				orderIDs = append(orderIDs, orderID)
			}
		}
		rows.Close()
	}
	_, _ = pool.Exec(ctx, `DELETE FROM outbox_events WHERE aggregate_type='order' AND aggregate_id IN (SELECT id::text FROM orders WHERE user_id=$1)`, userID)
	_, _ = pool.Exec(ctx, `DELETE FROM engine_control_commands WHERE user_id=$1`, userID)
	_, _ = pool.Exec(ctx, `DELETE FROM postings WHERE account_id=ANY($1::uuid[])`, []string{customerID, systemID})
	_, _ = pool.Exec(ctx, `DELETE FROM orders WHERE user_id=$1`, userID)
	if len(orderIDs) > 0 {
		_, _ = pool.Exec(ctx, `DELETE FROM journals WHERE reference_id=ANY($1::text[])`, orderIDs)
	}
	_, _ = pool.Exec(ctx, `DELETE FROM trading_pairs WHERE symbol=$1`, pair)
	_, _ = pool.Exec(ctx, `DELETE FROM journals WHERE idempotency_key LIKE $1`, fixture+"%")
	_, _ = pool.Exec(ctx, `DELETE FROM assets WHERE id=ANY($1::uuid[])`, []string{baseID, quoteID})
	_, _ = pool.Exec(ctx, `DELETE FROM accounts WHERE id=ANY($1::uuid[])`, []string{customerID, systemID})
	_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, userID)
}
