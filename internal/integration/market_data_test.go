package integration

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/limiance/backend/internal/marketdata"
)

func TestMarketDataReadModelIsSequencedIdempotentAndIntegerOnly(t *testing.T) {
	databaseURL := os.Getenv("LIMIANCE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set LIMIANCE_TEST_DATABASE_URL to run PostgreSQL market data tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	fixture := fmt.Sprintf("M%07X", time.Now().UnixNano()&0xfffffff)
	base, quote := fixture+"B", fixture+"Q"
	pair := base + quote
	var baseID, quoteID string
	if err = pool.QueryRow(ctx, `INSERT INTO assets(symbol,network,decimals,status) VALUES($1,$2,8,'enabled') RETURNING id::text`, base, fixture).Scan(&baseID); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `INSERT INTO assets(symbol,network,decimals,status) VALUES($1,$2,8,'enabled') RETURNING id::text`, quote, fixture).Scan(&quoteID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO trading_pairs(symbol,base_asset_id,quote_asset_id,status) VALUES($1,$2,$3,'active')`, pair, baseID, quoteID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM market_trade_events WHERE pair=$1`, pair)
		_, _ = pool.Exec(context.Background(), `DELETE FROM market_candles_1m WHERE pair=$1`, pair)
		_, _ = pool.Exec(context.Background(), `DELETE FROM market_stream_sequences WHERE pair=$1`, pair)
		_, _ = pool.Exec(context.Background(), `DELETE FROM trading_pairs WHERE symbol=$1`, pair)
		_, _ = pool.Exec(context.Background(), `DELETE FROM assets WHERE id=ANY($1::uuid[])`, []string{baseID, quoteID})
	}()

	store := marketdata.NewPostgresStore(pool)
	start := time.Date(2026, 8, 29, 10, 0, 5, 0, time.UTC)
	first := marketdata.Trade{Pair: pair, SequenceID: 1, Timestamp: start, PriceAtomic: "50000", QuantityAtomic: "2", QuoteAtomic: "100000", Side: "BUY", PayloadHash: sha256.Sum256([]byte("trade-1"))}
	early := first
	early.SequenceID = 2
	early.PayloadHash = sha256.Sum256([]byte("early-trade-2"))
	if _, _, err = store.ApplyTrade(ctx, early); !errors.Is(err, marketdata.ErrMarketSequenceGap) {
		t.Fatalf("initial trade gap was not detected: %v", err)
	}
	candle, duplicate, err := store.ApplyTrade(ctx, first)
	if err != nil || duplicate || candle.Open != "50000" {
		t.Fatalf("first trade: candle=%+v duplicate=%v err=%v", candle, duplicate, err)
	}
	_, duplicate, err = store.ApplyTrade(ctx, first)
	if err != nil || !duplicate {
		t.Fatalf("duplicate trade changed read model: duplicate=%v err=%v", duplicate, err)
	}
	conflict := first
	conflict.PayloadHash = sha256.Sum256([]byte("conflict"))
	if _, _, err = store.ApplyTrade(ctx, conflict); !errors.Is(err, marketdata.ErrMarketSequenceConflict) {
		t.Fatalf("conflicting duplicate accepted: %v", err)
	}
	gapTrade := marketdata.Trade{Pair: pair, SequenceID: 3, Timestamp: start.Add(2 * time.Second), PriceAtomic: "49000", QuantityAtomic: "3", QuoteAtomic: "147000", Side: "SELL", PayloadHash: sha256.Sum256([]byte("trade-3"))}
	if _, _, err = store.ApplyTrade(ctx, gapTrade); !errors.Is(err, marketdata.ErrMarketSequenceGap) {
		t.Fatalf("expected gap, got %v", err)
	}
	var mode string
	if err = pool.QueryRow(ctx, `SELECT mode FROM market_stream_sequences WHERE pair=$1 AND stream='trades'`, pair).Scan(&mode); err != nil || mode != "replay_required" {
		t.Fatalf("gap mode=%s err=%v", mode, err)
	}
	second := marketdata.Trade{Pair: pair, SequenceID: 2, Timestamp: start.Add(time.Second), PriceAtomic: "51000", QuantityAtomic: "4", QuoteAtomic: "204000", Side: "BUY", PayloadHash: sha256.Sum256([]byte("trade-2"))}
	if _, _, err = store.ApplyTrade(ctx, second); err != nil {
		t.Fatal(err)
	}
	candle, _, err = store.ApplyTrade(ctx, gapTrade)
	if err != nil {
		t.Fatal(err)
	}
	if candle.Open != "50000" || candle.High != "51000" || candle.Low != "49000" || candle.Close != "49000" || candle.Volume != "9" || candle.QuoteVolume != "451000" || candle.TradeCount != 3 {
		t.Fatalf("integer candle mismatch: %+v", candle)
	}
	ticker, err := store.Ticker(ctx, pair, start.Add(time.Hour))
	if err != nil || ticker.LastPrice != "49000" || ticker.High24H != "51000" || ticker.Low24H != "49000" || ticker.Volume24H != "9" || ticker.QuoteVolume24H != "451000" || ticker.Change24H != "-1000" {
		t.Fatalf("ticker mismatch: %+v err=%v", ticker, err)
	}
	trades, err := store.RecentTrades(ctx, pair, 10)
	if err != nil || len(trades) != 3 || trades[0].SequenceID != 3 || trades[2].SequenceID != 1 {
		t.Fatalf("recent trades mismatch: %+v err=%v", trades, err)
	}
	accepted, duplicate, err := store.ClaimOrderBookSequence(ctx, pair, 100)
	if err != nil || !accepted || duplicate {
		t.Fatalf("initial book sequence: accepted=%v duplicate=%v err=%v", accepted, duplicate, err)
	}
	if _, _, err = store.ClaimOrderBookSequence(ctx, pair, 102); !errors.Is(err, marketdata.ErrMarketSequenceGap) {
		t.Fatalf("book gap not detected: %v", err)
	}
	if accepted, _, err = store.ClaimOrderBookSequence(ctx, pair, 101); err != nil || !accepted {
		t.Fatalf("book replay did not resume: accepted=%v err=%v", accepted, err)
	}
}
