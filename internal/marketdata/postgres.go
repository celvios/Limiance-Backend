package marketdata

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Market struct {
	Pair          string `json:"pair"`
	BaseAsset     string `json:"base_asset"`
	QuoteAsset    string `json:"quote_asset"`
	PriceScale    int16  `json:"price_scale"`
	QuantityScale int16  `json:"quantity_scale"`
	Status        string `json:"status"`
}

type PostgresStore struct{ pool *pgxpool.Pool }

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

func (store *PostgresStore) Markets(ctx context.Context) ([]Market, error) {
	rows, err := store.pool.Query(ctx, `SELECT p.symbol,b.symbol,q.symbol,p.price_scale,p.quantity_scale,p.status
		FROM trading_pairs p JOIN assets b ON b.id=p.base_asset_id JOIN assets q ON q.id=p.quote_asset_id
		WHERE p.status <> 'disabled' ORDER BY p.symbol`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	markets := make([]Market, 0)
	for rows.Next() {
		var market Market
		if err = rows.Scan(&market.Pair, &market.BaseAsset, &market.QuoteAsset, &market.PriceScale, &market.QuantityScale, &market.Status); err != nil {
			return nil, err
		}
		markets = append(markets, market)
	}
	return markets, rows.Err()
}

func (store *PostgresStore) PairExists(ctx context.Context, pair string) (bool, error) {
	var exists bool
	err := store.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM trading_pairs WHERE symbol=$1 AND status <> 'disabled')`, pair).Scan(&exists)
	return exists, err
}

func (store *PostgresStore) TakerSide(ctx context.Context, orderID string) (string, error) {
	var side string
	err := store.pool.QueryRow(ctx, `SELECT side FROM orders WHERE id=$1`, orderID).Scan(&side)
	return side, err
}

func (store *PostgresStore) RecentTrades(ctx context.Context, pair string, limit int) ([]Trade, error) {
	rows, err := store.pool.Query(ctx, `SELECT sequence_id::text,traded_at,price_atomic::text,quantity_atomic::text,quote_amount_atomic::text,taker_side
		FROM market_trade_events WHERE pair=$1 ORDER BY traded_at DESC,sequence_id DESC LIMIT $2`, pair, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	trades := make([]Trade, 0)
	for rows.Next() {
		var trade Trade
		var sequence string
		trade.Pair = pair
		if err = rows.Scan(&sequence, &trade.Timestamp, &trade.PriceAtomic, &trade.QuantityAtomic, &trade.QuoteAtomic, &trade.Side); err != nil {
			return nil, err
		}
		value, ok := new(big.Int).SetString(sequence, 10)
		if !ok || !value.IsUint64() {
			return nil, fmt.Errorf("invalid stored trade sequence")
		}
		trade.SequenceID = value.Uint64()
		trades = append(trades, trade)
	}
	return trades, rows.Err()
}

func (store *PostgresStore) Ticker(ctx context.Context, pair string, now time.Time) (Ticker, error) {
	var ticker Ticker
	ticker.Pair = pair
	var first string
	var sequence string
	err := store.pool.QueryRow(ctx, `SELECT
		COALESCE((array_agg(price_atomic::text ORDER BY traded_at DESC,sequence_id DESC))[1],'0'),
		COALESCE(MAX(price_atomic)::text,'0'),COALESCE(MIN(price_atomic)::text,'0'),
		COALESCE(SUM(quantity_atomic)::text,'0'),COALESCE(SUM(quote_amount_atomic)::text,'0'),
		COALESCE((array_agg(price_atomic::text ORDER BY traded_at,sequence_id))[1],'0'),COALESCE(MAX(sequence_id)::text,'0')
		FROM market_trade_events WHERE pair=$1 AND traded_at >= $2`, pair, now.UTC().Add(-24*time.Hour)).Scan(
		&ticker.LastPrice, &ticker.High24H, &ticker.Low24H, &ticker.Volume24H, &ticker.QuoteVolume24H, &first, &sequence)
	if err != nil {
		return Ticker{}, err
	}
	sequenceValue, ok := new(big.Int).SetString(sequence, 10)
	if !ok || !sequenceValue.IsUint64() {
		return Ticker{}, fmt.Errorf("invalid ticker sequence")
	}
	ticker.SequenceID = sequenceValue.Uint64()
	last, lastOK := new(big.Int).SetString(ticker.LastPrice, 10)
	opening, openingOK := new(big.Int).SetString(first, 10)
	if lastOK && openingOK {
		ticker.Change24H = new(big.Int).Sub(last, opening).String()
	} else {
		ticker.Change24H = "0"
	}
	return ticker, nil
}

func (store *PostgresStore) MinuteCandles(ctx context.Context, pair string, before time.Time, limit int) ([]Candle, error) {
	rows, err := store.pool.Query(ctx, `SELECT open_time,open_atomic::text,high_atomic::text,low_atomic::text,close_atomic::text,
		volume_atomic::text,quote_volume_atomic::text,trade_count,last_sequence_id::text
		FROM market_candles_1m WHERE pair=$1 AND open_time < $2 ORDER BY open_time DESC LIMIT $3`, pair, before.UTC(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	reversed := make([]Candle, 0)
	for rows.Next() {
		var candle Candle
		var sequence string
		candle.Pair, candle.Interval = pair, "1m"
		if err = rows.Scan(&candle.OpenTime, &candle.Open, &candle.High, &candle.Low, &candle.Close, &candle.Volume, &candle.QuoteVolume, &candle.TradeCount, &sequence); err != nil {
			return nil, err
		}
		candle.CloseTime = candle.OpenTime.Add(time.Minute)
		value, ok := new(big.Int).SetString(sequence, 10)
		if !ok || !value.IsUint64() {
			return nil, fmt.Errorf("invalid stored candle sequence")
		}
		candle.SequenceID = value.Uint64()
		reversed = append(reversed, candle)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	return reversed, nil
}

func (store *PostgresStore) ApplyTrade(ctx context.Context, trade Trade) (Candle, bool, error) {
	if err := validateTrade(trade); err != nil {
		return Candle{}, false, err
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return Candle{}, false, err
	}
	defer tx.Rollback(ctx)

	accepted, duplicate, err := claimSequence(ctx, tx, trade.Pair, "trades", trade.SequenceID, false)
	if err != nil {
		return Candle{}, false, err
	}
	if duplicate {
		var storedHash []byte
		if err = tx.QueryRow(ctx, `SELECT payload_hash FROM market_trade_events WHERE pair=$1 AND sequence_id=$2`, trade.Pair, trade.SequenceID).Scan(&storedHash); errors.Is(err, pgx.ErrNoRows) {
			return Candle{}, true, nil
		} else if err != nil {
			return Candle{}, false, err
		}
		if len(storedHash) != len(trade.PayloadHash) || string(storedHash) != string(trade.PayloadHash[:]) {
			return Candle{}, false, ErrMarketSequenceConflict
		}
		return Candle{}, true, nil
	}
	if !accepted {
		return Candle{}, false, nil
	}
	if _, err = tx.Exec(ctx, `INSERT INTO market_trade_events(pair,sequence_id,traded_at,price_atomic,quantity_atomic,quote_amount_atomic,taker_side,payload_hash)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, trade.Pair, trade.SequenceID, trade.Timestamp, trade.PriceAtomic, trade.QuantityAtomic, trade.QuoteAtomic, trade.Side, trade.PayloadHash[:]); err != nil {
		return Candle{}, false, err
	}
	minute, _ := NewMinuteCandle(trade)
	var candle Candle
	candle.Pair, candle.Interval = trade.Pair, "1m"
	var sequence string
	err = tx.QueryRow(ctx, `INSERT INTO market_candles_1m(pair,open_time,open_atomic,high_atomic,low_atomic,close_atomic,volume_atomic,quote_volume_atomic,trade_count,last_sequence_id)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,1,$9)
		ON CONFLICT(pair,open_time) DO UPDATE SET
		high_atomic=GREATEST(market_candles_1m.high_atomic,EXCLUDED.high_atomic),
		low_atomic=LEAST(market_candles_1m.low_atomic,EXCLUDED.low_atomic),close_atomic=EXCLUDED.close_atomic,
		volume_atomic=market_candles_1m.volume_atomic+EXCLUDED.volume_atomic,
		quote_volume_atomic=market_candles_1m.quote_volume_atomic+EXCLUDED.quote_volume_atomic,
		trade_count=market_candles_1m.trade_count+1,last_sequence_id=EXCLUDED.last_sequence_id,updated_at=now()
		RETURNING open_time,open_atomic::text,high_atomic::text,low_atomic::text,close_atomic::text,volume_atomic::text,quote_volume_atomic::text,trade_count,last_sequence_id::text`,
		trade.Pair, minute.OpenTime, trade.PriceAtomic, trade.PriceAtomic, trade.PriceAtomic, trade.PriceAtomic, trade.QuantityAtomic, trade.QuoteAtomic, trade.SequenceID).Scan(
		&candle.OpenTime, &candle.Open, &candle.High, &candle.Low, &candle.Close, &candle.Volume, &candle.QuoteVolume, &candle.TradeCount, &sequence)
	if err != nil {
		return Candle{}, false, err
	}
	candle.CloseTime = candle.OpenTime.Add(time.Minute)
	sequenceValue, _ := new(big.Int).SetString(sequence, 10)
	candle.SequenceID = sequenceValue.Uint64()
	if err = tx.Commit(ctx); err != nil {
		return Candle{}, false, err
	}
	return candle, false, nil
}

func (store *PostgresStore) ClaimOrderBookSequence(ctx context.Context, pair string, sequence uint64) (bool, bool, error) {
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return false, false, err
	}
	defer tx.Rollback(ctx)
	accepted, duplicate, err := claimSequence(ctx, tx, pair, "orderbook", sequence, true)
	if err != nil || !accepted {
		return accepted, duplicate, err
	}
	return true, false, tx.Commit(ctx)
}

func claimSequence(ctx context.Context, tx pgx.Tx, pair, stream string, sequence uint64, allowInitialAny bool) (bool, bool, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1 || ':' || $2,0))`, pair, stream); err != nil {
		return false, false, err
	}
	var last string
	err := tx.QueryRow(ctx, `SELECT last_sequence_id::text FROM market_stream_sequences WHERE pair=$1 AND stream=$2 FOR UPDATE`, pair, stream).Scan(&last)
	if errors.Is(err, pgx.ErrNoRows) {
		if allowInitialAny || sequence == 1 {
			_, err = tx.Exec(ctx, `INSERT INTO market_stream_sequences(pair,stream,last_sequence_id) VALUES($1,$2,$3)`, pair, stream, sequence)
			return err == nil, false, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO market_stream_sequences(pair,stream,last_sequence_id,mode) VALUES($1,$2,0,'replay_required')`, pair, stream); err != nil {
			return false, false, err
		}
		if err = tx.Commit(ctx); err != nil {
			return false, false, err
		}
		return false, false, &SequenceGapError{Pair: pair, Stream: stream, Expected: 1, Received: sequence}
	}
	if err != nil {
		return false, false, err
	}
	lastValue, ok := new(big.Int).SetString(last, 10)
	if !ok || !lastValue.IsUint64() {
		return false, false, fmt.Errorf("invalid market sequence")
	}
	lastSequence := lastValue.Uint64()
	if sequence <= lastSequence {
		return false, true, nil
	}
	if sequence != lastSequence+1 {
		_, updateErr := tx.Exec(ctx, `UPDATE market_stream_sequences SET mode='replay_required',updated_at=now() WHERE pair=$1 AND stream=$2`, pair, stream)
		if updateErr != nil {
			return false, false, updateErr
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return false, false, commitErr
		}
		return false, false, &SequenceGapError{Pair: pair, Stream: stream, Expected: lastSequence + 1, Received: sequence}
	}
	_, err = tx.Exec(ctx, `UPDATE market_stream_sequences SET last_sequence_id=$3,mode='active',updated_at=now() WHERE pair=$1 AND stream=$2`, pair, stream, sequence)
	return err == nil, false, err
}

type SequenceGapError struct {
	Pair, Stream       string
	Expected, Received uint64
}

func (err *SequenceGapError) Error() string {
	return fmt.Sprintf("%v for %s %s: expected %d, received %d", ErrMarketSequenceGap, err.Pair, err.Stream, err.Expected, err.Received)
}
func (err *SequenceGapError) Unwrap() error { return ErrMarketSequenceGap }
