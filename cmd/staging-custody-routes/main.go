// staging-custody-routes exposes a narrow, staging-only maker-checker command
// for inspecting, proposing and approving custody withdrawal routes.
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
	"github.com/limiance/backend/internal/custody"
	"github.com/limiance/backend/internal/datamanager"
	"github.com/limiance/backend/internal/platform/database"
)

type options struct {
	action, actorEmail, asset, network, feeAsset, maxFee, reason, idempotencyKey, requestID string
	observationAge, confirmStaging                                                          int
}

type routeAsset struct {
	ID             string `json:"asset_id"`
	Symbol         string `json:"symbol"`
	Network        string `json:"network"`
	ProviderAsset  string `json:"provider_asset_id"`
	Decimals       int16  `json:"decimals"`
	LatestAction   string `json:"latest_action,omitempty"`
	LatestApproval string `json:"latest_approval,omitempty"`
}

type commandResult struct {
	Action    string       `json:"action"`
	Assets    []routeAsset `json:"eligible_assets,omitempty"`
	RequestID string       `json:"request_id,omitempty"`
	RouteID   string       `json:"route_id,omitempty"`
	Duplicate bool         `json:"duplicate,omitempty"`
}

func main() {
	var opt options
	flag.StringVar(&opt.action, "action", "inspect", "inspect, propose, or approve")
	flag.StringVar(&opt.actorEmail, "actor-email", "", "active operator or approver email")
	flag.StringVar(&opt.asset, "asset", "", "withdrawal asset symbol")
	flag.StringVar(&opt.network, "network", "", "exact testnet network")
	flag.StringVar(&opt.feeAsset, "fee-asset", "", "native network fee asset symbol")
	flag.StringVar(&opt.maxFee, "max-fee", "", "maximum provider fee in exact decimal units")
	flag.IntVar(&opt.observationAge, "max-observation-age-seconds", 15, "maximum custody observation age")
	flag.StringVar(&opt.reason, "reason", "", "audited reason")
	flag.StringVar(&opt.idempotencyKey, "idempotency-key", "", "payload-bound idempotency key")
	flag.StringVar(&opt.requestID, "request-id", "", "proposal request UUID")
	confirm := flag.Bool("confirm-staging", false, "confirm a staging mutation")
	flag.Parse()
	if *confirm {
		opt.confirmStaging = 1
	}
	opt.action = strings.ToLower(strings.TrimSpace(opt.action))
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

	result := commandResult{Action: opt.action}
	switch opt.action {
	case "inspect":
		result.Assets, err = inspectAssets(ctx, pool)
	case "propose":
		var actorID string
		actorID, err = resolveActor(ctx, pool, opt.actorEmail, "treasury_operator")
		if err == nil {
			var asset, fee routeAsset
			asset, err = resolveAsset(ctx, pool, opt.asset, opt.network)
			if err == nil {
				fee, err = resolveAsset(ctx, pool, opt.feeAsset, opt.network)
			}
			if err == nil {
				var maxFeeAtomic string
				maxFeeAtomic, err = custody.ProviderAmountToAtomic(opt.maxFee, fee.Decimals)
				if err == nil {
					var proposal datamanager.CustodyWithdrawalRouteRequest
					proposal, err = datamanager.New(pool).ProposeCustodyWithdrawalRoute(ctx, actorID, datamanager.CustodyWithdrawalRouteInput{
						AssetID: asset.ID, Provider: "fireblocks", Environment: "staging", Network: asset.Network,
						ProviderAssetID: asset.ProviderAsset, AssetDecimals: asset.Decimals,
						FeeProviderAssetID: fee.ProviderAsset, FeeAssetDecimals: fee.Decimals,
						MaxFeeAtomic: maxFeeAtomic, MaxObservationAgeSeconds: opt.observationAge,
						Reason: opt.reason, IdempotencyKey: opt.idempotencyKey,
					})
					result.RequestID, result.RouteID, result.Duplicate = proposal.RequestID, proposal.RouteID, proposal.Duplicate
				}
			}
		}
	case "approve":
		var actorID string
		actorID, err = resolveActor(ctx, pool, opt.actorEmail, "treasury_approver")
		if err == nil {
			var approval datamanager.CustodyWithdrawalRouteRequest
			approval, err = datamanager.New(pool).ApproveCustodyWithdrawalRoute(ctx, actorID, opt.requestID, opt.reason, opt.idempotencyKey)
			result.RequestID, result.RouteID, result.Duplicate = approval.RequestID, approval.RouteID, approval.Duplicate
		}
	}
	if err != nil {
		fatal(err)
	}
	if err = json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fatal(err)
	}
}

func validateOptions(opt options) error {
	if opt.action == "inspect" {
		return nil
	}
	if opt.confirmStaging != 1 || strings.TrimSpace(opt.actorEmail) == "" || len(strings.TrimSpace(opt.reason)) < 8 || len(opt.reason) > 500 || strings.TrimSpace(opt.idempotencyKey) == "" {
		return errors.New("mutation requires --confirm-staging, actor, reason, and idempotency key")
	}
	if opt.action == "propose" {
		if strings.TrimSpace(opt.asset) == "" || strings.TrimSpace(opt.network) == "" || strings.TrimSpace(opt.feeAsset) == "" || strings.TrimSpace(opt.maxFee) == "" || opt.observationAge < 1 || opt.observationAge > 30 {
			return errors.New("proposal requires exact asset, network, fee asset, max fee, and observation age")
		}
		return nil
	}
	if opt.action == "approve" && strings.TrimSpace(opt.requestID) != "" {
		return nil
	}
	return errors.New("action must be inspect, propose, or approve")
}

func inspectAssets(ctx context.Context, pool *pgxpool.Pool) ([]routeAsset, error) {
	rows, err := pool.Query(ctx, `SELECT a.id::text,a.symbol,a.network,a.custody_asset_id,a.decimals,
		COALESCE(latest.action,''),COALESCE(latest.approved_at::text,'')
		FROM assets a LEFT JOIN LATERAL (
			SELECT q.action,p.approved_at FROM custody_withdrawal_routes r
			JOIN custody_withdrawal_route_requests q ON q.route_id=r.id
			JOIN custody_withdrawal_route_approvals p ON p.request_id=q.id
			WHERE r.asset_id=a.id ORDER BY p.approved_at DESC,p.request_id DESC LIMIT 1
		) latest ON TRUE
		WHERE a.status='enabled' AND a.network IN ('ethereum_sepolia','bitcoin_testnet4')
		AND a.custody_asset_id<>'' ORDER BY a.network,a.symbol,a.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []routeAsset{}
	for rows.Next() {
		var item routeAsset
		if err = rows.Scan(&item.ID, &item.Symbol, &item.Network, &item.ProviderAsset, &item.Decimals, &item.LatestAction, &item.LatestApproval); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func resolveActor(ctx context.Context, pool *pgxpool.Pool, email, role string) (string, error) {
	var id string
	err := pool.QueryRow(ctx, `SELECT u.id::text FROM users u JOIN user_roles r ON r.user_id=u.id
		WHERE lower(u.email)=lower($1) AND u.status='active' AND r.role=$2`, strings.TrimSpace(email), role).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errors.New("active actor with required role was not found")
	}
	return id, err
}

func resolveAsset(ctx context.Context, pool *pgxpool.Pool, symbol, network string) (routeAsset, error) {
	rows, err := pool.Query(ctx, `SELECT id::text,symbol,network,custody_asset_id,decimals FROM assets
		WHERE upper(symbol)=upper($1) AND network=$2 AND status='enabled' AND custody_asset_id<>''
		ORDER BY id`, strings.TrimSpace(symbol), strings.ToLower(strings.TrimSpace(network)))
	if err != nil {
		return routeAsset{}, err
	}
	defer rows.Close()
	items := []routeAsset{}
	for rows.Next() {
		var item routeAsset
		if err = rows.Scan(&item.ID, &item.Symbol, &item.Network, &item.ProviderAsset, &item.Decimals); err != nil {
			return routeAsset{}, err
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return routeAsset{}, err
	}
	if len(items) != 1 {
		return routeAsset{}, errors.New("asset and network must resolve to exactly one enabled custody asset")
	}
	return items[0], nil
}

func fatal(err error) {
	_, _ = fmt.Fprintln(os.Stderr, "staging custody route command failed:", err)
	os.Exit(1)
}
