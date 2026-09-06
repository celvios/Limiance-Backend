package datamanager

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrWithdrawalCapacityRouteUnavailable = errors.New("approved custody capacity route is unavailable")
var ErrWithdrawalCustodyCapacity = errors.New("insufficient custody token or fee capacity")
var ErrWithdrawalCapacityObservation = errors.New("custody capacity observation is invalid or stale")
var ErrWithdrawalCapacityRouteActor = errors.New("custody capacity route actor is not authorized")
var ErrWithdrawalCapacityRouteConflict = errors.New("custody capacity route idempotency conflict")

type CustodyWithdrawalRouteInput struct {
	AssetID                  string
	Provider                 string
	Environment              string
	Network                  string
	ProviderAssetID          string
	AssetDecimals            int16
	FeeProviderAssetID       string
	FeeAssetDecimals         int16
	MaxFeeAtomic             string
	MaxObservationAgeSeconds int
	Reason                   string
	IdempotencyKey           string
}

type CustodyWithdrawalRouteRequest struct {
	RequestID string
	RouteID   string
	Duplicate bool
}

type WithdrawalCapacityRoute struct {
	Required                 bool
	RouteID                  string
	AssetID                  string
	Environment              string
	Provider                 string
	Network                  string
	ProviderAssetID          string
	AssetDecimals            int16
	FeeProviderAssetID       string
	FeeAssetDecimals         int16
	MaxFeeAtomic             string
	MaxObservationAgeSeconds int
}

type WithdrawalCapacityEvidence struct {
	RouteID          string
	Environment      string
	ObservedAt       time.Time
	AssetAvailable   string
	FeeAvailable     string
	FeeRequired      string
	AssetBlockHeight string
	AssetBlockHash   string
	FeeBlockHeight   string
	FeeBlockHash     string
}

func (m *Manager) ProposeCustodyWithdrawalRoute(ctx context.Context, actor string, input CustodyWithdrawalRouteInput) (CustodyWithdrawalRouteRequest, error) {
	if actor == "" || input.AssetID == "" || input.Provider == "" || input.Network == "" || input.ProviderAssetID == "" ||
		input.FeeProviderAssetID == "" || input.IdempotencyKey == "" || len(input.Reason) < 8 || len(input.Reason) > 500 ||
		(input.Environment != "staging" && input.Environment != "production") || input.AssetDecimals < 0 || input.AssetDecimals > 36 ||
		input.FeeAssetDecimals < 0 || input.FeeAssetDecimals > 36 || input.MaxObservationAgeSeconds < 1 || input.MaxObservationAgeSeconds > 30 {
		return CustodyWithdrawalRouteRequest{}, ErrWithdrawalCapacityRouteUnavailable
	}
	if input.ProviderAssetID == input.FeeProviderAssetID && input.AssetDecimals != input.FeeAssetDecimals {
		return CustodyWithdrawalRouteRequest{}, ErrWithdrawalCapacityRouteUnavailable
	}
	if _, err := parseCapacityAtomic(input.MaxFeeAtomic); err != nil {
		return CustodyWithdrawalRouteRequest{}, err
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return CustodyWithdrawalRouteRequest{}, err
	}
	hash := sha256.Sum256(payload)
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return CustodyWithdrawalRouteRequest{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "custody-route-proposal:"+actor+":"+input.IdempotencyKey); err != nil {
		return CustodyWithdrawalRouteRequest{}, err
	}
	var result CustodyWithdrawalRouteRequest
	var same bool
	err = tx.QueryRow(ctx, `SELECT q.id::text,q.route_id::text,q.payload_hash=$3
		FROM custody_withdrawal_route_requests q WHERE q.proposed_by=$1 AND q.idempotency_key=$2`,
		actor, input.IdempotencyKey, hash[:]).Scan(&result.RequestID, &result.RouteID, &same)
	if err == nil {
		if !same {
			return CustodyWithdrawalRouteRequest{}, ErrWithdrawalCapacityRouteConflict
		}
		result.Duplicate = true
		return result, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return CustodyWithdrawalRouteRequest{}, err
	}
	if err = requireCustodyRouteRole(ctx, tx, actor, "treasury_operator"); err != nil {
		return CustodyWithdrawalRouteRequest{}, err
	}
	var network, providerAssetID string
	var decimals int16
	err = tx.QueryRow(ctx, `SELECT network,custody_asset_id,decimals FROM assets
		WHERE id=$1 AND status='enabled' FOR SHARE`, input.AssetID).Scan(&network, &providerAssetID, &decimals)
	if errors.Is(err, pgx.ErrNoRows) {
		return CustodyWithdrawalRouteRequest{}, ErrWithdrawalCapacityRouteUnavailable
	}
	if err != nil {
		return CustodyWithdrawalRouteRequest{}, err
	}
	if network != input.Network || providerAssetID != input.ProviderAssetID || decimals != input.AssetDecimals {
		return CustodyWithdrawalRouteRequest{}, ErrWithdrawalCapacityRouteUnavailable
	}
	err = tx.QueryRow(ctx, `INSERT INTO custody_withdrawal_routes
		(asset_id,provider,environment,network,provider_asset_id,asset_decimals,fee_provider_asset_id,
		 fee_asset_decimals,max_fee_atomic,max_observation_age_seconds,created_by)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9::numeric,$10,$11) RETURNING id::text`, input.AssetID,
		input.Provider, input.Environment, input.Network, input.ProviderAssetID, input.AssetDecimals,
		input.FeeProviderAssetID, input.FeeAssetDecimals, input.MaxFeeAtomic, input.MaxObservationAgeSeconds, actor).Scan(&result.RouteID)
	if err != nil {
		return CustodyWithdrawalRouteRequest{}, err
	}
	err = tx.QueryRow(ctx, `INSERT INTO custody_withdrawal_route_requests
		(route_id,action,proposed_by,reason,idempotency_key,payload_hash)
		VALUES($1,'enable',$2,$3,$4,$5) RETURNING id::text`, result.RouteID, actor, input.Reason,
		input.IdempotencyKey, hash[:]).Scan(&result.RequestID)
	if err != nil {
		return CustodyWithdrawalRouteRequest{}, err
	}
	metadata, _ := json.Marshal(map[string]string{"request_id": result.RequestID, "route_id": result.RouteID})
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(actor_id,actor_type,action,resource_type,resource_id,metadata)
		VALUES($1,'admin','custody.route_proposed','custody_withdrawal_route',$2,$3::jsonb)`, actor, result.RouteID, metadata); err != nil {
		return CustodyWithdrawalRouteRequest{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO outbox_events(event_type,aggregate_type,aggregate_id,payload)
		VALUES('custody.route_proposed','custody_withdrawal_route',$1,$2::jsonb)`, result.RouteID, metadata); err != nil {
		return CustodyWithdrawalRouteRequest{}, err
	}
	return result, tx.Commit(ctx)
}

func (m *Manager) ApproveCustodyWithdrawalRoute(ctx context.Context, actor, requestID, reason, idempotencyKey string) (CustodyWithdrawalRouteRequest, error) {
	if actor == "" || requestID == "" || idempotencyKey == "" || len(reason) < 8 || len(reason) > 500 {
		return CustodyWithdrawalRouteRequest{}, ErrWithdrawalCapacityRouteActor
	}
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return CustodyWithdrawalRouteRequest{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "custody-route-approval:"+actor+":"+idempotencyKey); err != nil {
		return CustodyWithdrawalRouteRequest{}, err
	}
	var result CustodyWithdrawalRouteRequest
	var same bool
	err = tx.QueryRow(ctx, `SELECT request_id::text,route_id::text,request_id=$3 AND reason=$4
		FROM custody_withdrawal_route_approvals WHERE approved_by=$1 AND idempotency_key=$2`,
		actor, idempotencyKey, requestID, reason).Scan(&result.RequestID, &result.RouteID, &same)
	if err == nil {
		if !same {
			return CustodyWithdrawalRouteRequest{}, ErrWithdrawalCapacityRouteConflict
		}
		result.Duplicate = true
		return result, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return CustodyWithdrawalRouteRequest{}, err
	}
	if err = requireCustodyRouteRole(ctx, tx, actor, "treasury_approver"); err != nil {
		return CustodyWithdrawalRouteRequest{}, err
	}
	var proposer string
	err = tx.QueryRow(ctx, `SELECT q.route_id::text,q.proposed_by::text FROM custody_withdrawal_route_requests q
		WHERE q.id=$1 FOR SHARE`, requestID).Scan(&result.RouteID, &proposer)
	if errors.Is(err, pgx.ErrNoRows) || proposer == actor {
		return CustodyWithdrawalRouteRequest{}, ErrWithdrawalCapacityRouteActor
	}
	if err != nil {
		return CustodyWithdrawalRouteRequest{}, err
	}
	if err = requireCustodyRouteRole(ctx, tx, proposer, "treasury_operator"); err != nil {
		return CustodyWithdrawalRouteRequest{}, err
	}
	result.RequestID = requestID
	_, err = tx.Exec(ctx, `INSERT INTO custody_withdrawal_route_approvals
		(request_id,route_id,proposed_by,approved_by,reason,idempotency_key)
		VALUES($1,$2,$3,$4,$5,$6)`, requestID, result.RouteID, proposer, actor, reason, idempotencyKey)
	if err != nil {
		return CustodyWithdrawalRouteRequest{}, err
	}
	metadata, _ := json.Marshal(map[string]string{"request_id": requestID, "route_id": result.RouteID})
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(actor_id,actor_type,action,resource_type,resource_id,metadata)
		VALUES($1,'admin','custody.route_approved','custody_withdrawal_route',$2,$3::jsonb)`, actor, result.RouteID, metadata); err != nil {
		return CustodyWithdrawalRouteRequest{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO outbox_events(event_type,aggregate_type,aggregate_id,payload)
		VALUES('custody.route_approved','custody_withdrawal_route',$1,$2::jsonb)`, result.RouteID, metadata); err != nil {
		return CustodyWithdrawalRouteRequest{}, err
	}
	return result, tx.Commit(ctx)
}

func requireCustodyRouteRole(ctx context.Context, tx pgx.Tx, actor, role string) error {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users u JOIN user_roles r ON r.user_id=u.id
		WHERE u.id=$1 AND u.status='active' AND r.role=$2)`, actor, role).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return ErrWithdrawalCapacityRouteActor
	}
	return nil
}

func (m *Manager) WithdrawalCapacityRoute(ctx context.Context, provider, environment string, item WithdrawalForCustody) (WithdrawalCapacityRoute, error) {
	var required bool
	if err := m.pool.QueryRow(ctx, `SELECT withdrawal_limits_ready OR enabled OR
		EXISTS(SELECT 1 FROM test_money_grants) FROM test_money_control WHERE singleton=TRUE`).Scan(&required); err != nil {
		return WithdrawalCapacityRoute{}, err
	}
	required = required || environment == "production"
	if !required {
		return WithdrawalCapacityRoute{}, nil
	}
	var route WithdrawalCapacityRoute
	var action string
	err := m.pool.QueryRow(ctx, `SELECT r.id::text,r.asset_id::text,r.environment,r.provider,r.network,r.provider_asset_id,
		r.asset_decimals,r.fee_provider_asset_id,r.fee_asset_decimals,r.max_fee_atomic::text,
		r.max_observation_age_seconds,q.action
		FROM custody_withdrawal_route_approvals a
		JOIN custody_withdrawal_route_requests q ON q.id=a.request_id
		JOIN custody_withdrawal_routes r ON r.id=q.route_id
		WHERE r.asset_id=$1 AND r.provider=$2 AND r.environment=$3 AND r.network=$4
		ORDER BY a.approved_at DESC,a.request_id DESC LIMIT 1`, item.AssetID, provider, environment, item.Network).Scan(
		&route.RouteID, &route.AssetID, &route.Environment, &route.Provider, &route.Network, &route.ProviderAssetID,
		&route.AssetDecimals, &route.FeeProviderAssetID, &route.FeeAssetDecimals,
		&route.MaxFeeAtomic, &route.MaxObservationAgeSeconds, &action)
	if errors.Is(err, pgx.ErrNoRows) {
		return WithdrawalCapacityRoute{}, ErrWithdrawalCapacityRouteUnavailable
	}
	if err != nil {
		return WithdrawalCapacityRoute{}, err
	}
	if action != "enable" {
		return WithdrawalCapacityRoute{}, ErrWithdrawalCapacityRouteUnavailable
	}
	if environment == "" || route.Environment != environment || route.Network != item.Network ||
		route.ProviderAssetID != item.CustodyAssetID || route.AssetDecimals != item.AssetDecimals {
		return WithdrawalCapacityRoute{}, ErrWithdrawalCapacityRouteUnavailable
	}
	route.Required = true
	return route, nil
}

func parseCapacityAtomic(raw string) (*big.Int, error) {
	if raw == "" || strings.Trim(raw, "0123456789") != "" || len(raw) > 78 {
		return nil, ErrWithdrawalCapacityObservation
	}
	value, ok := new(big.Int).SetString(raw, 10)
	if !ok || value.Sign() < 0 {
		return nil, ErrWithdrawalCapacityObservation
	}
	return value, nil
}

func capacityRouteInTx(ctx context.Context, tx pgx.Tx, routeID string) (WithdrawalCapacityRoute, string, error) {
	if routeID == "" {
		return WithdrawalCapacityRoute{}, "", ErrWithdrawalCapacityObservation
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "custody-route:"+routeID); err != nil {
		return WithdrawalCapacityRoute{}, "", err
	}
	var route WithdrawalCapacityRoute
	var action string
	err := tx.QueryRow(ctx, `SELECT r.id::text,r.asset_id::text,r.environment,r.provider,r.network,r.provider_asset_id,
		r.asset_decimals,r.fee_provider_asset_id,r.fee_asset_decimals,r.max_fee_atomic::text,
		r.max_observation_age_seconds,q.action
		FROM custody_withdrawal_routes r
		JOIN custody_withdrawal_route_requests q ON q.route_id=r.id
		JOIN custody_withdrawal_route_approvals a ON a.request_id=q.id
		WHERE r.id=$1 ORDER BY a.approved_at DESC,a.request_id DESC LIMIT 1`, routeID).Scan(
		&route.RouteID, &route.AssetID, &route.Environment, &route.Provider, &route.Network, &route.ProviderAssetID,
		&route.AssetDecimals, &route.FeeProviderAssetID, &route.FeeAssetDecimals,
		&route.MaxFeeAtomic, &route.MaxObservationAgeSeconds, &action)
	if errors.Is(err, pgx.ErrNoRows) {
		return WithdrawalCapacityRoute{}, "", ErrWithdrawalCapacityRouteUnavailable
	}
	if err != nil {
		return WithdrawalCapacityRoute{}, "", err
	}
	if action != "enable" {
		return WithdrawalCapacityRoute{}, "", ErrWithdrawalCapacityRouteUnavailable
	}
	return route, action, nil
}

func lockCapacity(ctx context.Context, tx pgx.Tx, provider, vault string, assetIDs ...string) error {
	keys := make([]string, 0, len(assetIDs))
	seen := map[string]struct{}{}
	for _, asset := range assetIDs {
		key := "custody-capacity:" + provider + ":" + vault + ":" + asset
		if _, ok := seen[key]; !ok {
			seen[key] = struct{}{}
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, key); err != nil {
			return err
		}
	}
	return nil
}

func openCapacity(ctx context.Context, tx pgx.Tx, provider, vault, asset string) (*big.Int, error) {
	var raw string
	err := tx.QueryRow(ctx, `SELECT COALESCE(SUM(v),0)::text FROM (
		SELECT r.asset_amount_atomic v FROM custody_capacity_reservations r
		WHERE r.provider=$1 AND r.source_vault_id=$2 AND r.provider_asset_id=$3
		AND NOT EXISTS(SELECT 1 FROM custody_capacity_terminal_events t WHERE t.withdrawal_id=r.withdrawal_id)
		UNION ALL
		SELECT r.fee_amount_atomic v FROM custody_capacity_reservations r
		WHERE r.provider=$1 AND r.source_vault_id=$2 AND r.fee_provider_asset_id=$3
		AND NOT EXISTS(SELECT 1 FROM custody_capacity_terminal_events t WHERE t.withdrawal_id=r.withdrawal_id)
	) obligations`, provider, vault, asset).Scan(&raw)
	if err != nil {
		return nil, err
	}
	return parseCapacityAtomic(raw)
}
