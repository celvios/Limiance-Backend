// staging-test-money is an intentionally narrow operational command. It has no
// HTTP route and cannot run mutating actions outside staging.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/limiance/backend/internal/config"
	"github.com/limiance/backend/internal/marketdata"
	"github.com/limiance/backend/internal/platform/database"
	"github.com/limiance/backend/internal/testmoney"
)

type options struct {
	action, actorEmail, operatorEmail, approverEmail string
	recipients, assets, recipientEmail, assetSymbol  string
	amountAtomic, reason, idempotencyKey, requestID  string
	confirmStaging                                   bool
}

type inspection struct {
	Enabled               bool              `json:"enabled"`
	WithdrawalLimitsReady bool              `json:"withdrawal_limits_ready"`
	Environment           string            `json:"environment"`
	PolicyID              string            `json:"policy_id,omitempty"`
	Policy                json.RawMessage   `json:"policy,omitempty"`
	Recipients            []recipientRecord `json:"recipients"`
	Grants                []grantRecord     `json:"grants"`
}

type recipientRecord struct {
	Email   string `json:"email"`
	UserID  string `json:"user_id"`
	Enabled bool   `json:"enabled"`
}

type grantRecord struct {
	RequestID       string `json:"request_id"`
	RecipientEmail  string `json:"recipient_email"`
	Asset           string `json:"asset"`
	AmountAtomic    string `json:"amount_atomic"`
	ValueUSDTAtomic string `json:"value_usdt_atomic"`
	CreatedAt       string `json:"created_at"`
}

func main() {
	opts := parseOptions()
	cfg := config.Load()
	if cfg.Environment != "staging" {
		fatal(errors.New("command is restricted to APP_ENV=staging"))
	}
	if requiresConfirmation(opts.action) && !opts.confirmStaging {
		fatal(errors.New("mutating actions require --confirm-staging"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		fatal(err)
	}
	defer pool.Close()

	source, err := referenceSource(pool, cfg)
	if err != nil {
		fatal(err)
	}
	service := testmoney.NewService(pool, cfg.Environment, source)
	var output any
	switch opts.action {
	case "inspect":
		output, err = inspect(ctx, pool)
	case "prepare":
		output, err = service.ConfigurePilot(ctx, testmoney.PilotConfigInput{
			OperatorEmail: opts.operatorEmail, ApproverEmail: opts.approverEmail,
			RecipientEmails: splitList(opts.recipients), AssetSymbols: splitList(opts.assets),
			Reason: opts.reason, IdempotencyKey: opts.idempotencyKey,
		})
	case "propose":
		var actorID, recipientID, accountID, assetID string
		if actorID, err = userID(ctx, pool, opts.actorEmail); err == nil {
			recipientID, accountID, assetID, err = grantTarget(ctx, pool, opts.recipientEmail, opts.assetSymbol)
		}
		if err == nil {
			output, err = service.Propose(ctx, actorID, testmoney.Input{RecipientID: recipientID, AccountID: accountID, AssetID: assetID, AmountAtomic: opts.amountAtomic, Reason: opts.reason, IdempotencyKey: opts.idempotencyKey})
		}
	case "approve":
		var actorID string
		if actorID, err = userID(ctx, pool, opts.actorEmail); err == nil {
			output, err = service.Approve(ctx, actorID, opts.requestID, opts.idempotencyKey)
		}
	default:
		err = errors.New("action must be inspect, prepare, propose, or approve")
	}
	if err != nil {
		fatal(err)
	}
	if err = json.NewEncoder(os.Stdout).Encode(output); err != nil {
		fatal(err)
	}
}

func parseOptions() options {
	var opts options
	flag.StringVar(&opts.action, "action", "inspect", "inspect, prepare, propose, or approve")
	flag.BoolVar(&opts.confirmStaging, "confirm-staging", false, "explicit staging mutation acknowledgement")
	flag.StringVar(&opts.actorEmail, "actor-email", "", "proposer or approver email")
	flag.StringVar(&opts.operatorEmail, "operator-email", "", "treasury operator email for pilot preparation")
	flag.StringVar(&opts.approverEmail, "approver-email", "", "distinct treasury approver email for pilot preparation")
	flag.StringVar(&opts.recipients, "recipients", "", "comma-separated named recipient emails")
	flag.StringVar(&opts.assets, "assets", "", "comma-separated enabled internal_spot asset symbols")
	flag.StringVar(&opts.recipientEmail, "recipient-email", "", "named grant recipient email")
	flag.StringVar(&opts.assetSymbol, "asset", "", "enabled internal_spot asset symbol")
	flag.StringVar(&opts.amountAtomic, "amount-atomic", "", "exact integer asset amount")
	flag.StringVar(&opts.reason, "reason", "", "reviewed operational reason")
	flag.StringVar(&opts.idempotencyKey, "idempotency-key", "", "payload-bound operation key")
	flag.StringVar(&opts.requestID, "request-id", "", "issuance request ID to approve")
	flag.Parse()
	opts.action = strings.ToLower(strings.TrimSpace(opts.action))
	return opts
}

func requiresConfirmation(action string) bool { return action != "inspect" }

func splitList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	return strings.Split(raw, ",")
}

func referenceSource(pool *pgxpool.Pool, cfg config.Config) (*testmoney.SpotReferenceSource, error) {
	bybit, err := marketdata.NewBybit(cfg.BybitMarketDataBaseURL, nil)
	if err != nil {
		return nil, err
	}
	binance, err := marketdata.NewBinance(cfg.BinanceMarketDataBaseURL, nil)
	if err != nil {
		return nil, err
	}
	coinbase, err := marketdata.NewCoinbase(cfg.CoinbaseMarketDataBaseURL, nil)
	if err != nil {
		return nil, err
	}
	kraken, err := marketdata.NewKraken(cfg.KrakenMarketDataBaseURL, nil)
	if err != nil {
		return nil, err
	}
	gate, err := marketdata.NewGate(cfg.GateMarketDataBaseURL, nil)
	if err != nil {
		return nil, err
	}
	lookup := func(ctx context.Context, assetID string) (string, string, error) {
		var symbol, network string
		err := pool.QueryRow(ctx, `SELECT symbol,network FROM assets WHERE id=$1 AND status='enabled'`, assetID).Scan(&symbol, &network)
		return symbol, network, err
	}
	return testmoney.NewSpotReferenceSource(lookup, []testmoney.NamedSpotProvider{{Name: "bybit", Provider: bybit}, {Name: "binance", Provider: binance}, {Name: "coinbase", Provider: coinbase}, {Name: "kraken", Provider: kraken}, {Name: "gate", Provider: gate}}), nil
}

func userID(ctx context.Context, pool *pgxpool.Pool, email string) (string, error) {
	var id string
	err := pool.QueryRow(ctx, `SELECT id::text FROM users WHERE lower(email)=lower($1) AND status='active'`, strings.TrimSpace(email)).Scan(&id)
	if err == pgx.ErrNoRows {
		return "", testmoney.ErrRole
	}
	return id, err
}

func grantTarget(ctx context.Context, pool *pgxpool.Pool, email, symbol string) (user, account, asset string, err error) {
	email, symbol = strings.TrimSpace(email), strings.ToUpper(strings.TrimSpace(symbol))
	err = pool.QueryRow(ctx, `SELECT u.id::text,min(a.id::text) FROM users u JOIN accounts a ON a.user_id=u.id
	 WHERE lower(u.email)=lower($1) AND u.status='active' AND a.status='active' AND a.kind='uta'
	 GROUP BY u.id HAVING count(*)=1`, email).Scan(&user, &account)
	if err == pgx.ErrNoRows {
		return "", "", "", testmoney.ErrRecipient
	}
	if err != nil {
		return "", "", "", err
	}
	err = pool.QueryRow(ctx, `SELECT id::text FROM assets WHERE symbol=$1 AND network='internal_spot' AND status='enabled'`, symbol).Scan(&asset)
	if err == pgx.ErrNoRows {
		return "", "", "", testmoney.ErrInput
	}
	return user, account, asset, err
}

func inspect(ctx context.Context, pool *pgxpool.Pool) (inspection, error) {
	result := inspection{Recipients: []recipientRecord{}, Grants: []grantRecord{}}
	if err := pool.QueryRow(ctx, `SELECT c.enabled,c.withdrawal_limits_ready,c.environment,COALESCE(c.policy_id::text,''),COALESCE(p.policy,'{}'::jsonb)
	 FROM test_money_control c LEFT JOIN test_money_policies p ON p.id=c.policy_id WHERE c.singleton=TRUE`).Scan(&result.Enabled, &result.WithdrawalLimitsReady, &result.Environment, &result.PolicyID, &result.Policy); err != nil {
		return result, err
	}
	rows, err := pool.Query(ctx, `SELECT u.email::text,u.id::text,r.enabled FROM test_money_recipients r JOIN users u ON u.id=r.user_id ORDER BY lower(u.email::text)`)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var item recipientRecord
		if err = rows.Scan(&item.Email, &item.UserID, &item.Enabled); err != nil {
			rows.Close()
			return result, err
		}
		result.Recipients = append(result.Recipients, item)
	}
	rows.Close()
	rows, err = pool.Query(ctx, `SELECT r.id::text,u.email::text,a.symbol,r.amount_atomic::text,g.value_usdt_atomic::text,g.created_at::text
	 FROM test_money_grants g JOIN test_money_requests r ON r.id=g.request_id JOIN users u ON u.id=r.recipient_id JOIN assets a ON a.id=r.asset_id ORDER BY g.created_at`)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var item grantRecord
		if err = rows.Scan(&item.RequestID, &item.RecipientEmail, &item.Asset, &item.AmountAtomic, &item.ValueUSDTAtomic, &item.CreatedAt); err != nil {
			return result, err
		}
		result.Grants = append(result.Grants, item)
	}
	return result, rows.Err()
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "staging test-money command failed:", err)
	os.Exit(1)
}
