package datamanager

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/limiance/backend/internal/ledger"
)

var ErrWithdrawalDispatchChanged = errors.New("withdrawal dispatch payload or eligibility changed")

// BeginWithdrawalDispatch commits a one-shot permission to call custody with
// precisely the previously validated payload. False means this caller is not
// authorized (already claimed or no longer eligible); it NEVER permits a retry.
//
// There is deliberately no lease. A crash after commit but before the call is
// indistinguishable from a lost provider ACK and requires reconciliation.
// Lock order follows admission: controls, entitlement, withdrawal, account,
// shared ledger lock. External network calls are not made in this transaction.
func (m *Manager) BeginWithdrawalDispatch(ctx context.Context, provider string, expected WithdrawalForCustody, capacity *WithdrawalCapacityEvidence) (bool, error) {
	if provider == "" || expected.ID == "" {
		return false, ErrWithdrawalDispatchChanged
	}
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var enabled bool
	if err = tx.QueryRow(ctx, `SELECT enabled FROM operational_controls WHERE control_key='withdrawals_enabled' FOR SHARE`).Scan(&enabled); err != nil {
		return false, err
	}
	if !enabled {
		return false, ErrWithdrawalsDisabled
	}
	var userID, assetID string
	if err = tx.QueryRow(ctx, `SELECT user_id::text,asset_id::text FROM withdrawals WHERE id=$1`, expected.ID).Scan(&userID, &assetID); err != nil {
		return false, err
	}
	// amount=0: this approved withdrawal is already included in commitments.
	entitlement, err := checkWithdrawalEntitlement(ctx, tx, userID, assetID, 0)
	if err != nil {
		return false, err
	}
	var actual WithdrawalForCustody
	var accountID string
	err = tx.QueryRow(ctx, `SELECT w.id::text,w.user_id::text,a.id::text,c.external_vault_id,
        a.custody_asset_id,a.network,w.destination_address,w.destination_tag,
        w.amount_atomic::text,a.decimals,w.account_id::text
        FROM withdrawals w JOIN assets a ON a.id=w.asset_id
        JOIN custody_wallets c ON c.user_id=w.user_id AND c.provider=$2 AND c.status='active'
        WHERE w.id=$1 AND w.status='approved' AND w.provider_transaction_id IS NULL
        AND a.status='enabled' AND a.custody_asset_id<>'' AND c.external_vault_id<>''
        FOR UPDATE OF w FOR SHARE OF a,c`, expected.ID, provider).Scan(
		&actual.ID, &actual.UserID, &actual.AssetID, &actual.SourceVaultID, &actual.CustodyAssetID,
		&actual.Network, &actual.DestinationAddress, &actual.DestinationTag,
		&actual.AmountAtomic, &actual.AssetDecimals, &accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if actual != expected {
		return false, ErrWithdrawalDispatchChanged
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM withdrawal_dispatches WHERE withdrawal_id=$1)`, actual.ID).Scan(&exists); err != nil || exists {
		return false, err
	}
	// Lock eligibility rows so revocation is ordered against this boundary.
	var id string
	err = tx.QueryRow(ctx, `SELECT u.id::text FROM users u JOIN kyc_profiles k ON k.user_id=u.id
		WHERE u.id=$1 AND u.status='active' AND k.status='approved'
		AND (k.expires_at IS NULL OR k.expires_at>now()) FOR SHARE OF u,k`, userID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrWithdrawalKYCRequired
	}
	if err != nil {
		return false, err
	}
	err = tx.QueryRow(ctx, `SELECT id::text FROM accounts WHERE id=$1 AND user_id=$2
        AND status='active' AND kind IN ('funding','uta') FOR SHARE`, accountID, userID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrWithdrawalAccountUnavailable
	}
	if err != nil {
		return false, err
	}
	if err = ledger.LockAccountAsset(ctx, tx, accountID, assetID); err != nil {
		return false, err
	}
	// Require the actual original hold, no compensation, and sufficient posted
	// held balance. Merely seeing approved status is not proof that funds exist.
	var held bool
	err = tx.QueryRow(ctx, `SELECT
        EXISTS(SELECT 1 FROM journals j WHERE j.withdrawal_id=$1 AND j.reference_type='withdrawal_hold' AND j.status='posted'
          AND (SELECT COUNT(*) FROM postings p WHERE p.journal_id=j.id)=2
          AND EXISTS(SELECT 1 FROM postings p WHERE p.journal_id=j.id AND p.account_id=$2 AND p.asset_id=$3
            AND p.bucket='available' AND p.direction='debit' AND p.amount_atomic=$4::numeric)
          AND EXISTS(SELECT 1 FROM postings p WHERE p.journal_id=j.id AND p.account_id=$2 AND p.asset_id=$3
            AND p.bucket='held' AND p.direction='credit' AND p.amount_atomic=$4::numeric))
        AND NOT EXISTS(SELECT 1 FROM journals WHERE withdrawal_id=$1 AND status='posted'
          AND reference_type IN ('withdrawal_cancel','withdrawal_release','withdrawal_settlement'))
        AND (SELECT COALESCE(SUM(CASE p.direction WHEN 'credit' THEN p.amount_atomic ELSE -p.amount_atomic END),0)
          FROM postings p JOIN journals j ON j.id=p.journal_id AND j.status='posted'
          WHERE p.account_id=$2 AND p.asset_id=$3 AND p.bucket='held') >= $4::numeric`,
		actual.ID, accountID, assetID, actual.AmountAtomic).Scan(&held)
	if err != nil {
		return false, err
	}
	if !held {
		return false, ErrInsufficientWithdrawalBalance
	}
	var routeID any
	var capacityRoute WithdrawalCapacityRoute
	if entitlement.enforced && capacity == nil {
		return false, ErrWithdrawalCapacityRouteUnavailable
	}
	if capacity != nil {
		route, _, routeErr := capacityRouteInTx(ctx, tx, capacity.RouteID)
		if routeErr != nil {
			return false, routeErr
		}
		if route.AssetID != actual.AssetID || route.Provider != provider || route.Environment != capacity.Environment || route.Network != actual.Network ||
			route.ProviderAssetID != actual.CustodyAssetID || route.AssetDecimals != actual.AssetDecimals {
			return false, ErrWithdrawalCapacityRouteUnavailable
		}
		var databaseNow time.Time
		if err = tx.QueryRow(ctx, `SELECT now()`).Scan(&databaseNow); err != nil {
			return false, err
		}
		if capacity.ObservedAt.IsZero() || capacity.ObservedAt.After(databaseNow.Add(time.Second)) ||
			capacity.ObservedAt.Before(databaseNow.Add(-time.Duration(route.MaxObservationAgeSeconds)*time.Second)) {
			return false, ErrWithdrawalCapacityObservation
		}
		amount, amountErr := parseCapacityAtomic(actual.AmountAtomic)
		assetAvailable, assetErr := parseCapacityAtomic(capacity.AssetAvailable)
		feeAvailable, feeAvailableErr := parseCapacityAtomic(capacity.FeeAvailable)
		feeRequired, feeErr := parseCapacityAtomic(capacity.FeeRequired)
		maxFee, maxFeeErr := parseCapacityAtomic(route.MaxFeeAtomic)
		if amountErr != nil || assetErr != nil || feeAvailableErr != nil || feeErr != nil || maxFeeErr != nil {
			return false, ErrWithdrawalCapacityObservation
		}
		if feeRequired.Cmp(maxFee) > 0 {
			return false, ErrWithdrawalCustodyCapacity
		}
		if err = lockCapacity(ctx, tx, provider, actual.SourceVaultID, route.ProviderAssetID, route.FeeProviderAssetID); err != nil {
			return false, err
		}
		assetOpen, openErr := openCapacity(ctx, tx, provider, actual.SourceVaultID, route.ProviderAssetID)
		if openErr != nil {
			return false, openErr
		}
		if route.ProviderAssetID == route.FeeProviderAssetID {
			if assetAvailable.Cmp(feeAvailable) != 0 {
				return false, ErrWithdrawalCapacityObservation
			}
			required := new(big.Int).Add(assetOpen, amount)
			required.Add(required, feeRequired)
			if required.Cmp(assetAvailable) > 0 {
				return false, ErrWithdrawalCustodyCapacity
			}
		} else {
			if new(big.Int).Add(assetOpen, amount).Cmp(assetAvailable) > 0 {
				return false, ErrWithdrawalCustodyCapacity
			}
			feeOpen, openErr := openCapacity(ctx, tx, provider, actual.SourceVaultID, route.FeeProviderAssetID)
			if openErr != nil {
				return false, openErr
			}
			if new(big.Int).Add(feeOpen, feeRequired).Cmp(feeAvailable) > 0 {
				return false, ErrWithdrawalCustodyCapacity
			}
		}
		capacityRoute = route
		routeID = route.RouteID
	}
	payload, err := json.Marshal(actual)
	if err != nil {
		return false, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO withdrawal_dispatches
		(withdrawal_id,provider,request,entitlement_enforced,credited_deposits_atomic,committed_withdrawals_atomic,capacity_route_id)
		VALUES($1,$2,$3::jsonb,$4,NULLIF($5,'')::numeric,NULLIF($6,'')::numeric,$7)`,
		actual.ID, provider, payload, entitlement.enforced, entitlement.credited, entitlement.committed, routeID)
	if err != nil {
		return false, err
	}
	if capacity != nil {
		_, err = tx.Exec(ctx, `INSERT INTO custody_capacity_reservations
			(withdrawal_id,route_id,provider,source_vault_id,provider_asset_id,asset_amount_atomic,
			 asset_available_atomic,fee_provider_asset_id,fee_amount_atomic,fee_available_atomic,
			 observed_at,asset_block_height,asset_block_hash,fee_block_height,fee_block_hash)
			VALUES($1,$2,$3,$4,$5,$6::numeric,$7::numeric,$8,$9::numeric,$10::numeric,$11,$12,$13,$14,$15)`,
			actual.ID, capacity.RouteID, provider, actual.SourceVaultID, actual.CustodyAssetID,
			actual.AmountAtomic, capacity.AssetAvailable, capacityRoute.FeeProviderAssetID,
			capacity.FeeRequired, capacity.FeeAvailable, capacity.ObservedAt,
			capacity.AssetBlockHeight, capacity.AssetBlockHash, capacity.FeeBlockHeight, capacity.FeeBlockHash)
		if err != nil {
			return false, err
		}
	}
	// No addresses in the general audit/outbox: the exact payload stays in the
	// dispatch table; the external id is always the withdrawal UUID.
	evidence := map[string]string{"withdrawal_id": actual.ID, "provider": provider}
	if capacity != nil {
		evidence["capacity_route_id"] = capacity.RouteID
	}
	auditEvidence, err := json.Marshal(evidence)
	if err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(actor_type,action,resource_type,resource_id,metadata)
		VALUES('system','withdrawal.dispatch_started','withdrawal',$1,$2::jsonb)`, actual.ID, auditEvidence); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO outbox_events(event_type,aggregate_type,aggregate_id,payload)
		VALUES('withdrawal.dispatch_started','withdrawal',$1,$2::jsonb)`, actual.ID, auditEvidence); err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}
