package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/limiance/backend/internal/ledger"
	"github.com/limiance/backend/internal/trading"
	"github.com/limiance/backend/internal/trading/protocol"
)

type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

func (store *Store) PairRules(ctx context.Context, pair string) (trading.PairRules, error) {
	var rules trading.PairRules
	err := store.pool.QueryRow(ctx, `SELECT p.price_scale,p.quantity_scale,q.decimals
		FROM trading_pairs p JOIN assets q ON q.id=p.quote_asset_id WHERE p.symbol=$1`, pair).
		Scan(&rules.PriceScale, &rules.QuantityScale, &rules.QuoteScale)
	if errors.Is(err, pgx.ErrNoRows) {
		return trading.PairRules{}, trading.ErrPairUnavailable
	}
	return rules, err
}

func (store *Store) CreateOrder(ctx context.Context, input trading.CreateOrderCommand) (trading.Order, error) {
	if store.pool == nil || input.OrderID == "" || input.UserID == "" || input.AccountID == "" || input.IdempotencyKey == "" || len(input.EnginePayload) == 0 {
		return trading.Order{}, fmt.Errorf("%w: incomplete persistence command", trading.ErrInvalidOrder)
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return trading.Order{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var userStatus string
	if err := tx.QueryRow(ctx, `SELECT status FROM users WHERE id=$1 FOR UPDATE`, input.UserID).Scan(&userStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return trading.Order{}, trading.ErrTradingAccountRequired
		}
		return trading.Order{}, err
	}
	if userStatus != "active" {
		return trading.Order{}, trading.ErrTradingAccountRequired
	}

	var accountKind, accountStatus string
	if err := tx.QueryRow(ctx, `SELECT kind::text,status FROM accounts WHERE id=$1 AND user_id=$2 FOR UPDATE`, input.AccountID, input.UserID).Scan(&accountKind, &accountStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return trading.Order{}, trading.ErrTradingAccountRequired
		}
		return trading.Order{}, err
	}
	if accountStatus != "active" || accountKind != "uta" && accountKind != "subaccount" {
		return trading.Order{}, trading.ErrTradingAccountRequired
	}

	existing, storedHash, found, err := findByIdempotencyKey(ctx, tx, input.UserID, input.IdempotencyKey)
	if err != nil {
		return trading.Order{}, err
	}
	if found {
		if !bytes.Equal(storedHash, input.RequestHash[:]) {
			return trading.Order{}, trading.ErrIdempotencyConflict
		}
		existing.Duplicate = true
		if err := tx.Commit(ctx); err != nil {
			return trading.Order{}, err
		}
		return existing, nil
	}

	var baseAssetID, quoteAssetID, minimum, maximum, priceTick, quantityStep string
	err = tx.QueryRow(ctx, `
		SELECT pair.base_asset_id::text,pair.quote_asset_id::text,
		       pair.min_quantity_atomic::text,pair.max_quantity_atomic::text,
		       pair.price_tick_atomic::text,pair.quantity_step_atomic::text
		FROM trading_pairs pair
		JOIN assets base ON base.id=pair.base_asset_id AND base.status='enabled'
		JOIN assets quote ON quote.id=pair.quote_asset_id AND quote.status='enabled'
		WHERE pair.symbol=$1 AND pair.status='active'`, input.Pair).
		Scan(&baseAssetID, &quoteAssetID, &minimum, &maximum, &priceTick, &quantityStep)
	if errors.Is(err, pgx.ErrNoRows) {
		return trading.Order{}, trading.ErrPairUnavailable
	}
	if err != nil {
		return trading.Order{}, err
	}
	if !withinPairLimits(input.Quantity, input.Price, minimum, maximum, priceTick, quantityStep) {
		return trading.Order{}, fmt.Errorf("%w: order violates pair quantity or tick limits", trading.ErrInvalidOrder)
	}
	if input.Conditional && !withinPairLimits(input.Quantity, input.TriggerPrice, minimum, maximum, priceTick, quantityStep) {
		return trading.Order{}, fmt.Errorf("%w: trigger price violates pair tick limits", trading.ErrInvalidOrder)
	}

	holdAssetID := baseAssetID
	if input.Side == "BUY" {
		holdAssetID = quoteAssetID
	}
	if err := ledger.LockAccountAsset(ctx, tx, input.AccountID, holdAssetID); err != nil {
		return trading.Order{}, err
	}
	available, err := availableBalance(ctx, tx, input.AccountID, holdAssetID)
	if err != nil {
		return trading.Order{}, err
	}
	holdAmount := input.HoldAmount
	if input.HoldAllAvailable {
		holdAmount = available.String()
	}
	hold, ok := new(big.Int).SetString(holdAmount, 10)
	if !ok || hold.Sign() <= 0 {
		return trading.Order{}, trading.ErrInsufficientBalance
	}
	if available.Cmp(hold) < 0 {
		return trading.Order{}, trading.ErrInsufficientBalance
	}

	var order trading.Order
	initialStatus := "PENDING"
	commandStatus := "pending"
	if input.Conditional {
		initialStatus = "CONDITIONAL"
		commandStatus = "blocked"
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO orders (
			id,user_id,account_id,pair,side,type,price,quantity,remaining_quantity,time_in_force,
			post_only,reduce_only,fee_tier,idempotency_key,request_hash,status,trigger_price,trigger_direction
		) VALUES (
			$1,$2,$3,$4,$5,$6,NULLIF($7,'0')::numeric,$8::numeric,$8::numeric,$9,$10,$11,$12,$13,$14,$15,
			NULLIF($16,0)::numeric,NULLIF($17,'')
		)
		RETURNING id::text,user_id::text,account_id::text,pair,side,type,COALESCE(price,0)::text,COALESCE(trigger_price::text,''),
		          quantity::text,filled_quantity::text,remaining_quantity::text,avg_price::text,
		          time_in_force,status,post_only,reduce_only,fee_tier,created_at,updated_at`,
		input.OrderID, input.UserID, input.AccountID, input.Pair, input.Side, input.Type,
		strconv.FormatUint(input.Price, 10), strconv.FormatUint(input.Quantity, 10), input.TimeInForce, input.PostOnly, input.ReduceOnly,
		input.FeeTier, input.IdempotencyKey, input.RequestHash[:], initialStatus, strconv.FormatUint(input.TriggerPrice, 10), input.TriggerDirection).Scan(orderScanTargets(&order)...)
	if err != nil {
		return trading.Order{}, err
	}

	var journalID string
	if err := tx.QueryRow(ctx, `INSERT INTO journals (idempotency_key,reference_type,reference_id) VALUES ($1,'order_hold',$2) RETURNING id::text`, "order-hold:"+input.OrderID, input.OrderID).Scan(&journalID); err != nil {
		return trading.Order{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO postings (journal_id,account_id,asset_id,bucket,direction,amount_atomic) VALUES
		($1,$2,$3,'available','debit',$4::numeric),
		($1,$2,$3,'held','credit',$4::numeric)`, journalID, input.AccountID, holdAssetID, holdAmount); err != nil {
		return trading.Order{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE orders SET hold_journal_id=$2,hold_asset_id=$3,hold_amount_atomic=$4::numeric WHERE id=$1`, input.OrderID, journalID, holdAssetID, holdAmount); err != nil {
		return trading.Order{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO engine_commands (order_id,payload,status) VALUES ($1,$2,$3)`, input.OrderID, input.EnginePayload, commandStatus); err != nil {
		return trading.Order{}, err
	}
	payload, err := json.Marshal(map[string]string{"order_id": input.OrderID, "user_id": input.UserID, "account_id": input.AccountID, "status": initialStatus})
	if err != nil {
		return trading.Order{}, err
	}
	eventType := "order.pending"
	if input.Conditional {
		eventType = "order.conditional"
	}
	if _, err := tx.Exec(ctx, `INSERT INTO outbox_events (event_type,aggregate_type,aggregate_id,payload) VALUES ($3,'order',$1,$2::jsonb)`, input.OrderID, payload, eventType); err != nil {
		return trading.Order{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return trading.Order{}, err
	}
	order.EnginePayload = append([]byte(nil), input.EnginePayload...)
	return order, nil
}

func (store *Store) ApplyEngineStatus(ctx context.Context, orderID string, event protocol.OrderStatusEvent) (trading.Order, error) {
	if store.pool == nil || orderID == "" || event.OrderID != orderID {
		return trading.Order{}, trading.ErrEngineResponse
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return trading.Order{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var currentStatus, quantity string
	err = tx.QueryRow(ctx, `
		SELECT status,quantity::text FROM orders WHERE id=$1 FOR UPDATE`, orderID).
		Scan(&currentStatus, &quantity)
	if err != nil {
		return trading.Order{}, err
	}
	nextStatus := statusName(event.Status)
	if nextStatus == "" || !validTransition(currentStatus, nextStatus) {
		return trading.Order{}, trading.ErrEngineResponse
	}
	if !quantitiesBalance(quantity, event.FilledQuantity, event.RemainingQuantity) {
		return trading.Order{}, fmt.Errorf("%w: filled and remaining quantities do not equal order quantity", trading.ErrEngineResponse)
	}
	if currentStatus != nextStatus || currentStatus != "FILLED" && currentStatus != "CANCELED" && currentStatus != "REJECTED" {
		if _, err := tx.Exec(ctx, `
			UPDATE orders SET status=$2,filled_quantity=$3::numeric,remaining_quantity=$4::numeric,
			       avg_price=$5::numeric,engine_sequence_id=$6::numeric,updated_at=now()
			WHERE id=$1`, orderID, nextStatus, strconv.FormatUint(event.FilledQuantity, 10), strconv.FormatUint(event.RemainingQuantity, 10), strconv.FormatUint(event.AvgPrice, 10), strconv.FormatUint(event.SequenceID, 10)); err != nil {
			return trading.Order{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE engine_commands SET status='dispatched',attempts=attempts+1,last_error='',dispatched_at=now(),updated_at=now() WHERE order_id=$1`, orderID); err != nil {
			return trading.Order{}, err
		}
		if nextStatus == "REJECTED" || nextStatus == "CANCELED" {
			if err := releaseOrderHold(ctx, tx, orderID); err != nil {
				return trading.Order{}, err
			}
		}
		payload, marshalErr := json.Marshal(map[string]any{"order_id": orderID, "status": nextStatus, "sequence_id": event.SequenceID})
		if marshalErr != nil {
			return trading.Order{}, marshalErr
		}
		if _, err := tx.Exec(ctx, `INSERT INTO outbox_events (event_type,aggregate_type,aggregate_id,payload) VALUES ('order.status_changed','order',$1,$2::jsonb)`, orderID, payload); err != nil {
			return trading.Order{}, err
		}
	}
	order, err := findByID(ctx, tx, orderID)
	if err != nil {
		return trading.Order{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return trading.Order{}, err
	}
	return order, nil
}

func (store *Store) RecordDispatchFailure(ctx context.Context, orderID, reason string) error {
	if store.pool == nil || orderID == "" {
		return trading.ErrInvalidOrder
	}
	reason = strings.TrimSpace(reason)
	if len(reason) > 1000 {
		reason = reason[:1000]
	}
	command, err := store.pool.Exec(ctx, `UPDATE engine_commands SET attempts=attempts+1,last_error=$2,updated_at=now() WHERE order_id=$1 AND status='pending'`, orderID, reason)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		_, err = store.pool.Exec(ctx, `UPDATE engine_control_commands SET attempts=attempts+1,last_error=$2,updated_at=now() WHERE order_id=$1 AND status='pending'`, orderID, reason)
	}
	return err
}

func (store *Store) ListOrders(ctx context.Context, userID, accountID string, filter trading.OrderFilter) ([]trading.Order, error) {
	if store.pool == nil || userID == "" || accountID == "" {
		return nil, trading.ErrTradingAccountRequired
	}
	rows, err := store.pool.Query(ctx, `
		SELECT o.id::text,o.user_id::text,o.account_id::text,o.pair,o.side,o.type,COALESCE(o.price,0)::text,
		       COALESCE(o.trigger_price::text,''),o.quantity::text,o.filled_quantity::text,o.remaining_quantity::text,
		       o.avg_price::text,o.time_in_force,o.status,o.post_only,o.reduce_only,o.fee_tier,o.created_at,o.updated_at,c.payload
		FROM orders o JOIN engine_commands c ON c.order_id=o.id
		WHERE o.user_id=$1 AND o.account_id=$2
		  AND (($5 AND o.status IN ('FILLED','CANCELED','REJECTED'))
		       OR (NOT $5 AND o.status IN ('CONDITIONAL','PENDING','PENDING_CANCEL','OPEN','PARTIALLY_FILLED')))
		  AND ($3='' OR o.status=$3)
		  AND ($4='' OR o.pair=$4)
		ORDER BY o.created_at DESC,o.id DESC LIMIT $6 OFFSET $7`, userID, accountID, filter.Status, filter.Pair, filter.History, filter.Limit, filter.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	orders := make([]trading.Order, 0)
	for rows.Next() {
		var order trading.Order
		if err := rows.Scan(append(orderScanTargets(&order), &order.EnginePayload)...); err != nil {
			return nil, err
		}
		orders = append(orders, order)
	}
	return orders, rows.Err()
}

func (store *Store) GetOrder(ctx context.Context, userID, accountID, orderID string) (trading.Order, error) {
	if store.pool == nil || userID == "" || accountID == "" || orderID == "" {
		return trading.Order{}, trading.ErrOrderNotFound
	}
	var order trading.Order
	err := store.pool.QueryRow(ctx, `
		SELECT o.id::text,o.user_id::text,o.account_id::text,o.pair,o.side,o.type,COALESCE(o.price,0)::text,
		       COALESCE(o.trigger_price::text,''),o.quantity::text,o.filled_quantity::text,o.remaining_quantity::text,
		       o.avg_price::text,o.time_in_force,o.status,o.post_only,o.reduce_only,o.fee_tier,o.created_at,o.updated_at,c.payload
		FROM orders o JOIN engine_commands c ON c.order_id=o.id
		WHERE o.id=$1 AND o.user_id=$2 AND o.account_id=$3`, orderID, userID, accountID).
		Scan(append(orderScanTargets(&order), &order.EnginePayload)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return trading.Order{}, trading.ErrOrderNotFound
	}
	return order, err
}

func (store *Store) PrepareCancel(ctx context.Context, command trading.CancelOrderCommand) (trading.Order, error) {
	if store.pool == nil || command.UserID == "" || command.AccountID == "" || command.OrderID == "" || command.CommandID == "" || command.IdempotencyKey == "" || len(command.Payload) == 0 {
		return trading.Order{}, trading.ErrInvalidOrder
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return trading.Order{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var existingOrderID string
	err = tx.QueryRow(ctx, `SELECT order_id::text FROM engine_control_commands WHERE user_id=$1 AND idempotency_key=$2`, command.UserID, command.IdempotencyKey).Scan(&existingOrderID)
	if err == nil {
		if existingOrderID != command.OrderID {
			return trading.Order{}, trading.ErrIdempotencyConflict
		}
		order, findErr := findOwnedByID(ctx, tx, command.UserID, command.AccountID, command.OrderID, false)
		if findErr != nil {
			return trading.Order{}, findErr
		}
		order.Duplicate = true
		return order, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return trading.Order{}, err
	}

	order, err := findOwnedByID(ctx, tx, command.UserID, command.AccountID, command.OrderID, true)
	if err != nil {
		return trading.Order{}, err
	}
	switch order.Status {
	case "FILLED":
		return trading.Order{}, trading.ErrOrderAlreadyFilled
	case "CANCELED":
		order.Duplicate = true
		return order, tx.Commit(ctx)
	case "REJECTED":
		return trading.Order{}, trading.ErrOrderNotCancelable
	case "PENDING_CANCEL":
		order.Duplicate = true
		return order, tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO engine_control_commands(id,order_id,user_id,idempotency_key,payload) VALUES($1,$2,$3,$4,$5)`, command.CommandID, command.OrderID, command.UserID, command.IdempotencyKey, command.Payload); err != nil {
		return trading.Order{}, err
	}
	if order.Status == "CONDITIONAL" {
		if err = releaseOrderHold(ctx, tx, order.ID); err != nil {
			return trading.Order{}, err
		}
		if _, err = tx.Exec(ctx, `UPDATE orders SET status='CANCELED',remaining_quantity=quantity,updated_at=now() WHERE id=$1`, order.ID); err != nil {
			return trading.Order{}, err
		}
		if _, err = tx.Exec(ctx, `UPDATE engine_commands SET status='canceled',updated_at=now() WHERE order_id=$1 AND status='blocked'`, order.ID); err != nil {
			return trading.Order{}, err
		}
		if _, err = tx.Exec(ctx, `UPDATE engine_control_commands SET status='dispatched',updated_at=now() WHERE id=$1`, command.CommandID); err != nil {
			return trading.Order{}, err
		}
	} else {
		if _, err = tx.Exec(ctx, `UPDATE orders SET status='PENDING_CANCEL',updated_at=now() WHERE id=$1`, order.ID); err != nil {
			return trading.Order{}, err
		}
	}
	if err = appendOrderEvent(ctx, tx, order.ID, "order.cancel_requested", map[string]any{"order_id": order.ID, "command_id": command.CommandID}); err != nil {
		return trading.Order{}, err
	}
	order, err = findByID(ctx, tx, order.ID)
	if err != nil {
		return trading.Order{}, err
	}
	return order, tx.Commit(ctx)
}

func (store *Store) ApplyCancelAck(ctx context.Context, userID, orderID string, ack protocol.ControlAck) (trading.Order, error) {
	if store.pool == nil || userID == "" || orderID == "" || !ack.Accepted {
		return trading.Order{}, trading.ErrEngineResponse
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return trading.Order{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var status string
	err = tx.QueryRow(ctx, `SELECT status FROM engine_control_commands WHERE id=$1 AND order_id=$2 AND user_id=$3 FOR UPDATE`, ack.CommandID, orderID, userID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return trading.Order{}, trading.ErrEngineResponse
	}
	if err != nil {
		return trading.Order{}, err
	}
	order, err := findOwnedByID(ctx, tx, userID, "", orderID, true)
	if err != nil {
		return trading.Order{}, err
	}
	if status == "dispatched" && order.Status == "CANCELED" {
		order.Duplicate = true
		return order, tx.Commit(ctx)
	}
	if order.Status != "PENDING_CANCEL" {
		return trading.Order{}, trading.ErrOrderNotCancelable
	}
	if err = releaseOrderHold(ctx, tx, order.ID); err != nil {
		return trading.Order{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE orders SET status='CANCELED',engine_sequence_id=$2,updated_at=now() WHERE id=$1`, order.ID, strconv.FormatUint(ack.SequenceID, 10)); err != nil {
		return trading.Order{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE engine_control_commands SET status='dispatched',sequence_id=$2,attempts=attempts+1,last_error='',updated_at=now() WHERE id=$1`, ack.CommandID, strconv.FormatUint(ack.SequenceID, 10)); err != nil {
		return trading.Order{}, err
	}
	if err = appendOrderEvent(ctx, tx, order.ID, "order.status_changed", map[string]any{"order_id": order.ID, "status": "CANCELED", "sequence_id": ack.SequenceID}); err != nil {
		return trading.Order{}, err
	}
	order, err = findByID(ctx, tx, order.ID)
	if err != nil {
		return trading.Order{}, err
	}
	return order, tx.Commit(ctx)
}

func (store *Store) ActivateConditionalOrders(ctx context.Context, pair string, markPrice uint64, limit int) ([]trading.Order, error) {
	if store.pool == nil || pair == "" || markPrice == 0 {
		return nil, trading.ErrInvalidOrder
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `SELECT id::text FROM orders
		WHERE pair=$1 AND status='CONDITIONAL'
		  AND ((trigger_direction='UP' AND trigger_price <= $2::numeric) OR (trigger_direction='DOWN' AND trigger_price >= $2::numeric))
		ORDER BY created_at,id FOR UPDATE SKIP LOCKED LIMIT $3`, pair, strconv.FormatUint(markPrice, 10), limit)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	orders := make([]trading.Order, 0, len(ids))
	for _, id := range ids {
		if _, err = tx.Exec(ctx, `UPDATE orders SET status='PENDING',triggered_at=now(),updated_at=now() WHERE id=$1 AND status='CONDITIONAL'`, id); err != nil {
			return nil, err
		}
		if _, err = tx.Exec(ctx, `UPDATE engine_commands SET status='pending',updated_at=now() WHERE order_id=$1 AND status='blocked'`, id); err != nil {
			return nil, err
		}
		if err = appendOrderEvent(ctx, tx, id, "order.triggered", map[string]any{"order_id": id, "mark_price": markPrice}); err != nil {
			return nil, err
		}
		order, findErr := findByID(ctx, tx, id)
		if findErr != nil {
			return nil, findErr
		}
		orders = append(orders, order)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return orders, nil
}

func findOwnedByID(ctx context.Context, tx pgx.Tx, userID, accountID, orderID string, lock bool) (trading.Order, error) {
	query := `SELECT o.id::text,o.user_id::text,o.account_id::text,o.pair,o.side,o.type,COALESCE(o.price,0)::text,
		COALESCE(o.trigger_price::text,''),o.quantity::text,o.filled_quantity::text,o.remaining_quantity::text,o.avg_price::text,
		o.time_in_force,o.status,o.post_only,o.reduce_only,o.fee_tier,o.created_at,o.updated_at,c.payload
		FROM orders o JOIN engine_commands c ON c.order_id=o.id WHERE o.id=$1 AND o.user_id=$2 AND ($3='' OR o.account_id::text=$3)`
	if lock {
		query += ` FOR UPDATE OF o`
	}
	var order trading.Order
	err := tx.QueryRow(ctx, query, orderID, userID, accountID).Scan(append(orderScanTargets(&order), &order.EnginePayload)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return trading.Order{}, trading.ErrOrderNotFound
	}
	return order, err
}

func releaseOrderHold(ctx context.Context, tx pgx.Tx, orderID string) error {
	var accountID, assetID, originalRaw string
	err := tx.QueryRow(ctx, `SELECT account_id::text,hold_asset_id::text,hold_amount_atomic::text FROM orders
		WHERE id=$1 AND hold_released_at IS NULL AND hold_asset_id IS NOT NULL FOR UPDATE`, orderID).Scan(&accountID, &assetID, &originalRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = ledger.LockAccountAsset(ctx, tx, accountID, assetID); err != nil {
		return err
	}
	var consumedRaw string
	if err = tx.QueryRow(ctx, `SELECT COALESCE(SUM(hold_consumed_atomic),0)::text FROM trade_participants WHERE order_id=$1`, orderID).Scan(&consumedRaw); err != nil {
		return err
	}
	original, originalOK := new(big.Int).SetString(originalRaw, 10)
	consumed, consumedOK := new(big.Int).SetString(consumedRaw, 10)
	if !originalOK || !consumedOK {
		return fmt.Errorf("%w: order hold accounting is invalid", trading.ErrSettlementInvalid)
	}
	remaining := new(big.Int).Sub(original, consumed)
	heldBalance, err := bucketBalance(ctx, tx, accountID, assetID, "held")
	if err != nil {
		return err
	}
	if remaining.Sign() < 0 || heldBalance.Cmp(remaining) < 0 {
		return fmt.Errorf("%w: order held balance is inconsistent", trading.ErrSettlementInvalid)
	}
	if _, err = tx.Exec(ctx, `UPDATE orders SET hold_released_at=now(),updated_at=now() WHERE id=$1 AND hold_released_at IS NULL`, orderID); err != nil {
		return err
	}
	if remaining.Sign() == 0 {
		return nil
	}
	var journalID string
	if err = tx.QueryRow(ctx, `INSERT INTO journals(idempotency_key,reference_type,reference_id) VALUES($1,'order_release',$2) RETURNING id::text`, "order-release:"+orderID, orderID).Scan(&journalID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO postings(journal_id,account_id,asset_id,bucket,direction,amount_atomic) VALUES
		($1,$2,$3,'held','debit',$4::numeric),($1,$2,$3,'available','credit',$4::numeric)`, journalID, accountID, assetID, remaining.String())
	return err
}

func appendOrderEvent(ctx context.Context, tx pgx.Tx, orderID, eventType string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox_events(event_type,aggregate_type,aggregate_id,payload) VALUES($1,'order',$2,$3::jsonb)`, eventType, orderID, encoded)
	return err
}

func findByIdempotencyKey(ctx context.Context, tx pgx.Tx, userID, key string) (trading.Order, []byte, bool, error) {
	var order trading.Order
	var requestHash []byte
	err := tx.QueryRow(ctx, `
		SELECT o.id::text,o.user_id::text,o.account_id::text,o.pair,o.side,o.type,COALESCE(o.price,0)::text,COALESCE(o.trigger_price::text,''),
		       o.quantity::text,o.filled_quantity::text,o.remaining_quantity::text,o.avg_price::text,
		       o.time_in_force,o.status,o.post_only,o.reduce_only,o.fee_tier,o.created_at,o.updated_at,
		       o.request_hash,c.payload
		FROM orders o JOIN engine_commands c ON c.order_id=o.id
		WHERE o.user_id=$1 AND o.idempotency_key=$2`, userID, key).
		Scan(append(orderScanTargets(&order), &requestHash, &order.EnginePayload)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return trading.Order{}, nil, false, nil
	}
	return order, requestHash, err == nil, err
}

func findByID(ctx context.Context, tx pgx.Tx, orderID string) (trading.Order, error) {
	var order trading.Order
	err := tx.QueryRow(ctx, `
		SELECT o.id::text,o.user_id::text,o.account_id::text,o.pair,o.side,o.type,COALESCE(o.price,0)::text,COALESCE(o.trigger_price::text,''),
		       o.quantity::text,o.filled_quantity::text,o.remaining_quantity::text,o.avg_price::text,
		       o.time_in_force,o.status,o.post_only,o.reduce_only,o.fee_tier,o.created_at,o.updated_at,c.payload
		FROM orders o JOIN engine_commands c ON c.order_id=o.id WHERE o.id=$1`, orderID).
		Scan(append(orderScanTargets(&order), &order.EnginePayload)...)
	return order, err
}

func orderScanTargets(order *trading.Order) []any {
	return []any{
		&order.ID, &order.UserID, &order.AccountID, &order.Pair, &order.Side, &order.Type, &order.Price, &order.TriggerPrice,
		&order.Quantity, &order.FilledQuantity, &order.RemainingQuantity, &order.AvgPrice,
		&order.TimeInForce, &order.Status, &order.PostOnly, &order.ReduceOnly, &order.FeeTier,
		&order.CreatedAt, &order.UpdatedAt,
	}
}

func availableBalance(ctx context.Context, tx pgx.Tx, accountID, assetID string) (*big.Int, error) {
	var raw string
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(CASE p.direction WHEN 'credit' THEN p.amount_atomic ELSE -p.amount_atomic END),0)::text
		FROM postings p JOIN journals j ON j.id=p.journal_id AND j.status='posted'
		WHERE p.account_id=$1 AND p.asset_id=$2 AND p.bucket='available'`, accountID, assetID).Scan(&raw)
	if err != nil {
		return nil, err
	}
	amount, ok := new(big.Int).SetString(raw, 10)
	if !ok {
		return nil, fmt.Errorf("invalid available balance %q", raw)
	}
	return amount, nil
}

func withinPairLimits(quantity, price uint64, minimum, maximum, priceTick, quantityStep string) bool {
	quantityValue := new(big.Int).SetUint64(quantity)
	priceValue := new(big.Int).SetUint64(price)
	minimumValue, minimumOK := new(big.Int).SetString(minimum, 10)
	maximumValue, maximumOK := new(big.Int).SetString(maximum, 10)
	priceTickValue, priceTickOK := new(big.Int).SetString(priceTick, 10)
	quantityStepValue, quantityStepOK := new(big.Int).SetString(quantityStep, 10)
	if !minimumOK || !maximumOK || !priceTickOK || !quantityStepOK || priceTickValue.Sign() <= 0 || quantityStepValue.Sign() <= 0 {
		return false
	}
	if quantityValue.Cmp(minimumValue) < 0 || quantityValue.Cmp(maximumValue) > 0 || new(big.Int).Mod(quantityValue, quantityStepValue).Sign() != 0 {
		return false
	}
	return price == 0 || new(big.Int).Mod(priceValue, priceTickValue).Sign() == 0
}

func quantitiesBalance(quantity string, filled, remaining uint64) bool {
	total, ok := new(big.Int).SetString(quantity, 10)
	if !ok {
		return false
	}
	sum := new(big.Int).Add(new(big.Int).SetUint64(filled), new(big.Int).SetUint64(remaining))
	return total.Cmp(sum) == 0
}

func statusName(status protocol.OrderStatus) string {
	switch status {
	case protocol.OrderStatusOpen:
		return "OPEN"
	case protocol.OrderStatusPartial:
		return "PARTIALLY_FILLED"
	case protocol.OrderStatusFilled:
		return "FILLED"
	case protocol.OrderStatusCanceled:
		return "CANCELED"
	case protocol.OrderStatusRejected:
		return "REJECTED"
	default:
		return ""
	}
}

func validTransition(current, next string) bool {
	if current == next {
		return true
	}
	switch current {
	case "PENDING":
		return next == "OPEN" || next == "PARTIALLY_FILLED" || next == "FILLED" || next == "CANCELED" || next == "REJECTED"
	case "OPEN":
		return next == "PARTIALLY_FILLED" || next == "FILLED" || next == "CANCELED" || next == "REJECTED"
	case "PARTIALLY_FILLED":
		return next == "FILLED" || next == "CANCELED"
	case "PENDING_CANCEL":
		return next == "FILLED" || next == "CANCELED"
	default:
		return false
	}
}
