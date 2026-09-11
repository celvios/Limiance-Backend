// staging-market-maker-inventory issues bounded, non-withdrawable staging
// inventory to the internal treasury through a maker-checker workflow.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/limiance/backend/internal/config"
	"github.com/limiance/backend/internal/marketdata"
	"github.com/limiance/backend/internal/marketmaker"
	"github.com/limiance/backend/internal/platform/database"
	"github.com/limiance/backend/internal/testmoney"
)

type options struct {
	action, actorEmail, asset, amountAtomic, limitUSDTAtomic, reason, idempotencyKey, requestID string
	confirmStaging                                                                              bool
}

type inventoryRow struct {
	RequestID, Asset, AmountAtomic, ValueUSDTAtomic, JournalID, ApprovedAt string
}

type inspection struct {
	TreasuryAccountID     string         `json:"treasury_account_id,omitempty"`
	LimitPolicyID         string         `json:"limit_policy_id"`
	LimitVersion          int64          `json:"limit_version"`
	LimitUSDTAtomic       string         `json:"limit_usdt_atomic"`
	IssuedValueUSDTAtomic string         `json:"issued_value_usdt_atomic"`
	Grants                []inventoryRow `json:"grants"`
}

func main() {
	opt := parseOptions()
	if err := validateOptions(opt); err != nil {
		fatal(err)
	}
	cfg := config.Load()
	if cfg.Environment != "staging" {
		fatal(errors.New("command is restricted to staging"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		fatal(err)
	}
	defer pool.Close()
	references, err := referenceSource(pool, cfg)
	if err != nil {
		fatal(err)
	}
	service := marketmaker.NewTreasuryInventoryService(pool, cfg.Environment, references)
	var output any
	switch opt.action {
	case "inspect":
		output, err = inspect(ctx, pool)
	case "propose":
		var actorID, assetID string
		actorID, err = resolveActor(ctx, pool, opt.actorEmail, "treasury_operator")
		if err == nil {
			assetID, err = resolveAsset(ctx, pool, opt.asset)
		}
		if err == nil {
			output, err = service.Propose(ctx, actorID, marketmaker.TreasuryInventoryInput{AssetID: assetID, AmountAtomic: opt.amountAtomic, Reason: opt.reason, IdempotencyKey: opt.idempotencyKey})
		}
	case "approve":
		var actorID string
		actorID, err = resolveActor(ctx, pool, opt.actorEmail, "treasury_approver")
		if err == nil {
			output, err = service.Approve(ctx, actorID, opt.requestID, opt.idempotencyKey)
		}
	case "propose-limit":
		var actorID string
		actorID, err = resolveActor(ctx, pool, opt.actorEmail, "treasury_operator")
		if err == nil {
			output, err = service.ProposeLimit(ctx, actorID, marketmaker.TreasuryLimitInput{LimitUSDTAtomic: opt.limitUSDTAtomic, Reason: opt.reason, IdempotencyKey: opt.idempotencyKey})
		}
	case "approve-limit":
		var actorID string
		actorID, err = resolveActor(ctx, pool, opt.actorEmail, "treasury_approver")
		if err == nil {
			output, err = service.ApproveLimit(ctx, actorID, opt.requestID, opt.idempotencyKey)
		}
	}
	if err != nil {
		fatal(err)
	}
	if err = json.NewEncoder(os.Stdout).Encode(output); err != nil {
		fatal(err)
	}
}

func parseOptions() options {
	var o options
	flag.StringVar(&o.action, "action", "inspect", "inspect, propose, approve, propose-limit, or approve-limit")
	flag.StringVar(&o.actorEmail, "actor-email", "", "operator or approver email")
	flag.StringVar(&o.asset, "asset", "", "internal spot symbol")
	flag.StringVar(&o.amountAtomic, "amount-atomic", "", "exact integer amount")
	flag.StringVar(&o.limitUSDTAtomic, "limit-usdt-atomic", "", "exact aggregate USDT-scale-8 ceiling")
	flag.StringVar(&o.reason, "reason", "", "audited reason")
	flag.StringVar(&o.idempotencyKey, "idempotency-key", "", "payload-bound key")
	flag.StringVar(&o.requestID, "request-id", "", "proposal UUID")
	flag.BoolVar(&o.confirmStaging, "confirm-staging", false, "confirm staging mutation")
	flag.Parse()
	o.action = strings.ToLower(strings.TrimSpace(o.action))
	return o
}

func validateOptions(o options) error {
	switch o.action {
	case "inspect":
		return nil
	case "propose":
		if !o.confirmStaging || o.actorEmail == "" || o.asset == "" || o.amountAtomic == "" || len(strings.TrimSpace(o.reason)) < 8 || o.idempotencyKey == "" {
			return errors.New("proposal requires confirmation, actor, asset, amount, reason and idempotency key")
		}
	case "approve":
		if !o.confirmStaging || o.actorEmail == "" || o.requestID == "" || o.idempotencyKey == "" {
			return errors.New("approval requires confirmation, actor, request and idempotency key")
		}
	case "propose-limit":
		if !o.confirmStaging || o.actorEmail == "" || o.limitUSDTAtomic == "" || len(strings.TrimSpace(o.reason)) < 8 || o.idempotencyKey == "" {
			return errors.New("limit proposal requires confirmation, actor, exact limit, reason and idempotency key")
		}
	case "approve-limit":
		if !o.confirmStaging || o.actorEmail == "" || o.requestID == "" || o.idempotencyKey == "" {
			return errors.New("limit approval requires confirmation, actor, request and idempotency key")
		}
	default:
		return errors.New("unsupported action")
	}
	return nil
}

func resolveActor(ctx context.Context, pool *pgxpool.Pool, email, role string) (string, error) {
	var id string
	err := pool.QueryRow(ctx, `SELECT u.id::text FROM users u JOIN user_roles r ON r.user_id=u.id WHERE lower(u.email)=lower($1) AND u.status='active' AND r.role=$2`, strings.TrimSpace(email), role).Scan(&id)
	return id, err
}
func resolveAsset(ctx context.Context, pool *pgxpool.Pool, symbol string) (string, error) {
	var id string
	err := pool.QueryRow(ctx, `SELECT id::text FROM assets WHERE upper(symbol)=upper($1) AND network='internal_spot' AND status='enabled'`, strings.TrimSpace(symbol)).Scan(&id)
	return id, err
}

func referenceSource(pool *pgxpool.Pool, cfg config.Config) (*testmoney.SpotReferenceSource, error) {
	bybit, e := marketdata.NewBybit(cfg.BybitMarketDataBaseURL, nil)
	if e != nil {
		return nil, e
	}
	binance, e := marketdata.NewBinance(cfg.BinanceMarketDataBaseURL, nil)
	if e != nil {
		return nil, e
	}
	coinbase, e := marketdata.NewCoinbase(cfg.CoinbaseMarketDataBaseURL, nil)
	if e != nil {
		return nil, e
	}
	kraken, e := marketdata.NewKraken(cfg.KrakenMarketDataBaseURL, nil)
	if e != nil {
		return nil, e
	}
	gate, e := marketdata.NewGate(cfg.GateMarketDataBaseURL, nil)
	if e != nil {
		return nil, e
	}
	lookup := func(ctx context.Context, id string) (string, string, error) {
		var symbol, network string
		e := pool.QueryRow(ctx, `SELECT symbol,network FROM assets WHERE id=$1 AND status='enabled'`, id).Scan(&symbol, &network)
		return symbol, network, e
	}
	return testmoney.NewSpotReferenceSource(lookup, []testmoney.NamedSpotProvider{{Name: "bybit", Provider: bybit}, {Name: "binance", Provider: binance}, {Name: "coinbase", Provider: coinbase}, {Name: "kraken", Provider: kraken}, {Name: "gate", Provider: gate}}), nil
}

func inspect(ctx context.Context, pool *pgxpool.Pool) (inspection, error) {
	out := inspection{Grants: []inventoryRow{}}
	_ = pool.QueryRow(ctx, `SELECT id::text FROM accounts WHERE kind='system' AND name='staging-market-maker-treasury'`).Scan(&out.TreasuryAccountID)
	if err := pool.QueryRow(ctx, `SELECT p.id::text,p.version,p.limit_usdt_atomic::text FROM staging_market_maker_treasury_limit_control c JOIN staging_market_maker_treasury_limit_policies p ON p.id=c.policy_id WHERE c.singleton`).Scan(&out.LimitPolicyID, &out.LimitVersion, &out.LimitUSDTAtomic); err != nil {
		return out, err
	}
	rows, err := pool.Query(ctx, `SELECT r.id::text,a.symbol,r.amount_atomic::text,g.value_usdt_atomic::text,g.journal_id::text,g.created_at::text FROM staging_market_maker_treasury_grants g JOIN staging_market_maker_treasury_requests r ON r.id=g.request_id JOIN assets a ON a.id=r.asset_id ORDER BY a.symbol`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	total := new(big.Int)
	for rows.Next() {
		var item inventoryRow
		if err = rows.Scan(&item.RequestID, &item.Asset, &item.AmountAtomic, &item.ValueUSDTAtomic, &item.JournalID, &item.ApprovedAt); err != nil {
			return out, err
		}
		v, _ := new(big.Int).SetString(item.ValueUSDTAtomic, 10)
		total.Add(total, v)
		out.Grants = append(out.Grants, item)
	}
	out.IssuedValueUSDTAtomic = total.String()
	return out, rows.Err()
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "staging market-maker inventory command failed:", err)
	os.Exit(1)
}
