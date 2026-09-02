package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/limiance/backend/internal/ledger"
	"github.com/limiance/backend/internal/trading"
	"github.com/limiance/backend/internal/trading/protocol"
)

type settlementOrder struct {
	id, userID, accountID, pair, side, status string
	quantity, holdAssetID                     string
}

type settlementPosting struct {
	accountID, assetID, bucket, direction, amount string
}

func (store *Store) SettleTrade(ctx context.Context, event protocol.TradeEvent, payloadHash [32]byte) (trading.Trade, error) {
	for attempt := 0; attempt < 3; attempt++ {
		trade, err := store.settleTradeOnce(ctx, event, payloadHash)
		var postgresError *pgconn.PgError
		if !errors.As(err, &postgresError) || postgresError.Code != "40001" {
			return trade, err
		}
	}
	return trading.Trade{}, fmt.Errorf("trade settlement serialization retries exhausted")
}

func (store *Store) settleTradeOnce(ctx context.Context, event protocol.TradeEvent, payloadHash [32]byte) (trading.Trade, error) {
	if store.pool == nil || event.Validate() != nil {
		return trading.Trade{}, trading.ErrSettlementInvalid
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return trading.Trade{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err = tx.Exec(ctx, `INSERT INTO engine_symbol_sequences(pair) VALUES($1) ON CONFLICT (pair) DO NOTHING`, event.Pair); err != nil {
		return trading.Trade{}, err
	}
	var lastRaw, replayThroughRaw, mode string
	var haltedByGap bool
	if err = tx.QueryRow(ctx, `SELECT last_sequence_id::text,replay_through_sequence_id::text,mode,halted_by_sequence_gap FROM engine_symbol_sequences WHERE pair=$1 FOR UPDATE`, event.Pair).Scan(&lastRaw, &replayThroughRaw, &mode, &haltedByGap); err != nil {
		return trading.Trade{}, err
	}
	last, err := parseSequence(lastRaw)
	if err != nil {
		return trading.Trade{}, err
	}
	replayThrough, err := parseSequence(replayThroughRaw)
	if err != nil {
		return trading.Trade{}, err
	}

	var storedHash []byte
	var processedAt *time.Time
	err = tx.QueryRow(ctx, `SELECT payload_hash,processed_at FROM engine_trade_inbox WHERE pair=$1 AND sequence_id=$2::numeric`, event.Pair, strconv.FormatUint(event.SequenceID, 10)).Scan(&storedHash, &processedAt)
	if err == nil {
		if !bytes.Equal(storedHash, payloadHash[:]) {
			return trading.Trade{}, trading.ErrSequenceConflict
		}
		if processedAt != nil {
			trade, findErr := findTradeBySequence(ctx, tx, event.Pair, event.SequenceID)
			if findErr != nil {
				return trading.Trade{}, findErr
			}
			trade.Duplicate = true
			return trade, tx.Commit(ctx)
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return trading.Trade{}, err
	}

	expected := last + 1
	if event.SequenceID != expected {
		if event.SequenceID < expected {
			return trading.Trade{}, trading.ErrSequenceConflict
		}
		if event.SequenceID > replayThrough {
			replayThrough = event.SequenceID
		}
		halted, haltErr := tx.Exec(ctx, `UPDATE trading_pairs SET status='halted',updated_at=now() WHERE symbol=$1 AND status='active'`, event.Pair)
		if haltErr != nil {
			return trading.Trade{}, haltErr
		}
		haltedByGap = haltedByGap || halted.RowsAffected() > 0
		if _, err = tx.Exec(ctx, `UPDATE engine_symbol_sequences SET mode='replay_required',replay_through_sequence_id=$2::numeric,halted_by_sequence_gap=$3,updated_at=now() WHERE pair=$1`, event.Pair, strconv.FormatUint(replayThrough, 10), haltedByGap); err != nil {
			return trading.Trade{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return trading.Trade{}, err
		}
		return trading.Trade{}, &trading.SequenceGapError{Pair: event.Pair, Expected: expected, Received: event.SequenceID}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO engine_trade_inbox(pair,sequence_id,payload_hash) VALUES($1,$2::numeric,$3)`, event.Pair, strconv.FormatUint(event.SequenceID, 10), payloadHash[:]); err != nil {
		return trading.Trade{}, err
	}

	var baseAssetID, quoteAssetID string
	var rules trading.PairRules
	if err = tx.QueryRow(ctx, `SELECT p.base_asset_id::text,p.quote_asset_id::text,p.price_scale,p.quantity_scale,q.decimals
		FROM trading_pairs p JOIN assets q ON q.id=p.quote_asset_id WHERE p.symbol=$1`, event.Pair).
		Scan(&baseAssetID, &quoteAssetID, &rules.PriceScale, &rules.QuantityScale, &rules.QuoteScale); err != nil {
		return trading.Trade{}, err
	}
	orders, err := lockSettlementOrders(ctx, tx, event.MakerOrderID, event.TakerOrderID)
	if err != nil {
		return trading.Trade{}, err
	}
	maker, makerOK := orders[event.MakerOrderID]
	taker, takerOK := orders[event.TakerOrderID]
	if !makerOK || !takerOK || maker.userID != event.MakerUserID || taker.userID != event.TakerUserID || maker.pair != event.Pair || taker.pair != event.Pair || maker.side == taker.side {
		return trading.Trade{}, trading.ErrSettlementInvalid
	}
	if !settleableStatus(maker.status) || !settleableStatus(taker.status) {
		return trading.Trade{}, trading.ErrSettlementInvalid
	}

	notional, err := trading.QuoteAmountForScales(event.Price, event.Quantity, rules)
	if err != nil {
		return trading.Trade{}, err
	}
	makerFee, err := trading.FeeAmount(notional, event.MakerFeeBPS)
	if err != nil {
		return trading.Trade{}, err
	}
	takerFee, err := trading.FeeAmount(notional, event.TakerFeeBPS)
	if err != nil {
		return trading.Trade{}, err
	}
	feeAccountID, err := ensureTradingFeeAccount(ctx, tx, quoteAssetID)
	if err != nil {
		return trading.Trade{}, err
	}

	buyer, seller := maker, taker
	buyerFee, sellerFee := makerFee, takerFee
	if maker.side == "SELL" {
		buyer, seller = taker, maker
		buyerFee, sellerFee = takerFee, makerFee
	}
	if buyer.holdAssetID != quoteAssetID || seller.holdAssetID != baseAssetID {
		return trading.Trade{}, trading.ErrSettlementInvalid
	}
	lockTargets := []struct{ accountID, assetID string }{
		{buyer.accountID, quoteAssetID}, {buyer.accountID, baseAssetID}, {seller.accountID, baseAssetID},
		{seller.accountID, quoteAssetID}, {feeAccountID, quoteAssetID},
	}
	sort.Slice(lockTargets, func(i, j int) bool {
		return lockTargets[i].accountID+":"+lockTargets[i].assetID < lockTargets[j].accountID+":"+lockTargets[j].assetID
	})
	for _, target := range lockTargets {
		if err = ledger.LockAccountAsset(ctx, tx, target.accountID, target.assetID); err != nil {
			return trading.Trade{}, err
		}
	}
	buyerHeld, err := bucketBalance(ctx, tx, buyer.accountID, quoteAssetID, "held")
	if err != nil {
		return trading.Trade{}, err
	}
	buyerRequired := new(big.Int).Set(notional)
	if buyerFee.Sign() > 0 {
		buyerRequired.Add(buyerRequired, buyerFee)
	}
	sellerHeld, err := bucketBalance(ctx, tx, seller.accountID, baseAssetID, "held")
	if err != nil {
		return trading.Trade{}, err
	}
	quantity := new(big.Int).SetUint64(event.Quantity)
	if buyerHeld.Cmp(buyerRequired) < 0 || sellerHeld.Cmp(quantity) < 0 {
		return trading.Trade{}, trading.ErrInsufficientBalance
	}
	if err = ensureOrderSettlementCapacity(ctx, tx, maker, event.Quantity); err != nil {
		return trading.Trade{}, err
	}
	if err = ensureOrderSettlementCapacity(ctx, tx, taker, event.Quantity); err != nil {
		return trading.Trade{}, err
	}

	var tradeID string
	if err = tx.QueryRow(ctx, `INSERT INTO trades(pair,maker_user_id,taker_user_id,quantity,price,traded_at,sequence_id,engine_timestamp_ns,maker_order_id,taker_order_id,price_atomic,quantity_atomic,quote_amount_atomic)
		VALUES($1,$4,$5,$9::numeric/100000000,$8::numeric/100000000,to_timestamp($3::numeric/1000000000),$2::numeric,$3::numeric,$6::uuid,$7::uuid,$8::numeric,$9::numeric,$10::numeric) RETURNING id::text`,
		event.Pair, strconv.FormatUint(event.SequenceID, 10), strconv.FormatUint(event.TimestampNS, 10), event.MakerUserID, event.TakerUserID,
		event.MakerOrderID, event.TakerOrderID, strconv.FormatUint(event.Price, 10), strconv.FormatUint(event.Quantity, 10), notional.String()).Scan(&tradeID); err != nil {
		return trading.Trade{}, err
	}
	makerHoldConsumed := holdConsumed(maker.side, notional, quantity, makerFee)
	takerHoldConsumed := holdConsumed(taker.side, notional, quantity, takerFee)
	if err = insertTradeParticipant(ctx, tx, tradeID, maker, "maker", event.MakerFeeBPS, makerFee, makerHoldConsumed, quoteAssetID); err != nil {
		return trading.Trade{}, err
	}
	if err = insertTradeParticipant(ctx, tx, tradeID, taker, "taker", event.TakerFeeBPS, takerFee, takerHoldConsumed, quoteAssetID); err != nil {
		return trading.Trade{}, err
	}

	var journalID string
	if err = tx.QueryRow(ctx, `INSERT INTO journals(idempotency_key,reference_type,reference_id) VALUES($1,'trade_settlement',$2) RETURNING id::text`, "trade-settlement:"+event.Pair+":"+strconv.FormatUint(event.SequenceID, 10), tradeID).Scan(&journalID); err != nil {
		return trading.Trade{}, err
	}
	postings := []settlementPosting{
		{seller.accountID, baseAssetID, "held", "debit", quantity.String()},
		{buyer.accountID, baseAssetID, "available", "credit", quantity.String()},
		{buyer.accountID, quoteAssetID, "held", "debit", notional.String()},
		{seller.accountID, quoteAssetID, "available", "credit", notional.String()},
	}
	postings = appendFeePostings(postings, buyer.accountID, feeAccountID, quoteAssetID, "held", buyerFee)
	postings = appendFeePostings(postings, seller.accountID, feeAccountID, quoteAssetID, "available", sellerFee)
	for _, posting := range postings {
		if _, err = tx.Exec(ctx, `INSERT INTO postings(journal_id,account_id,asset_id,bucket,direction,amount_atomic) VALUES($1,$2,$3,$4,$5,$6::numeric)`, journalID, posting.accountID, posting.assetID, posting.bucket, posting.direction, posting.amount); err != nil {
			return trading.Trade{}, err
		}
	}

	makerFilled, err := updateOrderFromSettledTrades(ctx, tx, maker, event.SequenceID)
	if err != nil {
		return trading.Trade{}, err
	}
	takerFilled, err := updateOrderFromSettledTrades(ctx, tx, taker, event.SequenceID)
	if err != nil {
		return trading.Trade{}, err
	}
	if makerFilled {
		if err = releaseOrderHold(ctx, tx, maker.id); err != nil {
			return trading.Trade{}, err
		}
	}
	if takerFilled {
		if err = releaseOrderHold(ctx, tx, taker.id); err != nil {
			return trading.Trade{}, err
		}
	}

	nextMode := mode
	if mode == "replay_required" && event.SequenceID >= replayThrough {
		nextMode = "active"
		replayThrough = 0
		if haltedByGap {
			if _, err = tx.Exec(ctx, `UPDATE trading_pairs SET status='active',updated_at=now() WHERE symbol=$1 AND status='halted'`, event.Pair); err != nil {
				return trading.Trade{}, err
			}
			haltedByGap = false
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE engine_trade_inbox SET processed_at=now() WHERE pair=$1 AND sequence_id=$2::numeric`, event.Pair, strconv.FormatUint(event.SequenceID, 10)); err != nil {
		return trading.Trade{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE engine_symbol_sequences SET last_sequence_id=$2::numeric,mode=$3,replay_through_sequence_id=$4::numeric,halted_by_sequence_gap=$5,updated_at=now() WHERE pair=$1`, event.Pair, strconv.FormatUint(event.SequenceID, 10), nextMode, strconv.FormatUint(replayThrough, 10), haltedByGap); err != nil {
		return trading.Trade{}, err
	}
	outboxPayload, _ := json.Marshal(map[string]any{"trade_id": tradeID, "pair": event.Pair, "sequence_id": event.SequenceID, "maker_order_id": event.MakerOrderID, "taker_order_id": event.TakerOrderID})
	if _, err = tx.Exec(ctx, `INSERT INTO outbox_events(event_type,aggregate_type,aggregate_id,payload) VALUES('trade.settled','trade',$1,$2::jsonb)`, tradeID, outboxPayload); err != nil {
		return trading.Trade{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return trading.Trade{}, err
	}
	return trading.Trade{ID: tradeID, Pair: event.Pair, SequenceID: event.SequenceID, MakerOrderID: event.MakerOrderID, TakerOrderID: event.TakerOrderID,
		PriceAtomic: strconv.FormatUint(event.Price, 10), QuantityAtomic: strconv.FormatUint(event.Quantity, 10), QuoteAtomic: notional.String(),
		MakerFeeAtomic: makerFee.String(), TakerFeeAtomic: takerFee.String(), SettlementState: nextMode}, nil
}

func lockSettlementOrders(ctx context.Context, tx pgx.Tx, makerOrderID, takerOrderID string) (map[string]settlementOrder, error) {
	rows, err := tx.Query(ctx, `SELECT id::text,user_id::text,account_id::text,pair,side,status,quantity::text,COALESCE(hold_asset_id::text,'')
		FROM orders WHERE id=ANY($1::uuid[]) ORDER BY id FOR UPDATE`, []string{makerOrderID, takerOrderID})
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	orders := make(map[string]settlementOrder, 2)
	for rows.Next() {
		var order settlementOrder
		if err = rows.Scan(&order.id, &order.userID, &order.accountID, &order.pair, &order.side, &order.status, &order.quantity, &order.holdAssetID); err != nil {
			return nil, err
		}
		orders[order.id] = order
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(orders) != 2 {
		return nil, trading.ErrSettlementInvalid
	}
	return orders, nil
}

func settleableStatus(status string) bool {
	return status == "PENDING" || status == "PENDING_CANCEL" || status == "OPEN" || status == "PARTIALLY_FILLED" || status == "FILLED"
}

func ensureTradingFeeAccount(ctx context.Context, tx pgx.Tx, assetID string) (string, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "trading-fee-account:"+assetID); err != nil {
		return "", err
	}
	var accountID string
	err := tx.QueryRow(ctx, `SELECT account_id::text FROM trading_fee_accounts WHERE asset_id=$1`, assetID).Scan(&accountID)
	if err == nil {
		return accountID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if err = tx.QueryRow(ctx, `INSERT INTO accounts(kind,name) VALUES('system',$1) RETURNING id::text`, "trading-fees-"+assetID).Scan(&accountID); err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `INSERT INTO trading_fee_accounts(asset_id,account_id) VALUES($1,$2)`, assetID, accountID)
	return accountID, err
}

func ensureOrderSettlementCapacity(ctx context.Context, tx pgx.Tx, order settlementOrder, fillQuantity uint64) error {
	var settledRaw string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(SUM(quantity_atomic),0)::text FROM trades WHERE maker_order_id=$1 OR taker_order_id=$1`, order.id).Scan(&settledRaw); err != nil {
		return err
	}
	settled, ok := new(big.Int).SetString(settledRaw, 10)
	quantity, quantityOK := new(big.Int).SetString(order.quantity, 10)
	if !ok || !quantityOK || new(big.Int).Add(settled, new(big.Int).SetUint64(fillQuantity)).Cmp(quantity) > 0 {
		return trading.ErrSettlementInvalid
	}
	return nil
}

func insertTradeParticipant(ctx context.Context, tx pgx.Tx, tradeID string, order settlementOrder, role string, feeBPS int16, fee, holdAmount *big.Int, quoteAssetID string) error {
	if _, err := tx.Exec(ctx, `INSERT INTO trade_participants(trade_id,order_id,user_id,account_id,role,side,fee_bps,fee_amount_atomic,hold_consumed_atomic) VALUES($1,$2,$3,$4,$5,$6,$7,$8::numeric,$9::numeric)`, tradeID, order.id, order.userID, order.accountID, role, order.side, feeBPS, fee.String(), holdAmount.String()); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO trade_fee_accruals(trade_id,order_id,user_id,asset_id,fee_bps,amount_atomic) VALUES($1,$2,$3,$4,$5,$6::numeric)`, tradeID, order.id, order.userID, quoteAssetID, feeBPS, fee.String())
	return err
}

func holdConsumed(side string, notional, quantity, fee *big.Int) *big.Int {
	if side == "SELL" {
		return new(big.Int).Set(quantity)
	}
	consumed := new(big.Int).Set(notional)
	if fee.Sign() > 0 {
		consumed.Add(consumed, fee)
	}
	return consumed
}

func appendFeePostings(postings []settlementPosting, payerAccountID, feeAccountID, assetID, payerBucket string, fee *big.Int) []settlementPosting {
	if fee.Sign() > 0 {
		return append(postings,
			settlementPosting{payerAccountID, assetID, payerBucket, "debit", fee.String()},
			settlementPosting{feeAccountID, assetID, "available", "credit", fee.String()},
		)
	}
	if fee.Sign() < 0 {
		amount := new(big.Int).Abs(new(big.Int).Set(fee)).String()
		return append(postings,
			settlementPosting{feeAccountID, assetID, "available", "debit", amount},
			settlementPosting{payerAccountID, assetID, "available", "credit", amount},
		)
	}
	return postings
}

func updateOrderFromSettledTrades(ctx context.Context, tx pgx.Tx, order settlementOrder, sequenceID uint64) (bool, error) {
	var quantitySumRaw, priceQuantitySumRaw string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(SUM(quantity_atomic),0)::text,COALESCE(SUM(price_atomic*quantity_atomic),0)::text
		FROM trades WHERE maker_order_id=$1 OR taker_order_id=$1`, order.id).Scan(&quantitySumRaw, &priceQuantitySumRaw); err != nil {
		return false, err
	}
	filled, filledOK := new(big.Int).SetString(quantitySumRaw, 10)
	priceQuantity, priceOK := new(big.Int).SetString(priceQuantitySumRaw, 10)
	quantity, quantityOK := new(big.Int).SetString(order.quantity, 10)
	if !filledOK || !priceOK || !quantityOK || filled.Sign() <= 0 || filled.Cmp(quantity) > 0 {
		return false, trading.ErrSettlementInvalid
	}
	remaining := new(big.Int).Sub(quantity, filled)
	average := new(big.Int).Quo(priceQuantity, filled)
	status := "PARTIALLY_FILLED"
	if remaining.Sign() == 0 {
		status = "FILLED"
	}
	_, err := tx.Exec(ctx, `UPDATE orders SET filled_quantity=$2::numeric,remaining_quantity=$3::numeric,avg_price=$4::numeric,status=$5,engine_sequence_id=$6::numeric,updated_at=now() WHERE id=$1`,
		order.id, filled.String(), remaining.String(), average.String(), status, strconv.FormatUint(sequenceID, 10))
	return status == "FILLED", err
}

func bucketBalance(ctx context.Context, tx pgx.Tx, accountID, assetID, bucket string) (*big.Int, error) {
	var raw string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(SUM(CASE direction WHEN 'credit' THEN amount_atomic ELSE -amount_atomic END),0)::text
		FROM postings WHERE account_id=$1 AND asset_id=$2 AND bucket=$3`, accountID, assetID, bucket).Scan(&raw); err != nil {
		return nil, err
	}
	value, ok := new(big.Int).SetString(raw, 10)
	if !ok {
		return nil, fmt.Errorf("invalid bucket balance %q", raw)
	}
	return value, nil
}

func findTradeBySequence(ctx context.Context, tx pgx.Tx, pair string, sequenceID uint64) (trading.Trade, error) {
	var trade trading.Trade
	var sequenceRaw string
	err := tx.QueryRow(ctx, `SELECT t.id::text,t.pair,t.sequence_id::text,t.maker_order_id::text,t.taker_order_id::text,t.price_atomic::text,t.quantity_atomic::text,t.quote_amount_atomic::text,
		maker.fee_amount_atomic::text,taker.fee_amount_atomic::text
		FROM trades t
		JOIN trade_participants maker ON maker.trade_id=t.id AND maker.role='maker'
		JOIN trade_participants taker ON taker.trade_id=t.id AND taker.role='taker'
		WHERE t.pair=$1 AND t.sequence_id=$2::numeric`, pair, strconv.FormatUint(sequenceID, 10)).
		Scan(&trade.ID, &trade.Pair, &sequenceRaw, &trade.MakerOrderID, &trade.TakerOrderID, &trade.PriceAtomic, &trade.QuantityAtomic, &trade.QuoteAtomic, &trade.MakerFeeAtomic, &trade.TakerFeeAtomic)
	if err != nil {
		return trading.Trade{}, err
	}
	trade.SequenceID, err = parseSequence(sequenceRaw)
	return trade, err
}

func parseSequence(raw string) (uint64, error) {
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid engine sequence %q", raw)
	}
	return value, nil
}
