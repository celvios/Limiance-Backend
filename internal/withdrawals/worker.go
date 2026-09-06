package withdrawals

import (
	"context"
	"errors"
	"time"

	"github.com/limiance/backend/internal/custody"
	"github.com/limiance/backend/internal/datamanager"
)

type CustodySubmitter interface {
	CreateWithdrawal(context.Context, custody.WithdrawalRequest) (custody.Withdrawal, error)
}

type WithdrawalData interface {
	ApprovedWithdrawals(context.Context, string, int) ([]datamanager.WithdrawalForCustody, error)
	WithdrawalCapacityRoute(context.Context, string, string, datamanager.WithdrawalForCustody) (datamanager.WithdrawalCapacityRoute, error)
	BeginWithdrawalDispatch(context.Context, string, datamanager.WithdrawalForCustody, *datamanager.WithdrawalCapacityEvidence) (bool, error)
	MarkWithdrawalSubmitted(context.Context, string, string) error
}

type Worker struct {
	data       WithdrawalData
	provider   CustodySubmitter
	providerID string
	policy     custody.RoutePolicy
	now        func() time.Time
}

func NewWorker(data WithdrawalData, provider CustodySubmitter, providerID string, policies ...custody.RoutePolicy) *Worker {
	policy := custody.RoutePolicy{}
	if len(policies) > 0 {
		policy = policies[0]
	}
	return &Worker{data: data, provider: provider, providerID: providerID, policy: policy, now: time.Now}
}

func (w *Worker) RunOnce(ctx context.Context) (int, error) {
	if w.data == nil || w.provider == nil || w.providerID == "" {
		return 0, errors.New("withdrawal worker is not configured")
	}
	items, err := w.data.ApprovedWithdrawals(ctx, w.providerID, 25)
	if err != nil {
		return 0, err
	}
	processed := 0
	for _, item := range items {
		if err := w.policy.Validate(item.Network); err != nil {
			return processed, err
		}
		amount, err := custody.AtomicToProviderAmount(item.AmountAtomic, item.AssetDecimals)
		if err != nil {
			return processed, err
		}
		request := custody.WithdrawalRequest{
			SourceVaultID:  item.SourceVaultID,
			AssetID:        item.CustodyAssetID,
			Destination:    item.DestinationAddress,
			DestinationTag: item.DestinationTag,
			Amount:         amount,
			ExternalID:     item.ID,
		}
		route, err := w.data.WithdrawalCapacityRoute(ctx, w.providerID, w.policy.Environment, item)
		if err != nil {
			return processed, err
		}
		var evidence *datamanager.WithdrawalCapacityEvidence
		if route.Required {
			observedAt := w.now().UTC()
			observer, ok := w.provider.(custody.WithdrawalObserver)
			if !ok {
				return processed, datamanager.ErrWithdrawalCapacityObservation
			}
			assetBalance, err := observer.GetVaultAssetBalance(ctx, item.SourceVaultID, route.ProviderAssetID)
			if err != nil || assetBalance.AssetID != route.ProviderAssetID {
				if err != nil {
					return processed, err
				}
				return processed, datamanager.ErrWithdrawalCapacityObservation
			}
			feeBalance := assetBalance
			if route.FeeProviderAssetID != route.ProviderAssetID {
				feeBalance, err = observer.GetVaultAssetBalance(ctx, item.SourceVaultID, route.FeeProviderAssetID)
				if err != nil || feeBalance.AssetID != route.FeeProviderAssetID {
					if err != nil {
						return processed, err
					}
					return processed, datamanager.ErrWithdrawalCapacityObservation
				}
			}
			fee, err := observer.EstimateWithdrawalFee(ctx, request)
			if err != nil {
				return processed, err
			}
			assetAvailable, err := custody.ProviderAmountToAtomic(assetBalance.Available, route.AssetDecimals)
			if err != nil {
				return processed, datamanager.ErrWithdrawalCapacityObservation
			}
			feeAvailable, err := custody.ProviderAmountToAtomic(feeBalance.Available, route.FeeAssetDecimals)
			if err != nil {
				return processed, datamanager.ErrWithdrawalCapacityObservation
			}
			feeRequired, err := custody.ProviderAmountToAtomic(fee.NetworkFee, route.FeeAssetDecimals)
			if err != nil {
				return processed, datamanager.ErrWithdrawalCapacityObservation
			}
			evidence = &datamanager.WithdrawalCapacityEvidence{
				RouteID: route.RouteID, Environment: route.Environment, ObservedAt: observedAt, AssetAvailable: assetAvailable,
				FeeAvailable: feeAvailable, FeeRequired: feeRequired,
				AssetBlockHeight: assetBalance.BlockHeight, AssetBlockHash: assetBalance.BlockHash,
				FeeBlockHeight: feeBalance.BlockHeight, FeeBlockHash: feeBalance.BlockHash,
			}
		}
		// Commit the durable boundary only after local validation and fresh
		// read-only custody observations, immediately before the external call.
		// An error after this point is an unknown
		// outcome, not permission to retry or release held funds.
		claimed, err := w.data.BeginWithdrawalDispatch(ctx, w.providerID, item, evidence)
		if err != nil {
			return processed, err
		}
		if !claimed {
			continue
		}
		result, err := w.provider.CreateWithdrawal(ctx, request)
		if err != nil {
			return processed, err
		}
		if result.ProviderTransactionID == "" {
			return processed, errors.New("custody provider returned an empty withdrawal transaction ID")
		}
		if err := w.data.MarkWithdrawalSubmitted(ctx, item.ID, result.ProviderTransactionID); err != nil {
			return processed, err
		}
		processed++
	}
	return processed, nil
}
