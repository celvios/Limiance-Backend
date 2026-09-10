// staging-readiness-audit emits a sanitized, read-only report of the controls
// that must remain independently verifiable before any market-maker release.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/limiance/backend/internal/config"
	"github.com/limiance/backend/internal/platform/database"
)

var auditedEmails = []string{
	"toluking001@gmail.com",
	"favourtolu57@gmail.com",
	"internal-market-maker@staging.celvios.site",
}

type identity struct {
	Email  string   `json:"email"`
	UserID string   `json:"user_id"`
	Status string   `json:"status"`
	Roles  []string `json:"roles"`
}

type operationalControl struct {
	Key       string `json:"key"`
	Enabled   bool   `json:"enabled"`
	UpdatedAt string `json:"updated_at"`
}

type testMoneyControl struct {
	Enabled               bool   `json:"enabled"`
	WithdrawalLimitsReady bool   `json:"withdrawal_limits_ready"`
	Environment           string `json:"environment"`
	PolicyID              string `json:"policy_id"`
	GrantCount            int    `json:"grant_count"`
	PendingRequestCount   int    `json:"pending_request_count"`
}

type marketMakerControl struct {
	Enabled              bool   `json:"enabled"`
	DryRun               bool   `json:"dry_run"`
	KillSwitch           bool   `json:"kill_switch"`
	UserID               string `json:"user_id"`
	AccountID            string `json:"account_id"`
	EnabledConfigs       int    `json:"enabled_configs"`
	ActivePairs          int    `json:"active_pairs"`
	InventoryJournals    int    `json:"inventory_journals"`
	OpenOrders           int    `json:"open_orders"`
	CommandCount         int    `json:"command_count"`
	LiveCommandCount     int    `json:"live_command_count"`
	LatestStopAt         string `json:"latest_stop_at"`
	NextActivationAt     string `json:"next_activation_at"`
	CommandsDuringStop   int    `json:"commands_during_stop"`
	CommandCessationSeen bool   `json:"command_cessation_verified"`
}

type custodyRoute struct {
	Asset                string `json:"asset"`
	Network              string `json:"network"`
	Provider             string `json:"provider"`
	Environment          string `json:"environment"`
	ProviderAssetID      string `json:"provider_asset_id"`
	FeeProviderAssetID   string `json:"fee_provider_asset_id"`
	MaxFeeAtomic         string `json:"max_fee_atomic"`
	MaxObservationAgeSec int    `json:"max_observation_age_seconds"`
	LatestAction         string `json:"latest_action"`
	ApprovedAt           string `json:"approved_at"`
}

type statusCount struct {
	Status string `json:"status"`
	Count  int    `json:"count"`
}

type report struct {
	GeneratedAt         string               `json:"generated_at"`
	Environment         string               `json:"environment"`
	Identities          []identity           `json:"identities"`
	OperationalControls []operationalControl `json:"operational_controls"`
	TestMoney           testMoneyControl     `json:"test_money"`
	MarketMaker         marketMakerControl   `json:"market_maker"`
	CustodyRoutes       []custodyRoute       `json:"custody_withdrawal_routes"`
	PairStatuses        []statusCount        `json:"pair_statuses"`
	WithdrawalStatuses  []statusCount        `json:"withdrawal_statuses"`
	DispatchStatuses    []statusCount        `json:"dispatch_statuses"`
	OpenReservations    int                  `json:"open_capacity_reservations"`
	UnprocessedWebhooks int                  `json:"unprocessed_fireblocks_receipts"`
}

func main() {
	cfg := config.Load()
	if cfg.Environment != "staging" {
		fatal(fmt.Errorf("command is restricted to staging"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		fatal(err)
	}
	defer pool.Close()

	out := report{GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano), Environment: cfg.Environment}
	for _, email := range auditedEmails {
		item := identity{Email: email, Roles: []string{}}
		err = pool.QueryRow(ctx, `SELECT u.id::text,u.status,
			COALESCE(array_agg(r.role ORDER BY r.role) FILTER(WHERE r.role IS NOT NULL),'{}'::text[])
			FROM users u LEFT JOIN user_roles r ON r.user_id=u.id
			WHERE lower(u.email)=lower($1) GROUP BY u.id,u.status`, email).
			Scan(&item.UserID, &item.Status, &item.Roles)
		if err != nil {
			fatal(err)
		}
		out.Identities = append(out.Identities, item)
	}

	rows, err := pool.Query(ctx, `SELECT control_key,enabled,updated_at::text FROM operational_controls ORDER BY control_key`)
	if err != nil {
		fatal(err)
	}
	for rows.Next() {
		var item operationalControl
		if err = rows.Scan(&item.Key, &item.Enabled, &item.UpdatedAt); err != nil {
			fatal(err)
		}
		out.OperationalControls = append(out.OperationalControls, item)
	}
	rows.Close()

	err = pool.QueryRow(ctx, `SELECT c.enabled,c.withdrawal_limits_ready,c.environment,
		COALESCE(c.policy_id::text,''),(SELECT count(*)::int FROM test_money_grants),
		(SELECT count(*)::int FROM test_money_requests r WHERE NOT EXISTS
			(SELECT 1 FROM test_money_grants g WHERE g.request_id=r.id))
		FROM test_money_control c WHERE c.singleton=TRUE`).Scan(
		&out.TestMoney.Enabled, &out.TestMoney.WithdrawalLimitsReady, &out.TestMoney.Environment,
		&out.TestMoney.PolicyID, &out.TestMoney.GrantCount, &out.TestMoney.PendingRequestCount)
	if err != nil {
		fatal(err)
	}

	err = pool.QueryRow(ctx, `SELECT enabled,dry_run,kill_switch,COALESCE(user_id::text,''),
		COALESCE(account_id::text,'') FROM market_maker_control WHERE singleton=TRUE`).Scan(
		&out.MarketMaker.Enabled, &out.MarketMaker.DryRun, &out.MarketMaker.KillSwitch,
		&out.MarketMaker.UserID, &out.MarketMaker.AccountID)
	if err != nil {
		fatal(err)
	}
	err = pool.QueryRow(ctx, `SELECT
		(SELECT count(*)::int FROM market_maker_configs WHERE enabled),
		(SELECT count(*)::int FROM trading_pairs WHERE status='active'),
		(SELECT count(*)::int FROM market_maker_inventory_journals),
		(SELECT count(*)::int FROM orders WHERE account_id=NULLIF($1,'')::uuid
			AND status IN ('CONDITIONAL','PENDING','PENDING_CANCEL','OPEN','PARTIALLY_FILLED')),
		(SELECT count(*)::int FROM market_maker_command_log),
		(SELECT count(*)::int FROM market_maker_command_log WHERE NOT dry_run)`, out.MarketMaker.AccountID).Scan(
		&out.MarketMaker.EnabledConfigs, &out.MarketMaker.ActivePairs, &out.MarketMaker.InventoryJournals,
		&out.MarketMaker.OpenOrders, &out.MarketMaker.CommandCount, &out.MarketMaker.LiveCommandCount)
	if err != nil {
		fatal(err)
	}
	err = pool.QueryRow(ctx, `WITH stop AS (
		SELECT max(created_at) AS at FROM audit_events WHERE action='market_maker.emergency_stopped'
	), resumed AS (
		SELECT min(created_at) AS at FROM audit_events,stop
		WHERE action='market_maker.activation_approved' AND created_at>stop.at
	) SELECT COALESCE(stop.at::text,''),COALESCE(resumed.at::text,''),
		(SELECT count(*)::int FROM market_maker_command_log c
		 WHERE stop.at IS NOT NULL AND c.created_at>stop.at
		 AND (resumed.at IS NULL OR c.created_at<resumed.at)) FROM stop,resumed`).Scan(
		&out.MarketMaker.LatestStopAt, &out.MarketMaker.NextActivationAt, &out.MarketMaker.CommandsDuringStop)
	if err != nil {
		fatal(err)
	}
	out.MarketMaker.CommandCessationSeen = commandCessationVerified(out.MarketMaker)

	rows, err = pool.Query(ctx, `SELECT s.symbol,r.network,r.provider,r.environment,r.provider_asset_id,
		r.fee_provider_asset_id,r.max_fee_atomic::text,r.max_observation_age_seconds,
		COALESCE(latest.action,''),COALESCE(latest.approved_at::text,'')
		FROM custody_withdrawal_routes r JOIN assets s ON s.id=r.asset_id
		LEFT JOIN LATERAL (SELECT q.action,a.approved_at FROM custody_withdrawal_route_requests q
			JOIN custody_withdrawal_route_approvals a ON a.request_id=q.id
			WHERE q.route_id=r.id ORDER BY a.approved_at DESC,a.request_id DESC LIMIT 1) latest ON TRUE
		ORDER BY s.symbol,r.network,r.provider,r.created_at`)
	if err != nil {
		fatal(err)
	}
	for rows.Next() {
		var item custodyRoute
		if err = rows.Scan(&item.Asset, &item.Network, &item.Provider, &item.Environment,
			&item.ProviderAssetID, &item.FeeProviderAssetID, &item.MaxFeeAtomic,
			&item.MaxObservationAgeSec, &item.LatestAction, &item.ApprovedAt); err != nil {
			fatal(err)
		}
		out.CustodyRoutes = append(out.CustodyRoutes, item)
	}
	rows.Close()

	out.PairStatuses = loadStatusCounts(ctx, pool, `SELECT status,count(*)::int FROM trading_pairs GROUP BY status ORDER BY status`)
	out.WithdrawalStatuses = loadStatusCounts(ctx, pool, `SELECT status,count(*)::int FROM withdrawals GROUP BY status ORDER BY status`)
	out.DispatchStatuses = loadStatusCounts(ctx, pool, `SELECT status,count(*)::int FROM withdrawal_dispatches GROUP BY status ORDER BY status`)
	err = pool.QueryRow(ctx, `SELECT count(*)::int FROM custody_capacity_reservations r
		WHERE NOT EXISTS(SELECT 1 FROM custody_capacity_terminal_events t WHERE t.withdrawal_id=r.withdrawal_id)`).Scan(&out.OpenReservations)
	if err != nil {
		fatal(err)
	}
	err = pool.QueryRow(ctx, `SELECT count(*)::int FROM webhook_receipts
		WHERE provider='fireblocks' AND processed_at IS NULL`).Scan(&out.UnprocessedWebhooks)
	if err != nil {
		fatal(err)
	}

	if err = json.NewEncoder(os.Stdout).Encode(out); err != nil {
		fatal(err)
	}
}

// loadStatusCounts uses the concrete database pool at the call site; keeping
// the scanner local makes the audit command unable to mutate state.
func loadStatusCounts(ctx context.Context, pool *pgxpool.Pool, query string) []statusCount {
	rows, err := pool.Query(ctx, query)
	if err != nil {
		fatal(err)
	}
	defer rows.Close()
	items := []statusCount{}
	for rows.Next() {
		var item statusCount
		if err = rows.Scan(&item.Status, &item.Count); err != nil {
			fatal(err)
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		fatal(err)
	}
	return items
}

func commandCessationVerified(control marketMakerControl) bool {
	return control.LatestStopAt != "" && control.CommandsDuringStop == 0
}

func fatal(err error) {
	_, _ = fmt.Fprintln(os.Stderr, "staging readiness audit failed:", err)
	os.Exit(1)
}
