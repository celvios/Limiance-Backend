package integration

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/limiance/backend/internal/ledger"
)

func TestConcurrentDebitsCannotOverspendAvailableBalance(t *testing.T) {
	databaseURL := os.Getenv("LIMIANCE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set LIMIANCE_TEST_DATABASE_URL to run PostgreSQL ledger concurrency tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	defer pool.Close()

	fixture := fmt.Sprintf("ledger-concurrency-%d", time.Now().UnixNano())
	var sourceID, customerID, sinkID, assetID string
	err = pool.QueryRow(ctx, `INSERT INTO accounts(user_id,kind,name) VALUES
		(NULL,'system',$1 || '-source'),(NULL,'system',$1 || '-customer'),(NULL,'system',$1 || '-sink')
		RETURNING id::text`, fixture).Scan(&sourceID)
	if err != nil {
		// PostgreSQL returns only the first row through QueryRow; fetch all three
		// explicitly below to keep cleanup identities deterministic.
		t.Fatalf("create source account: %v", err)
	}
	if err = pool.QueryRow(ctx, `SELECT id::text FROM accounts WHERE name=$1 || '-customer'`, fixture).Scan(&customerID); err != nil {
		t.Fatalf("find customer account: %v", err)
	}
	if err = pool.QueryRow(ctx, `SELECT id::text FROM accounts WHERE name=$1 || '-sink'`, fixture).Scan(&sinkID); err != nil {
		t.Fatalf("find sink account: %v", err)
	}
	if err = pool.QueryRow(ctx, `INSERT INTO assets(symbol,network,decimals,status) VALUES ($1,'TEST',0,'enabled') RETURNING id::text`, fixture).Scan(&assetID); err != nil {
		t.Fatalf("create asset: %v", err)
	}
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM postings WHERE account_id=ANY($1::uuid[])`, []string{sourceID, customerID, sinkID})
		_, _ = pool.Exec(context.Background(), `DELETE FROM journals WHERE idempotency_key LIKE $1`, fixture+"%")
		_, _ = pool.Exec(context.Background(), `DELETE FROM assets WHERE id=$1`, assetID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM accounts WHERE id=ANY($1::uuid[])`, []string{sourceID, customerID, sinkID})
	}()

	var openingJournal string
	if err = pool.QueryRow(ctx, `INSERT INTO journals(idempotency_key,reference_type,reference_id) VALUES($1,'test_opening',$2) RETURNING id::text`, fixture+"-opening", fixture).Scan(&openingJournal); err != nil {
		t.Fatalf("create opening journal: %v", err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO postings(journal_id,account_id,asset_id,bucket,direction,amount_atomic) VALUES
		($1,$2,$4,'available','debit',100),($1,$3,$4,'available','credit',100)`, openingJournal, sourceID, customerID, assetID); err != nil {
		t.Fatalf("seed opening balance: %v", err)
	}

	start := make(chan struct{})
	var succeeded atomic.Int32
	var wg sync.WaitGroup
	for attempt := 0; attempt < 2; attempt++ {
		wg.Add(1)
		go func(attempt int) {
			defer wg.Done()
			<-start
			posted, debitErr := postTestDebit(ctx, pool, fixture, attempt, customerID, sinkID, assetID, 80)
			if debitErr != nil {
				t.Errorf("concurrent debit %d: %v", attempt, debitErr)
				return
			}
			if posted {
				succeeded.Add(1)
			}
		}(attempt)
	}
	close(start)
	wg.Wait()
	if got := succeeded.Load(); got != 1 {
		t.Fatalf("expected exactly one debit to succeed, got %d", got)
	}
	var available int64
	if err = pool.QueryRow(ctx, `SELECT COALESCE(SUM(CASE direction WHEN 'credit' THEN amount_atomic ELSE -amount_atomic END),0)::bigint FROM postings WHERE account_id=$1 AND asset_id=$2 AND bucket='available'`, customerID, assetID).Scan(&available); err != nil {
		t.Fatalf("read final balance: %v", err)
	}
	if available != 20 {
		t.Fatalf("expected final balance 20, got %d", available)
	}
}

func postTestDebit(ctx context.Context, pool *pgxpool.Pool, fixture string, attempt int, sourceID, sinkID, assetID string, amount int64) (bool, error) {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = ledger.LockAccountAsset(ctx, tx, sourceID, assetID); err != nil {
		return false, err
	}
	var available int64
	if err = tx.QueryRow(ctx, `SELECT COALESCE(SUM(CASE direction WHEN 'credit' THEN amount_atomic ELSE -amount_atomic END),0)::bigint FROM postings WHERE account_id=$1 AND asset_id=$2 AND bucket='available'`, sourceID, assetID).Scan(&available); err != nil {
		return false, err
	}
	if available < amount {
		return false, tx.Commit(ctx)
	}
	var journalID string
	if err = tx.QueryRow(ctx, `INSERT INTO journals(idempotency_key,reference_type,reference_id) VALUES($1,'test_debit',$2) RETURNING id::text`, fmt.Sprintf("%s-debit-%d", fixture, attempt), fixture).Scan(&journalID); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO postings(journal_id,account_id,asset_id,bucket,direction,amount_atomic) VALUES
		($1,$2,$4,'available','debit',$5),($1,$3,$4,'available','credit',$5)`, journalID, sourceID, sinkID, assetID, amount); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}
