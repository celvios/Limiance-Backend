package p2p

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/limiance/backend/internal/ledger"
)

type PostgresStore struct{ pool *pgxpool.Pool }

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

const tradeColumns = `SELECT t.id::text,t.seller_user_id::text,t.seller_account_id::text,
	COALESCE(t.buyer_user_id::text,''),COALESCE(t.buyer_account_id::text,''),t.asset_id::text,
	a.symbol,a.network,t.amount_atomic::text,t.fiat_currency,t.fiat_amount::text,t.payment_method,t.status,
	t.offer_expires_at,t.payment_deadline,t.accepted_at,t.paid_at,t.completed_at,t.created_at,t.updated_at
	FROM p2p_trades t JOIN assets a ON a.id=t.asset_id`

func (store *PostgresStore) Create(ctx context.Context, command CreateCommand) (Trade, error) {
	if store.pool == nil {
		return Trade{}, ErrInvalidRequest
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Trade{}, err
	}
	defer tx.Rollback(ctx)
	if trade, duplicate, err := existingAction(ctx, tx, userActor(command.UserID), command.IdempotencyKey, command.RequestHash); err != nil || duplicate {
		if err == nil {
			trade.Duplicate = true
			err = tx.Commit(ctx)
		}
		return trade, err
	}
	if err = requireFundingAccount(ctx, tx, command.UserID, command.AccountID); err != nil {
		return Trade{}, err
	}
	var assetID string
	if err = tx.QueryRow(ctx, `SELECT id::text FROM assets WHERE symbol=$1 AND network=$2 AND status='enabled'`, command.AssetSymbol, command.AssetNetwork).Scan(&assetID); errors.Is(err, pgx.ErrNoRows) {
		return Trade{}, ErrInvalidRequest
	} else if err != nil {
		return Trade{}, err
	}
	if err = ledger.LockAccountAsset(ctx, tx, command.AccountID, assetID); err != nil {
		return Trade{}, err
	}
	available, err := bucketBalance(ctx, tx, command.AccountID, assetID, "available")
	if err != nil {
		return Trade{}, err
	}
	amount, ok := new(big.Int).SetString(command.AmountAtomic, 10)
	if !ok || amount.Sign() <= 0 || available.Cmp(amount) < 0 {
		return Trade{}, ErrInsufficientBalance
	}
	if _, err = tx.Exec(ctx, `INSERT INTO p2p_trades(id,seller_user_id,seller_account_id,asset_id,amount_atomic,fiat_currency,fiat_amount,payment_method,status,offer_expires_at,created_at,updated_at)
		VALUES($1,$2,$3,$4,$5::numeric,$6,$7::numeric,$8,'open',$9,$10,$10)`, command.TradeID, command.UserID, command.AccountID, assetID, command.AmountAtomic, command.FiatCurrency, command.FiatAmount, command.PaymentMethod, command.OfferExpiresAt, command.Now); err != nil {
		return Trade{}, err
	}
	if err = postEscrow(ctx, tx, command.TradeID, command.AccountID, assetID, command.AmountAtomic); err != nil {
		return Trade{}, err
	}
	if err = recordEvent(ctx, tx, command.TradeID, "created", userActor(command.UserID), command.UserID, command.IdempotencyKey, command.RequestHash, map[string]any{"status": "open"}); err != nil {
		return Trade{}, err
	}
	trade, err := findTrade(ctx, tx, command.TradeID, false)
	if err != nil {
		return Trade{}, err
	}
	return trade, tx.Commit(ctx)
}

func (store *PostgresStore) Accept(ctx context.Context, command ActionCommand) (Trade, error) {
	tx, err := store.begin(ctx)
	if err != nil {
		return Trade{}, err
	}
	defer tx.Rollback(ctx)
	if trade, duplicate, err := existingAction(ctx, tx, userActor(command.UserID), command.IdempotencyKey, command.RequestHash); err != nil || duplicate {
		return commitDuplicate(ctx, tx, trade, duplicate, err)
	}
	trade, err := findTrade(ctx, tx, command.TradeID, true)
	if err != nil {
		return Trade{}, err
	}
	if trade.Status != "open" {
		return Trade{}, ErrInvalidTransition
	}
	if !command.Now.Before(trade.OfferExpiresAt) {
		return Trade{}, ErrExpired
	}
	if command.UserID == trade.SellerUserID {
		return Trade{}, ErrForbidden
	}
	if err = requireFundingAccount(ctx, tx, command.UserID, command.AccountID); err != nil {
		return Trade{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE p2p_trades SET buyer_user_id=$2,buyer_account_id=$3,status='accepted',accepted_at=$4,payment_deadline=$5,updated_at=$4 WHERE id=$1`, trade.ID, command.UserID, command.AccountID, command.Now, command.PaymentDeadline); err != nil {
		return Trade{}, err
	}
	if err = recordEvent(ctx, tx, trade.ID, "accepted", userActor(command.UserID), command.UserID, command.IdempotencyKey, command.RequestHash, map[string]any{"status": "accepted", "payment_deadline": command.PaymentDeadline}); err != nil {
		return Trade{}, err
	}
	trade, err = findTrade(ctx, tx, trade.ID, false)
	if err != nil {
		return Trade{}, err
	}
	return trade, tx.Commit(ctx)
}

func (store *PostgresStore) MarkPaid(ctx context.Context, command ActionCommand) (Trade, error) {
	return store.transition(ctx, command, "paid", func(trade Trade) error {
		if trade.Status != "accepted" {
			return ErrInvalidTransition
		}
		if trade.BuyerUserID != command.UserID || trade.BuyerAccountID != command.AccountID {
			return ErrForbidden
		}
		if trade.PaymentDeadline == nil || command.Now.After(*trade.PaymentDeadline) {
			return ErrExpired
		}
		return nil
	}, `UPDATE p2p_trades SET status='paid',paid_at=$2,updated_at=$2 WHERE id=$1`)
}

func (store *PostgresStore) Release(ctx context.Context, command ActionCommand) (Trade, error) {
	tx, err := store.begin(ctx)
	if err != nil {
		return Trade{}, err
	}
	defer tx.Rollback(ctx)
	if trade, duplicate, err := existingAction(ctx, tx, userActor(command.UserID), command.IdempotencyKey, command.RequestHash); err != nil || duplicate {
		return commitDuplicate(ctx, tx, trade, duplicate, err)
	}
	trade, err := findTrade(ctx, tx, command.TradeID, true)
	if err != nil {
		return Trade{}, err
	}
	if trade.Status != "paid" {
		return Trade{}, ErrInvalidTransition
	}
	if trade.SellerUserID != command.UserID || trade.SellerAccountID != command.AccountID {
		return Trade{}, ErrForbidden
	}
	if err = releaseEscrow(ctx, tx, trade, "p2p-release:"); err != nil {
		return Trade{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE p2p_trades SET status='released',completed_at=$2,updated_at=$2 WHERE id=$1`, trade.ID, command.Now); err != nil {
		return Trade{}, err
	}
	if err = recordEvent(ctx, tx, trade.ID, "released", userActor(command.UserID), command.UserID, command.IdempotencyKey, command.RequestHash, map[string]any{"status": "released"}); err != nil {
		return Trade{}, err
	}
	trade, err = findTrade(ctx, tx, trade.ID, false)
	if err != nil {
		return Trade{}, err
	}
	return trade, tx.Commit(ctx)
}

func (store *PostgresStore) Cancel(ctx context.Context, command ActionCommand) (Trade, error) {
	tx, err := store.begin(ctx)
	if err != nil {
		return Trade{}, err
	}
	defer tx.Rollback(ctx)
	if trade, duplicate, err := existingAction(ctx, tx, userActor(command.UserID), command.IdempotencyKey, command.RequestHash); err != nil || duplicate {
		return commitDuplicate(ctx, tx, trade, duplicate, err)
	}
	trade, err := findTrade(ctx, tx, command.TradeID, true)
	if err != nil {
		return Trade{}, err
	}
	if trade.Status != "open" {
		return Trade{}, ErrInvalidTransition
	}
	if trade.SellerUserID != command.UserID || trade.SellerAccountID != command.AccountID {
		return Trade{}, ErrForbidden
	}
	if err = refundEscrow(ctx, tx, trade, "p2p-cancel:"); err != nil {
		return Trade{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE p2p_trades SET status='cancelled',completed_at=$2,updated_at=$2 WHERE id=$1`, trade.ID, command.Now); err != nil {
		return Trade{}, err
	}
	if err = recordEvent(ctx, tx, trade.ID, "cancelled", userActor(command.UserID), command.UserID, command.IdempotencyKey, command.RequestHash, map[string]any{"status": "cancelled"}); err != nil {
		return Trade{}, err
	}
	trade, err = findTrade(ctx, tx, trade.ID, false)
	if err != nil {
		return Trade{}, err
	}
	return trade, tx.Commit(ctx)
}

func (store *PostgresStore) Dispute(ctx context.Context, command DisputeCommand) (Trade, error) {
	return store.transition(ctx, command.ActionCommand, "disputed", func(trade Trade) error {
		if trade.Status != "accepted" && trade.Status != "paid" {
			return ErrInvalidTransition
		}
		if !isParticipant(trade, command.UserID, command.AccountID) {
			return ErrForbidden
		}
		return nil
	}, `UPDATE p2p_trades SET status='disputed',updated_at=$2 WHERE id=$1`, command.Reason)
}

func (store *PostgresStore) AddEvidence(ctx context.Context, command EvidenceCommand) (Trade, error) {
	tx, err := store.begin(ctx)
	if err != nil {
		return Trade{}, err
	}
	defer tx.Rollback(ctx)
	if trade, duplicate, err := existingAction(ctx, tx, userActor(command.UserID), command.IdempotencyKey, command.RequestHash); err != nil || duplicate {
		return commitDuplicate(ctx, tx, trade, duplicate, err)
	}
	trade, err := findTrade(ctx, tx, command.TradeID, true)
	if err != nil {
		return Trade{}, err
	}
	if trade.Status != "disputed" {
		return Trade{}, ErrInvalidTransition
	}
	if !isParticipant(trade, command.UserID, command.AccountID) {
		return Trade{}, ErrForbidden
	}
	if _, err = tx.Exec(ctx, `INSERT INTO p2p_evidence(id,trade_id,submitted_by,evidence_type,object_key,sha256_hex) VALUES($1,$2,$3,$4,$5,$6)`, command.EvidenceID, trade.ID, command.UserID, command.EvidenceType, command.ObjectKey, command.SHA256Hex); err != nil {
		return Trade{}, err
	}
	if err = recordEvent(ctx, tx, trade.ID, "evidence_added", userActor(command.UserID), command.UserID, command.IdempotencyKey, command.RequestHash, map[string]any{"evidence_id": command.EvidenceID, "evidence_type": command.EvidenceType}); err != nil {
		return Trade{}, err
	}
	return trade, tx.Commit(ctx)
}

func (store *PostgresStore) Expire(ctx context.Context, now time.Time, limit int) ([]Trade, error) {
	tx, err := store.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT id::text FROM p2p_trades
		WHERE (status='open' AND offer_expires_at <= $1) OR (status='accepted' AND payment_deadline <= $1)
		ORDER BY COALESCE(payment_deadline,offer_expires_at),id LIMIT $2 FOR UPDATE SKIP LOCKED`, now, limit)
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
	result := make([]Trade, 0, len(ids))
	for _, id := range ids {
		trade, findErr := findTrade(ctx, tx, id, false)
		if findErr != nil {
			return nil, findErr
		}
		if err = refundEscrow(ctx, tx, trade, "p2p-expiry:"); err != nil {
			return nil, err
		}
		if _, err = tx.Exec(ctx, `UPDATE p2p_trades SET status='expired',completed_at=$2,updated_at=$2 WHERE id=$1`, id, now); err != nil {
			return nil, err
		}
		hash := requestHash("expire", id)
		if err = recordEvent(ctx, tx, id, "expired", "system:expiry", "", "expiry:"+id, hash, map[string]any{"status": "expired"}); err != nil {
			return nil, err
		}
		trade.Status, trade.CompletedAt, trade.UpdatedAt = "expired", &now, now
		result = append(result, trade)
	}
	return result, tx.Commit(ctx)
}

func (store *PostgresStore) ProposeResolution(ctx context.Context, command ResolutionCommand) (Resolution, error) {
	tx, err := store.begin(ctx)
	if err != nil {
		return Resolution{}, err
	}
	defer tx.Rollback(ctx)
	if allowed, roleErr := hasRole(ctx, tx, command.ActorID, "treasury_operator"); roleErr != nil {
		return Resolution{}, roleErr
	} else if !allowed {
		return Resolution{}, ErrApprovalRole
	}
	if trade, duplicate, actionErr := existingAction(ctx, tx, userActor(command.ActorID), command.IdempotencyKey, command.RequestHash); actionErr != nil {
		return Resolution{}, actionErr
	} else if duplicate {
		resolution, findErr := findResolutionByTrade(ctx, tx, trade.ID)
		if findErr == nil {
			resolution.Duplicate = true
			findErr = tx.Commit(ctx)
		}
		return resolution, findErr
	}
	trade, err := findTrade(ctx, tx, command.TradeID, true)
	if err != nil {
		return Resolution{}, err
	}
	if trade.Status != "disputed" {
		return Resolution{}, ErrInvalidTransition
	}
	if _, err = tx.Exec(ctx, `INSERT INTO p2p_resolution_requests(id,trade_id,action,reason,proposed_by,created_at) VALUES($1,$2,$3,$4,$5,$6)`, command.ResolutionID, trade.ID, command.Action, command.Reason, command.ActorID, command.Now); err != nil {
		return Resolution{}, err
	}
	if err = recordEvent(ctx, tx, trade.ID, "resolution_proposed", userActor(command.ActorID), command.ActorID, command.IdempotencyKey, command.RequestHash, map[string]any{"resolution_id": command.ResolutionID, "action": command.Action}); err != nil {
		return Resolution{}, err
	}
	if err = auditResolution(ctx, tx, command.ActorID, "p2p.resolution_proposed", command.ResolutionID, command.Reason, map[string]any{"trade_id": trade.ID, "action": command.Action}); err != nil {
		return Resolution{}, err
	}
	resolution, err := findResolution(ctx, tx, command.ResolutionID, false)
	if err != nil {
		return Resolution{}, err
	}
	return resolution, tx.Commit(ctx)
}

func (store *PostgresStore) ApproveResolution(ctx context.Context, command ApprovalCommand) (Trade, error) {
	tx, err := store.begin(ctx)
	if err != nil {
		return Trade{}, err
	}
	defer tx.Rollback(ctx)
	if allowed, roleErr := hasRole(ctx, tx, command.ActorID, "treasury_approver"); roleErr != nil {
		return Trade{}, roleErr
	} else if !allowed {
		return Trade{}, ErrApprovalRole
	}
	if trade, duplicate, actionErr := existingAction(ctx, tx, userActor(command.ActorID), command.IdempotencyKey, command.RequestHash); actionErr != nil || duplicate {
		return commitDuplicate(ctx, tx, trade, duplicate, actionErr)
	}
	resolution, err := findResolution(ctx, tx, command.ResolutionID, true)
	if err != nil {
		return Trade{}, err
	}
	if resolution.Status != "pending" {
		return Trade{}, ErrInvalidTransition
	}
	if resolution.ProposedBy == command.ActorID {
		return Trade{}, ErrSameApprover
	}
	trade, err := findTrade(ctx, tx, resolution.TradeID, true)
	if err != nil {
		return Trade{}, err
	}
	if trade.Status != "disputed" {
		return Trade{}, ErrInvalidTransition
	}
	status := "refunded"
	if resolution.Action == "force_release" {
		if err = releaseEscrow(ctx, tx, trade, "p2p-resolution-release:"); err != nil {
			return Trade{}, err
		}
		status = "released"
	} else if err = refundEscrow(ctx, tx, trade, "p2p-resolution-refund:"); err != nil {
		return Trade{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE p2p_trades SET status=$2,completed_at=$3,updated_at=$3 WHERE id=$1`, trade.ID, status, command.Now); err != nil {
		return Trade{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE p2p_resolution_requests SET status='approved',approved_by=$2,decided_at=$3 WHERE id=$1`, resolution.ID, command.ActorID, command.Now); err != nil {
		return Trade{}, err
	}
	if err = recordEvent(ctx, tx, trade.ID, "resolution_approved", userActor(command.ActorID), command.ActorID, command.IdempotencyKey, command.RequestHash, map[string]any{"resolution_id": resolution.ID, "status": status}); err != nil {
		return Trade{}, err
	}
	if err = auditResolution(ctx, tx, command.ActorID, "p2p.resolution_approved", resolution.ID, resolution.Reason, map[string]any{"trade_id": trade.ID, "maker": resolution.ProposedBy, "checker": command.ActorID, "outcome": status}); err != nil {
		return Trade{}, err
	}
	trade, err = findTrade(ctx, tx, trade.ID, false)
	if err != nil {
		return Trade{}, err
	}
	return trade, tx.Commit(ctx)
}

func (store *PostgresStore) Get(ctx context.Context, userID, tradeID string) (Trade, error) {
	trade, err := scanTrade(store.pool.QueryRow(ctx, tradeColumns+` WHERE t.id=$1 AND (t.seller_user_id=$2 OR t.buyer_user_id=$2)`, tradeID, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Trade{}, ErrTradeNotFound
	}
	return trade, err
}

func (store *PostgresStore) List(ctx context.Context, userID string, limit, offset int) ([]Trade, error) {
	rows, err := store.pool.Query(ctx, tradeColumns+` WHERE (t.seller_user_id=$1 OR t.buyer_user_id=$1) ORDER BY t.created_at DESC,t.id LIMIT $2 OFFSET $3`, userID, limit, offset)
	return scanTrades(rows, err)
}

func (store *PostgresStore) ListOpen(ctx context.Context, now time.Time, limit, offset int) ([]Trade, error) {
	rows, err := store.pool.Query(ctx, tradeColumns+` WHERE t.status='open' AND t.offer_expires_at>$1 ORDER BY t.created_at,t.id LIMIT $2 OFFSET $3`, now, limit, offset)
	return scanTrades(rows, err)
}

func (store *PostgresStore) ListDisputes(ctx context.Context, actorID string, limit, offset int) ([]Trade, error) {
	var allowed bool
	err := store.pool.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM user_roles WHERE user_id=$1 AND role IN ('treasury_operator','treasury_approver')
	)`, actorID).Scan(&allowed)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, ErrApprovalRole
	}
	rows, err := store.pool.Query(ctx, tradeColumns+` WHERE t.status='disputed' ORDER BY t.updated_at,t.id LIMIT $1 OFFSET $2`, limit, offset)
	return scanTrades(rows, err)
}

func scanTrades(rows pgx.Rows, err error) ([]Trade, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	trades := make([]Trade, 0)
	for rows.Next() {
		trade, scanErr := scanTrade(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		trades = append(trades, trade)
	}
	return trades, rows.Err()
}

func (store *PostgresStore) transition(ctx context.Context, command ActionCommand, event string, validate func(Trade) error, updateSQL string, payload ...string) (Trade, error) {
	tx, err := store.begin(ctx)
	if err != nil {
		return Trade{}, err
	}
	defer tx.Rollback(ctx)
	if trade, duplicate, err := existingAction(ctx, tx, userActor(command.UserID), command.IdempotencyKey, command.RequestHash); err != nil || duplicate {
		return commitDuplicate(ctx, tx, trade, duplicate, err)
	}
	trade, err := findTrade(ctx, tx, command.TradeID, true)
	if err != nil {
		return Trade{}, err
	}
	if err = validate(trade); err != nil {
		return Trade{}, err
	}
	if _, err = tx.Exec(ctx, updateSQL, trade.ID, command.Now); err != nil {
		return Trade{}, err
	}
	eventPayload := map[string]any{"status": event}
	if len(payload) > 0 {
		eventPayload["reason"] = payload[0]
	}
	if err = recordEvent(ctx, tx, trade.ID, event, userActor(command.UserID), command.UserID, command.IdempotencyKey, command.RequestHash, eventPayload); err != nil {
		return Trade{}, err
	}
	trade, err = findTrade(ctx, tx, trade.ID, false)
	if err != nil {
		return Trade{}, err
	}
	return trade, tx.Commit(ctx)
}

func (store *PostgresStore) begin(ctx context.Context) (pgx.Tx, error) {
	if store.pool == nil {
		return nil, ErrInvalidRequest
	}
	return store.pool.BeginTx(ctx, pgx.TxOptions{})
}

func existingAction(ctx context.Context, tx pgx.Tx, actorKey, idempotencyKey string, hash [32]byte) (Trade, bool, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "p2p-idempotency:"+actorKey+":"+idempotencyKey); err != nil {
		return Trade{}, false, err
	}
	var tradeID string
	var storedHash []byte
	err := tx.QueryRow(ctx, `SELECT trade_id::text,request_hash FROM p2p_events WHERE actor_key=$1 AND idempotency_key=$2`, actorKey, idempotencyKey).Scan(&tradeID, &storedHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return Trade{}, false, nil
	}
	if err != nil {
		return Trade{}, false, err
	}
	if !bytes.Equal(storedHash, hash[:]) {
		return Trade{}, false, ErrIdempotencyConflict
	}
	trade, err := findTrade(ctx, tx, tradeID, false)
	return trade, err == nil, err
}

func commitDuplicate(ctx context.Context, tx pgx.Tx, trade Trade, duplicate bool, err error) (Trade, error) {
	if err != nil || !duplicate {
		return trade, err
	}
	trade.Duplicate = true
	return trade, tx.Commit(ctx)
}

func requireFundingAccount(ctx context.Context, tx pgx.Tx, userID, accountID string) error {
	var accountStatus, userStatus, kind string
	err := tx.QueryRow(ctx, `SELECT a.status,u.status,a.kind::text FROM accounts a JOIN users u ON u.id=a.user_id WHERE a.id=$1 AND a.user_id=$2 FOR UPDATE OF a,u`, accountID, userID).Scan(&accountStatus, &userStatus, &kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrFundingAccountNeeded
	}
	if err != nil {
		return err
	}
	if accountStatus != "active" || userStatus != "active" || kind != "funding" {
		return ErrFundingAccountNeeded
	}
	return nil
}

func postEscrow(ctx context.Context, tx pgx.Tx, tradeID, accountID, assetID, amount string) error {
	var journalID string
	if err := tx.QueryRow(ctx, `INSERT INTO journals(idempotency_key,reference_type,reference_id) VALUES($1,'p2p_escrow',$2) RETURNING id::text`, "p2p-escrow:"+tradeID, tradeID).Scan(&journalID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO postings(journal_id,account_id,asset_id,bucket,direction,amount_atomic) VALUES
		($1,$2,$3,'available','debit',$4::numeric),($1,$2,$3,'held','credit',$4::numeric)`, journalID, accountID, assetID, amount)
	return err
}

func releaseEscrow(ctx context.Context, tx pgx.Tx, trade Trade, prefix string) error {
	if trade.BuyerAccountID == "" {
		return ErrInvalidTransition
	}
	if err := lockBalances(ctx, tx, trade.AssetID, trade.SellerAccountID, trade.BuyerAccountID); err != nil {
		return err
	}
	held, err := bucketBalance(ctx, tx, trade.SellerAccountID, trade.AssetID, "held")
	amount, ok := new(big.Int).SetString(trade.AmountAtomic, 10)
	if err != nil {
		return err
	}
	if !ok || held.Cmp(amount) < 0 {
		return ErrInsufficientBalance
	}
	var journalID string
	if err = tx.QueryRow(ctx, `INSERT INTO journals(idempotency_key,reference_type,reference_id) VALUES($1,'p2p_release',$2) RETURNING id::text`, prefix+trade.ID, trade.ID).Scan(&journalID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO postings(journal_id,account_id,asset_id,bucket,direction,amount_atomic) VALUES
		($1,$2,$4,'held','debit',$5::numeric),($1,$3,$4,'available','credit',$5::numeric)`, journalID, trade.SellerAccountID, trade.BuyerAccountID, trade.AssetID, trade.AmountAtomic)
	return err
}

func refundEscrow(ctx context.Context, tx pgx.Tx, trade Trade, prefix string) error {
	if err := ledger.LockAccountAsset(ctx, tx, trade.SellerAccountID, trade.AssetID); err != nil {
		return err
	}
	held, err := bucketBalance(ctx, tx, trade.SellerAccountID, trade.AssetID, "held")
	amount, ok := new(big.Int).SetString(trade.AmountAtomic, 10)
	if err != nil {
		return err
	}
	if !ok || held.Cmp(amount) < 0 {
		return ErrInsufficientBalance
	}
	var journalID string
	if err = tx.QueryRow(ctx, `INSERT INTO journals(idempotency_key,reference_type,reference_id) VALUES($1,'p2p_refund',$2) RETURNING id::text`, prefix+trade.ID, trade.ID).Scan(&journalID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO postings(journal_id,account_id,asset_id,bucket,direction,amount_atomic) VALUES
		($1,$2,$3,'held','debit',$4::numeric),($1,$2,$3,'available','credit',$4::numeric)`, journalID, trade.SellerAccountID, trade.AssetID, trade.AmountAtomic)
	return err
}

func lockBalances(ctx context.Context, tx pgx.Tx, assetID string, accountIDs ...string) error {
	keys := append([]string(nil), accountIDs...)
	sort.Slice(keys, func(i, j int) bool {
		return ledger.AccountAssetLockKey(keys[i], assetID) < ledger.AccountAssetLockKey(keys[j], assetID)
	})
	for _, accountID := range keys {
		if err := ledger.LockAccountAsset(ctx, tx, accountID, assetID); err != nil {
			return err
		}
	}
	return nil
}

func bucketBalance(ctx context.Context, tx pgx.Tx, accountID, assetID, bucket string) (*big.Int, error) {
	var raw string
	err := tx.QueryRow(ctx, `SELECT COALESCE(SUM(CASE p.direction WHEN 'credit' THEN p.amount_atomic ELSE -p.amount_atomic END),0)::text
		FROM postings p JOIN journals j ON j.id=p.journal_id AND j.status='posted'
		WHERE p.account_id=$1 AND p.asset_id=$2 AND p.bucket=$3::balance_bucket`, accountID, assetID, bucket).Scan(&raw)
	if err != nil {
		return nil, err
	}
	value, ok := new(big.Int).SetString(raw, 10)
	if !ok {
		return nil, fmt.Errorf("invalid ledger balance %q", raw)
	}
	return value, nil
}

func recordEvent(ctx context.Context, tx pgx.Tx, tradeID, eventType, actorKey, actorUserID, key string, hash [32]byte, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var actor any
	if actorUserID != "" {
		actor = actorUserID
	}
	if _, err = tx.Exec(ctx, `INSERT INTO p2p_events(trade_id,event_type,actor_key,actor_user_id,idempotency_key,request_hash,payload) VALUES($1,$2,$3,$4,$5,$6,$7::jsonb)`, tradeID, eventType, actorKey, actor, key, hash[:], encoded); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox_events(event_type,aggregate_type,aggregate_id,payload) VALUES($1,'p2p_trade',$2,$3::jsonb)`, "p2p."+eventType, tradeID, encoded)
	return err
}

func findTrade(ctx context.Context, tx pgx.Tx, tradeID string, lock bool) (Trade, error) {
	query := tradeColumns + ` WHERE t.id=$1`
	if lock {
		query += ` FOR UPDATE OF t`
	}
	trade, err := scanTrade(tx.QueryRow(ctx, query, tradeID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Trade{}, ErrTradeNotFound
	}
	return trade, err
}

type rowScanner interface{ Scan(...any) error }

func scanTrade(row rowScanner) (Trade, error) {
	var trade Trade
	err := row.Scan(&trade.ID, &trade.SellerUserID, &trade.SellerAccountID, &trade.BuyerUserID, &trade.BuyerAccountID,
		&trade.AssetID, &trade.AssetSymbol, &trade.AssetNetwork, &trade.AmountAtomic, &trade.FiatCurrency, &trade.FiatAmount,
		&trade.PaymentMethod, &trade.Status, &trade.OfferExpiresAt, &trade.PaymentDeadline, &trade.AcceptedAt, &trade.PaidAt,
		&trade.CompletedAt, &trade.CreatedAt, &trade.UpdatedAt)
	return trade, err
}

func findResolution(ctx context.Context, tx pgx.Tx, id string, lock bool) (Resolution, error) {
	query := `SELECT id::text,trade_id::text,action,reason,proposed_by::text,COALESCE(approved_by::text,''),status,created_at,decided_at FROM p2p_resolution_requests WHERE id=$1`
	if lock {
		query += ` FOR UPDATE`
	}
	var result Resolution
	err := tx.QueryRow(ctx, query, id).Scan(&result.ID, &result.TradeID, &result.Action, &result.Reason, &result.ProposedBy, &result.ApprovedBy, &result.Status, &result.CreatedAt, &result.DecidedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Resolution{}, ErrResolutionNotFound
	}
	return result, err
}

func findResolutionByTrade(ctx context.Context, tx pgx.Tx, tradeID string) (Resolution, error) {
	var result Resolution
	err := tx.QueryRow(ctx, `SELECT id::text,trade_id::text,action,reason,proposed_by::text,COALESCE(approved_by::text,''),status,created_at,decided_at FROM p2p_resolution_requests WHERE trade_id=$1 ORDER BY created_at DESC LIMIT 1`, tradeID).
		Scan(&result.ID, &result.TradeID, &result.Action, &result.Reason, &result.ProposedBy, &result.ApprovedBy, &result.Status, &result.CreatedAt, &result.DecidedAt)
	return result, err
}

func hasRole(ctx context.Context, tx pgx.Tx, actorID, role string) (bool, error) {
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM user_roles WHERE user_id=$1 AND role=$2)`, actorID, role).Scan(&allowed)
	return allowed, err
}

func auditResolution(ctx context.Context, tx pgx.Tx, actorID, action, resourceID, reason string, metadata any) error {
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_events(actor_id,actor_type,action,resource_type,resource_id,reason,metadata) VALUES($1,'administrator',$2,'p2p_resolution',$3,$4,$5::jsonb)`, actorID, action, resourceID, reason, encoded)
	return err
}

func isParticipant(trade Trade, userID, accountID string) bool {
	return trade.SellerUserID == userID && trade.SellerAccountID == accountID || trade.BuyerUserID == userID && trade.BuyerAccountID == accountID
}

func userActor(userID string) string { return "user:" + userID }
