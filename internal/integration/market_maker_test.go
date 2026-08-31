package integration

import (
	"context"
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
