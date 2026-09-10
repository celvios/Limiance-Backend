package integration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/limiance/backend/internal/marketmaker"
)

func TestInternalMarketMakerDefaultsAndRestartDeduplication(t *testing.T) {
	databaseURL := os.Getenv("LIMIANCE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set LIMIANCE_TEST_DATABASE_URL to run PostgreSQL market-maker tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	firstStore := marketmaker.NewPostgresStore(pool)
	control, err := firstStore.LoadControl(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if control.Enabled || !control.DryRun || !control.KillSwitch || control.UserID != "" || control.AccountID != "" {
		t.Fatalf("unsafe default control: %+v", control)
	}
	var pairCount, configCount, enabledCount int
	if err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM trading_pairs),(SELECT count(*) FROM market_maker_configs),(SELECT count(*) FROM market_maker_configs WHERE enabled)`).Scan(&pairCount, &configCount, &enabledCount); err != nil {
		t.Fatal(err)
	}
	if pairCount == 0 || configCount != pairCount || enabledCount != 0 {
		t.Fatalf("pairs=%d configs=%d enabled=%d", pairCount, configCount, enabledCount)
	}

	var pair string
	if err = pool.QueryRow(ctx, `SELECT symbol FROM trading_pairs ORDER BY symbol LIMIT 1`).Scan(&pair); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE market_maker_control SET enabled=TRUE,dry_run=TRUE,kill_switch=FALSE WHERE singleton`); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(context.Background(), `UPDATE market_maker_control SET enabled=FALSE,dry_run=TRUE,kill_switch=TRUE,user_id=NULL,account_id=NULL WHERE singleton`)
	key := fmt.Sprintf("market-maker-integration:%d", time.Now().UnixNano())
	defer pool.Exec(context.Background(), `DELETE FROM market_maker_command_log WHERE idempotency_key=$1`, key)
	claimed, err := firstStore.ClaimCommand(ctx, key, pair, "BUY", "100", "1", true)
	if err != nil || !claimed {
		t.Fatalf("first claim=%v err=%v", claimed, err)
	}
	secondStore := marketmaker.NewPostgresStore(pool)
	claimed, err = secondStore.ClaimCommand(ctx, key, pair, "BUY", "100", "1", true)
	if err != nil || claimed {
		t.Fatalf("restart duplicate claim=%v err=%v", claimed, err)
	}
}

func TestMarketMakerCommandClaimCannotCrossCommittedEmergencyStop(t *testing.T) {
	databaseURL := os.Getenv("LIMIANCE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set LIMIANCE_TEST_DATABASE_URL to run PostgreSQL market-maker tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err = pool.Exec(ctx, `UPDATE market_maker_control SET enabled=TRUE,dry_run=TRUE,kill_switch=FALSE WHERE singleton`); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(context.Background(), `UPDATE market_maker_control SET enabled=FALSE,dry_run=TRUE,kill_switch=TRUE,user_id=NULL,account_id=NULL WHERE singleton`)

	var pair string
	if err = pool.QueryRow(ctx, `SELECT symbol FROM trading_pairs ORDER BY symbol LIMIT 1`).Scan(&pair); err != nil {
		t.Fatal(err)
	}
	stopTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer stopTx.Rollback(context.Background())
	if _, err = stopTx.Exec(ctx, `SELECT singleton FROM market_maker_control WHERE singleton FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	if _, err = stopTx.Exec(ctx, `UPDATE market_maker_control SET kill_switch=TRUE WHERE singleton`); err != nil {
		t.Fatal(err)
	}

	key := fmt.Sprintf("market-maker-stop-race:%d", time.Now().UnixNano())
	defer pool.Exec(context.Background(), `DELETE FROM market_maker_command_log WHERE idempotency_key=$1`, key)
	type claimResult struct {
		claimed bool
		err     error
	}
	result := make(chan claimResult, 1)
	go func() {
		claimed, claimErr := marketmaker.NewPostgresStore(pool).ClaimCommand(ctx, key, pair, "BUY", "100", "1", true)
		result <- claimResult{claimed: claimed, err: claimErr}
	}()
	select {
	case early := <-result:
		t.Fatalf("command claim crossed the uncommitted stop lock: %+v", early)
	case <-time.After(100 * time.Millisecond):
	}
	if err = stopTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-result:
		if got.claimed || !errors.Is(got.err, marketmaker.ErrKillSwitch) {
			t.Fatalf("claim after committed stop=%+v", got)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM market_maker_command_log WHERE idempotency_key=$1`, key).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("commands recorded after stop: %d", count)
	}
}
