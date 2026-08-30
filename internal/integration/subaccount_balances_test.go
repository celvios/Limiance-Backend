package integration

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/limiance/backend/internal/datamanager"
)

func TestSubaccountBalancesEmptyAndPostedOnly(t *testing.T) {
	databaseURL := os.Getenv("LIMIANCE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set LIMIANCE_TEST_DATABASE_URL to run PostgreSQL balance tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	defer pool.Close()

	fixture := fmt.Sprintf("subaccount-balance-%d", time.Now().UnixNano())
	var userID, accountID, assetID string
	if err = pool.QueryRow(ctx, `INSERT INTO users(email,password_hash,country_code,status) VALUES($1,$2,'NG','active') RETURNING id::text`, fixture+"@example.test", "test-only").Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err = pool.QueryRow(ctx, `INSERT INTO accounts(user_id,kind,name) VALUES($1,'subaccount',$2) RETURNING id::text`, userID, fixture).Scan(&accountID); err != nil {
		t.Fatalf("create account: %v", err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO subaccounts(main_account_id,account_id,nickname,type,account_mode) VALUES($1,$2,'Test123','standard','uta')`, userID, accountID); err != nil {
		t.Fatalf("create subaccount: %v", err)
	}
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM postings WHERE account_id=$1`, accountID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM journals WHERE idempotency_key LIKE $1`, fixture+"%")
		_, _ = pool.Exec(context.Background(), `DELETE FROM assets WHERE id=$1`, assetID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM subaccounts WHERE account_id=$1`, accountID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM accounts WHERE id=$1`, accountID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID)
	}()

	manager := datamanager.New(pool)
	balances, err := manager.SubaccountBalances(ctx, userID, accountID)
	if err != nil {
		t.Fatalf("empty subaccount balance returned an error: %v", err)
	}
	if len(balances) != 0 {
		t.Fatalf("empty subaccount returned %d balance rows, want 0", len(balances))
	}

	if err = pool.QueryRow(ctx, `INSERT INTO assets(symbol,network,decimals,status) VALUES($1,'TEST',0,'enabled') RETURNING id::text`, fixture).Scan(&assetID); err != nil {
		t.Fatalf("create asset: %v", err)
	}
	var reversedJournalID string
	if err = pool.QueryRow(ctx, `INSERT INTO journals(idempotency_key,reference_type,reference_id,status) VALUES($1,'test',$2,'reversed') RETURNING id::text`, fixture+"-reversed", fixture).Scan(&reversedJournalID); err != nil {
		t.Fatalf("create reversed journal: %v", err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO postings(journal_id,account_id,asset_id,bucket,direction,amount_atomic) VALUES($1,$2,$3,'available','credit',999)`, reversedJournalID, accountID, assetID); err != nil {
		t.Fatalf("create reversed posting: %v", err)
	}
	balances, err = manager.SubaccountBalances(ctx, userID, accountID)
	if err != nil || len(balances) != 0 {
		t.Fatalf("reversed journal affected balances: balances=%v err=%v", balances, err)
	}

	var postedJournalID string
	if err = pool.QueryRow(ctx, `INSERT INTO journals(idempotency_key,reference_type,reference_id,status) VALUES($1,'test',$2,'posted') RETURNING id::text`, fixture+"-posted", fixture).Scan(&postedJournalID); err != nil {
		t.Fatalf("create posted journal: %v", err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO postings(journal_id,account_id,asset_id,bucket,direction,amount_atomic) VALUES($1,$2,$3,'available','credit',42)`, postedJournalID, accountID, assetID); err != nil {
		t.Fatalf("create posted posting: %v", err)
	}
	balances, err = manager.SubaccountBalances(ctx, userID, accountID)
	if err != nil {
		t.Fatalf("read posted balance: %v", err)
	}
	if len(balances) != 1 || balances[0].AvailableAtomic != "42" {
		t.Fatalf("posted balance = %v, want one row with 42 available", balances)
	}
}
