package marketmaker

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresStore struct{ pool *pgxpool.Pool }

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

func (store *PostgresStore) LoadControl(ctx context.Context) (Control, error) {
	var control Control
	err := store.pool.QueryRow(ctx, `SELECT c.enabled,c.dry_run,c.kill_switch,COALESCE(c.user_id::text,''),COALESCE(c.account_id::text,'')
		FROM market_maker_control c WHERE singleton=TRUE`).Scan(&control.Enabled, &control.DryRun, &control.KillSwitch, &control.UserID, &control.AccountID)
	if err != nil {
		return Control{}, err
	}
	if control.UserID != "" {
		var valid bool
		err = store.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM accounts a JOIN users u ON u.id=a.user_id
			WHERE a.id=$1 AND a.user_id=$2 AND a.kind='uta' AND a.status='active' AND u.status='active')`, control.AccountID, control.UserID).Scan(&valid)
		if err != nil {
			return Control{}, err
		}
		if !valid {
			return Control{}, fmt.Errorf("%w: internal account is invalid", ErrRiskLimit)
		}
	}
	return control, nil
}

func (store *PostgresStore) ListConfigs(ctx context.Context) ([]Config, error) {
	rows, err := store.pool.Query(ctx, `SELECT c.pair,c.enabled,p.price_scale,p.quantity_scale,q.decimals,p.price_tick_atomic::text,c.spread_bps,c.quantity_atomic::text,
		c.max_base_inventory_atomic::text,c.max_quote_notional_atomic::text,c.max_daily_loss_atomic::text,
		c.max_divergence_bps,c.stale_after_seconds
		FROM market_maker_configs c JOIN trading_pairs p ON p.symbol=c.pair JOIN assets q ON q.id=p.quote_asset_id
		WHERE p.status='active' ORDER BY c.pair`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	configs := make([]Config, 0)
	for rows.Next() {
		var config Config
		var staleSeconds int
		if err = rows.Scan(&config.Pair, &config.Enabled, &config.PriceScale, &config.QuantityScale, &config.QuoteScale, &config.PriceTickAtomic, &config.SpreadBPS, &config.QuantityAtomic,
			&config.MaxBaseInventoryAtomic, &config.MaxQuoteNotionalAtomic, &config.MaxDailyLossAtomic, &config.MaxDivergenceBPS, &staleSeconds); err != nil {
			return nil, err
		}
		config.StaleAfter = time.Duration(staleSeconds) * time.Second
		configs = append(configs, config)
	}
	return configs, rows.Err()
}

func (store *PostgresStore) Risk(ctx context.Context, accountID, pair string) (RiskSnapshot, error) {
	var inventory, pnl string
	err := store.pool.QueryRow(ctx, `SELECT
		COALESCE((SELECT SUM(CASE po.direction WHEN 'credit' THEN po.amount_atomic ELSE -po.amount_atomic END)::text
			FROM postings po JOIN journals j ON j.id=po.journal_id AND j.status='posted'
			WHERE po.account_id=$1 AND po.asset_id=p.base_asset_id),'0'),
		r.realized_pnl_quote_atomic::text
		FROM trading_pairs p JOIN market_maker_risk_state r ON r.pair=p.symbol WHERE p.symbol=$2`, accountID, pair).Scan(&inventory, &pnl)
	if errors.Is(err, pgx.ErrNoRows) {
		return RiskSnapshot{}, ErrRiskLimit
	}
	if err != nil {
		return RiskSnapshot{}, err
	}
	if _, ok := new(big.Int).SetString(inventory, 10); !ok {
		return RiskSnapshot{}, ErrRiskLimit
	}
	return RiskSnapshot{BaseInventoryAtomic: inventory, DailyPnLQuoteAtomic: pnl}, nil
}

func (store *PostgresStore) ClaimCommand(ctx context.Context, key, pair, side, price, quantity string, dryRun bool) (bool, error) {
	result, err := store.pool.Exec(ctx, `INSERT INTO market_maker_command_log(idempotency_key,pair,side,price_atomic,quantity_atomic,dry_run)
		VALUES($1,$2,$3,$4::numeric,$5::numeric,$6) ON CONFLICT (idempotency_key) DO NOTHING`, key, pair, side, price, quantity, dryRun)
	return result.RowsAffected() == 1, err
}

func (store *PostgresStore) AttachOrder(ctx context.Context, key, orderID string) error {
	result, err := store.pool.Exec(ctx, `UPDATE market_maker_command_log SET order_id=$2 WHERE idempotency_key=$1 AND order_id IS NULL AND dry_run=FALSE`, key, orderID)
	if err == nil && result.RowsAffected() != 1 {
		return ErrRiskLimit
	}
	return err
}

func (store *PostgresStore) RecordDecision(ctx context.Context, decision Decision) error {
	_, err := store.pool.Exec(ctx, `UPDATE market_maker_risk_state SET last_reference_price_atomic=NULLIF($2,'')::numeric,
		last_decision=$3,last_reason=$4,updated_at=$5 WHERE pair=$1`, decision.Pair, decision.ReferencePriceAtomic, decision.Status, decision.Reason, decision.At)
	return err
}
