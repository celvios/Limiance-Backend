package withdrawals

import (
	"context"
	"errors"

	"github.com/limiance/backend/internal/custody"
	"github.com/limiance/backend/internal/datamanager"
)

type CustodySubmitter interface {
	CreateWithdrawal(context.Context, custody.WithdrawalRequest) (custody.Withdrawal, error)
}

type WithdrawalData interface {
	ApprovedWithdrawals(context.Context, string, int) ([]datamanager.WithdrawalForCustody, error)
	BeginWithdrawalDispatch(context.Context, string, datamanager.WithdrawalForCustody) (bool, error)
	MarkWithdrawalSubmitted(context.Context, string, string) error
}

type Worker struct {
	data       WithdrawalData
	provider   CustodySubmitter
	providerID string
	policy     custody.RoutePolicy
}

func NewWorker(data WithdrawalData, provider CustodySubmitter, providerID string, policies ...custody.RoutePolicy) *Worker {
	policy := custody.RoutePolicy{}
	if len(policies) > 0 {
		policy = policies[0]
	}
	return &Worker{data: data, provider: provider, providerID: providerID, policy: policy}
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
		// Commit the durable boundary only after local validation, immediately
		// before the external call. An error after this point is an unknown
		// outcome, not permission to retry or release held funds.
		claimed, err := w.data.BeginWithdrawalDispatch(ctx, w.providerID, item)
		if err != nil {
			return processed, err
		}
		if !claimed {
			continue
		}
		result, err := w.provider.CreateWithdrawal(ctx, custody.WithdrawalRequest{
			SourceVaultID:  item.SourceVaultID,
			AssetID:        item.CustodyAssetID,
			Destination:    item.DestinationAddress,
			DestinationTag: item.DestinationTag,
			Amount:         amount,
			ExternalID:     item.ID,
		})
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
