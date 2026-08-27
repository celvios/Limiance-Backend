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

type Worker struct {
	data       *datamanager.Manager
	provider   CustodySubmitter
	providerID string
}

func NewWorker(data *datamanager.Manager, provider CustodySubmitter, providerID string) *Worker {
	return &Worker{data: data, provider: provider, providerID: providerID}
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
		amount, err := custody.AtomicToProviderAmount(item.AmountAtomic, item.AssetDecimals)
		if err != nil {
			return processed, err
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
