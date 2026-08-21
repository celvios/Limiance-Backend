package datamanager

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrConversionPairUnavailable = errors.New("conversion pair unavailable")
var ErrConversionQuoteExpired = errors.New("conversion quote expired")
var ErrConversionQuoteNotConfirmable = errors.New("conversion quote not confirmable")
var ErrConversionInsufficientBalance = errors.New("insufficient conversion balance")
var ErrConversionsDisabled = errors.New("conversions temporarily disabled")

// ConversionPair is operator-owned policy. A customer cannot choose a market
// symbol, spread, or fee; those are configured and audited by Limiance.
type ConversionPair struct {
	FromAssetID, ToAssetID   string
	FromSymbol, ToSymbol     string
	FromNetwork, ToNetwork   string
	FromDecimals, ToDecimals int16
	MarketSymbol             string
	SpreadBPS, FeeBPS        int
}

type ConversionQuoteInput struct {
	UserID, SourceAccountID                                string
	FromSymbol, FromNetwork                                string
	ToSymbol, ToNetwork                                    string
	InputAmountAtomic, OutputAmountAtomic, FeeAmountAtomic int64
	Price, Provider                                        string
	ExpiresAt                                              time.Time
}

type ConversionQuote struct {
	ID                 string    `json:"quote_id"`
	SourceAccountID    string    `json:"source_account_id"`
	FromSymbol         string    `json:"from_asset_symbol"`
	FromNetwork        string    `json:"from_network"`
	ToSymbol           string    `json:"to_asset_symbol"`
	ToNetwork          string    `json:"to_network"`
	InputAmountAtomic  int64     `json:"input_amount_atomic"`
	OutputAmountAtomic int64     `json:"output_amount_atomic"`
	FeeAmountAtomic    int64     `json:"fee_amount_atomic"`
	Price              string    `json:"price"`
	Provider           string    `json:"provider"`
	ExpiresAt          time.Time `json:"expires_at"`
}

type ConversionPairPolicyInput struct {
	FromSymbol   string `json:"from_asset_symbol"`
	FromNetwork  string `json:"from_network"`
	ToSymbol     string `json:"to_asset_symbol"`
	ToNetwork    string `json:"to_network"`
	MarketSymbol string `json:"market_symbol"`
	SpreadBPS    int    `json:"spread_bps"`
	FeeBPS       int    `json:"fee_bps"`
	Enabled      bool   `json:"enabled"`
}

func (m *Manager) SetConversionPairPolicy(ctx context.Context, actorID string, input ConversionPairPolicyInput) error {
	if actorID == "" || input.FromSymbol == "" || input.FromNetwork == "" || input.ToSymbol == "" || input.ToNetwork == "" || input.FromSymbol == input.ToSymbol && input.FromNetwork == input.ToNetwork || input.MarketSymbol == "" || input.SpreadBPS < 0 || input.SpreadBPS > 1000 || input.FeeBPS < 0 || input.FeeBPS > 1000 {
		return errors.New("invalid conversion pair policy")
	}
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var administrator bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM user_roles WHERE user_id=$1 AND role='platform_administrator')`, actorID).Scan(&administrator); err != nil {
		return err
	}
	if !administrator {
		return ErrAdministratorRoleRequired
	}
	var fromID, toID string
	if err = tx.QueryRow(ctx, `SELECT id::text FROM assets WHERE symbol=$1 AND network=$2 AND status='enabled' ORDER BY id LIMIT 1`, input.FromSymbol, input.FromNetwork).Scan(&fromID); err != nil {
		return ErrConversionPairUnavailable
	}
	if err = tx.QueryRow(ctx, `SELECT id::text FROM assets WHERE symbol=$1 AND network=$2 AND status='enabled' ORDER BY id LIMIT 1`, input.ToSymbol, input.ToNetwork).Scan(&toID); err != nil {
		return ErrConversionPairUnavailable
	}
	status := "disabled"
	if input.Enabled {
		status = "enabled"
	}
	if _, err = tx.Exec(ctx, `INSERT INTO conversion_pairs (from_asset_id,to_asset_id,market_symbol,spread_bps,fee_bps,status) VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (from_asset_id,to_asset_id) DO UPDATE SET market_symbol=EXCLUDED.market_symbol,spread_bps=EXCLUDED.spread_bps,fee_bps=EXCLUDED.fee_bps,status=EXCLUDED.status`, fromID, toID, input.MarketSymbol, input.SpreadBPS, input.FeeBPS, status); err != nil {
		return err
	}
	metadata, _ := json.Marshal(map[string]any{"from_asset_id": fromID, "to_asset_id": toID, "market_symbol": input.MarketSymbol, "spread_bps": input.SpreadBPS, "fee_bps": input.FeeBPS, "status": status})
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events (actor_id,actor_type,action,resource_type,resource_id,metadata) VALUES ($1,'user','conversion_pair.configured','conversion_pair',$2,$3::jsonb)`, actorID, fromID+":"+toID, metadata); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO outbox_events (event_type,aggregate_type,aggregate_id,payload) VALUES ('conversion_pair.configured','conversion_pair',$1,$2::jsonb)`, fromID+":"+toID, metadata); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (m *Manager) ConversionPair(ctx context.Context, fromSymbol, fromNetwork, toSymbol, toNetwork string) (ConversionPair, error) {
	if enabled, err := m.ConversionsEnabled(ctx); err != nil || !enabled {
		return ConversionPair{}, ErrConversionsDisabled
	}
	var pair ConversionPair
	err := m.pool.QueryRow(ctx, `SELECT fa.id::text, ta.id::text, fa.symbol, ta.symbol, fa.network, ta.network, fa.decimals, ta.decimals, p.market_symbol, p.spread_bps, p.fee_bps
		FROM conversion_pairs p JOIN assets fa ON fa.id=p.from_asset_id JOIN assets ta ON ta.id=p.to_asset_id
		WHERE fa.symbol=$1 AND fa.network=$2 AND ta.symbol=$3 AND ta.network=$4 AND fa.status='enabled' AND ta.status='enabled' AND p.status='enabled'`, fromSymbol, fromNetwork, toSymbol, toNetwork).
		Scan(&pair.FromAssetID, &pair.ToAssetID, &pair.FromSymbol, &pair.ToSymbol, &pair.FromNetwork, &pair.ToNetwork, &pair.FromDecimals, &pair.ToDecimals, &pair.MarketSymbol, &pair.SpreadBPS, &pair.FeeBPS)
	if err != nil {
		return ConversionPair{}, ErrConversionPairUnavailable
	}
	return pair, nil
}

func (m *Manager) ConversionsEnabled(ctx context.Context) (bool, error) {
	var enabled bool
	err := m.pool.QueryRow(ctx, `SELECT enabled FROM operational_controls WHERE control_key='conversions_enabled'`).Scan(&enabled)
	return enabled, err
}

func (m *Manager) SetConversionsEnabled(ctx context.Context, actorID string, enabled bool) error {
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var administrator bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM user_roles WHERE user_id=$1 AND role='platform_administrator')`, actorID).Scan(&administrator); err != nil {
		return err
	}
	if !administrator {
		return ErrAdministratorRoleRequired
	}
	if _, err = tx.Exec(ctx, `UPDATE operational_controls SET enabled=$1,updated_by=$2,updated_at=now() WHERE control_key='conversions_enabled'`, enabled, actorID); err != nil {
		return err
	}
	metadata, _ := json.Marshal(map[string]any{"enabled": enabled})
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(actor_id,actor_type,action,resource_type,resource_id,metadata) VALUES($1,'user','conversion.control_updated','operational_control','conversions_enabled',$2::jsonb)`, actorID, metadata); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (m *Manager) CreateConversionQuote(ctx context.Context, input ConversionQuoteInput) (ConversionQuote, error) {
	if input.UserID == "" || input.SourceAccountID == "" || input.InputAmountAtomic <= 0 || input.OutputAmountAtomic <= 0 || input.FeeAmountAtomic < 0 || input.Price == "" || input.Provider == "" || !input.ExpiresAt.After(time.Now()) {
		return ConversionQuote{}, errors.New("invalid conversion quote")
	}
	var q ConversionQuote
	err := m.pool.QueryRow(ctx, `WITH source AS (SELECT id FROM accounts WHERE id=$2 AND user_id=$1 AND status='active'), from_asset AS (SELECT id FROM assets WHERE symbol=$3 AND network=$4 AND status='enabled'), to_asset AS (SELECT id FROM assets WHERE symbol=$5 AND network=$6 AND status='enabled')
		INSERT INTO conversion_quotes (user_id,source_account_id,from_asset_id,to_asset_id,input_amount_atomic,output_amount_atomic,fee_amount_atomic,price,provider,expires_at)
		SELECT $1,source.id,from_asset.id,to_asset.id,$7,$8,$9,$10,$11,$12 FROM source,from_asset,to_asset RETURNING id::text`, input.UserID, input.SourceAccountID, input.FromSymbol, input.FromNetwork, input.ToSymbol, input.ToNetwork, input.InputAmountAtomic, input.OutputAmountAtomic, input.FeeAmountAtomic, input.Price, input.Provider, input.ExpiresAt).Scan(&q.ID)
	if err != nil {
		return ConversionQuote{}, err
	}
	q.SourceAccountID = input.SourceAccountID
	q.FromSymbol = input.FromSymbol
	q.FromNetwork = input.FromNetwork
	q.ToSymbol = input.ToSymbol
	q.ToNetwork = input.ToNetwork
	q.InputAmountAtomic = input.InputAmountAtomic
	q.OutputAmountAtomic = input.OutputAmountAtomic
	q.FeeAmountAtomic = input.FeeAmountAtomic
	q.Price = input.Price
	q.Provider = input.Provider
	q.ExpiresAt = input.ExpiresAt.UTC()
	return q, nil
}

type ConversionResult struct {
	ConversionID string `json:"conversion_id"`
	JournalID    string `json:"journal_id"`
	Status       string `json:"status"`
	Duplicate    bool   `json:"duplicate"`
}

// ConfirmConversion posts a single balanced journal. It never sends an order
// to a venue: the approved quote is settled against Limiance's own inventory.
func (m *Manager) ConfirmConversion(ctx context.Context, userID, quoteID, idempotencyKey string) (ConversionResult, error) {
	if userID == "" || quoteID == "" || idempotencyKey == "" {
		return ConversionResult{}, ErrConversionQuoteNotConfirmable
	}
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ConversionResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var enabled bool
	if err = tx.QueryRow(ctx, `SELECT enabled FROM operational_controls WHERE control_key='conversions_enabled' FOR SHARE`).Scan(&enabled); err != nil || !enabled {
		if err != nil {
			return ConversionResult{}, err
		}
		return ConversionResult{}, ErrConversionsDisabled
	}
	var existing ConversionResult
	err = tx.QueryRow(ctx, `SELECT id::text,journal_id::text,status FROM conversions WHERE idempotency_key=$1`, idempotencyKey).Scan(&existing.ConversionID, &existing.JournalID, &existing.Status)
	if err == nil {
		existing.Duplicate = true
		return existing, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ConversionResult{}, err
	}
	var sourceID, fromID, toID string
	var inputAmount, outputAmount, feeAmount int64
	var expires time.Time
	var confirmed *time.Time
	err = tx.QueryRow(ctx, `SELECT source_account_id::text,from_asset_id::text,to_asset_id::text,input_amount_atomic::bigint,output_amount_atomic::bigint,fee_amount_atomic::bigint,expires_at,confirmed_at FROM conversion_quotes WHERE id=$1 AND user_id=$2 FOR UPDATE`, quoteID, userID).Scan(&sourceID, &fromID, &toID, &inputAmount, &outputAmount, &feeAmount, &expires, &confirmed)
	if errors.Is(err, pgx.ErrNoRows) {
		return ConversionResult{}, ErrConversionQuoteNotConfirmable
	}
	if err != nil {
		return ConversionResult{}, err
	}
	if confirmed != nil {
		return ConversionResult{}, ErrConversionQuoteNotConfirmable
	}
	if !expires.After(time.Now().UTC()) {
		return ConversionResult{}, ErrConversionQuoteExpired
	}
	var sourceActive bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM accounts WHERE id=$1 AND user_id=$2 AND status='active')`, sourceID, userID).Scan(&sourceActive); err != nil || !sourceActive {
		return ConversionResult{}, ErrConversionQuoteNotConfirmable
	}
	var treasuryFrom, treasuryTo, feeTo string
	for _, a := range []struct {
		name, asset string
		dst         *string
	}{{"conversion-treasury-" + fromID, fromID, &treasuryFrom}, {"conversion-treasury-" + toID, toID, &treasuryTo}, {"conversion-fees-" + toID, toID, &feeTo}} {
		if err = tx.QueryRow(ctx, `INSERT INTO accounts (user_id,kind,name) VALUES (NULL,'system',$1) ON CONFLICT (name) WHERE kind='system' DO UPDATE SET name=EXCLUDED.name RETURNING id::text`, a.name).Scan(a.dst); err != nil {
			return ConversionResult{}, err
		}
	}
	locks := []string{sourceID + ":" + fromID, treasuryFrom + ":" + fromID, treasuryTo + ":" + toID}
	sort.Strings(locks)
	for _, lock := range locks {
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, lock); err != nil {
			return ConversionResult{}, err
		}
	}
	var customerAvailable, treasuryAvailable int64
	if err = tx.QueryRow(ctx, `SELECT COALESCE(SUM(CASE direction WHEN 'credit' THEN amount_atomic ELSE -amount_atomic END),0)::bigint FROM postings WHERE account_id=$1 AND asset_id=$2 AND bucket='available'`, sourceID, fromID).Scan(&customerAvailable); err != nil {
		return ConversionResult{}, err
	}
	if err = tx.QueryRow(ctx, `SELECT COALESCE(SUM(CASE direction WHEN 'credit' THEN amount_atomic ELSE -amount_atomic END),0)::bigint FROM postings WHERE account_id=$1 AND asset_id=$2 AND bucket='available'`, treasuryTo, toID).Scan(&treasuryAvailable); err != nil {
		return ConversionResult{}, err
	}
	if outputAmount > 9_223_372_036_854_775_807-feeAmount {
		return ConversionResult{}, ErrConversionQuoteNotConfirmable
	}
	gross := outputAmount + feeAmount
	if customerAvailable < inputAmount || treasuryAvailable < gross {
		return ConversionResult{}, ErrConversionInsufficientBalance
	}
	var result ConversionResult
	if err = tx.QueryRow(ctx, `INSERT INTO conversions (quote_id,user_id,idempotency_key,status) VALUES ($1,$2,$3,'settled') RETURNING id::text`, quoteID, userID, idempotencyKey).Scan(&result.ConversionID); err != nil {
		return ConversionResult{}, err
	}
	if err = tx.QueryRow(ctx, `INSERT INTO journals (idempotency_key,reference_type,reference_id) VALUES ($1,'conversion_settlement',$2) RETURNING id::text`, idempotencyKey, result.ConversionID).Scan(&result.JournalID); err != nil {
		return ConversionResult{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO postings (journal_id,account_id,asset_id,bucket,direction,amount_atomic) VALUES ($1,$2,$3,'available','debit',$4),($1,$5,$3,'available','credit',$4),($1,$6,$7,'available','debit',$8),($1,$2,$7,'available','credit',$9),($1,$10,$7,'available','credit',$11)`, result.JournalID, sourceID, fromID, inputAmount, treasuryFrom, treasuryTo, toID, gross, outputAmount, feeTo, feeAmount); err != nil {
		return ConversionResult{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE conversion_quotes SET confirmed_at=now() WHERE id=$1 AND confirmed_at IS NULL`, quoteID); err != nil {
		return ConversionResult{}, err
	}
	metadata, _ := json.Marshal(map[string]any{"quote_id": quoteID, "input_amount_atomic": inputAmount, "output_amount_atomic": outputAmount, "fee_amount_atomic": feeAmount})
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(actor_id,actor_type,action,resource_type,resource_id,metadata) VALUES($1,'user','conversion.settled','conversion',$2,$3::jsonb)`, userID, result.ConversionID, metadata); err != nil {
		return ConversionResult{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO outbox_events(event_type,aggregate_type,aggregate_id,payload) VALUES('conversion.settled','conversion',$1,$2::jsonb)`, result.ConversionID, metadata); err != nil {
		return ConversionResult{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE conversions SET journal_id=$2 WHERE id=$1`, result.ConversionID, result.JournalID); err != nil {
		return ConversionResult{}, err
	}
	result.Status = "settled"
	return result, tx.Commit(ctx)
}

type ConversionHistoryItem struct {
	ID                 string    `json:"id"`
	FromAssetSymbol    string    `json:"from_asset_symbol"`
	FromNetwork        string    `json:"from_network"`
	ToAssetSymbol      string    `json:"to_asset_symbol"`
	ToNetwork          string    `json:"to_network"`
	InputAmountAtomic  string    `json:"input_amount_atomic"`
	OutputAmountAtomic string    `json:"output_amount_atomic"`
	FeeAmountAtomic    string    `json:"fee_amount_atomic"`
	Status             string    `json:"status"`
	CreatedAt          time.Time `json:"created_at"`
}

func (m *Manager) ConversionHistory(ctx context.Context, userID string, limit int) ([]ConversionHistoryItem, error) {
	if limit < 1 || limit > 100 {
		limit = 25
	}
	rows, err := m.pool.Query(ctx, `SELECT c.id::text,fa.symbol,fa.network,ta.symbol,ta.network,q.input_amount_atomic::text,q.output_amount_atomic::text,q.fee_amount_atomic::text,c.status,c.created_at FROM conversions c JOIN conversion_quotes q ON q.id=c.quote_id JOIN assets fa ON fa.id=q.from_asset_id JOIN assets ta ON ta.id=q.to_asset_id WHERE c.user_id=$1 ORDER BY c.created_at DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ConversionHistoryItem, 0)
	for rows.Next() {
		var item ConversionHistoryItem
		if err := rows.Scan(&item.ID, &item.FromAssetSymbol, &item.FromNetwork, &item.ToAssetSymbol, &item.ToNetwork, &item.InputAmountAtomic, &item.OutputAmountAtomic, &item.FeeAmountAtomic, &item.Status, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
