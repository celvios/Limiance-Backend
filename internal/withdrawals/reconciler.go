package withdrawals

import (
	"context"
	"errors"

	"github.com/limiance/backend/internal/custody"
	"github.com/limiance/backend/internal/datamanager"
)

type ReconciliationData interface {
	WithdrawalReconciliationCandidates(context.Context, string, int) ([]datamanager.WithdrawalReconciliationCandidate, error)
	RecordWithdrawalReconciliationObservation(context.Context, datamanager.WithdrawalReconciliationCandidate, datamanager.WithdrawalReconciliationObservation) (bool, error)
	MarkWithdrawalSubmitted(context.Context, string, string) error
	RecordWithdrawalCustodyUpdate(context.Context, datamanager.WithdrawalCustodyUpdate) (bool, error)
}

type Reconciler struct {
	data       ReconciliationData
	observer   custody.WithdrawalObserver
	providerID string
}

func NewReconciler(data ReconciliationData, observer custody.WithdrawalObserver, providerID string) *Reconciler {
	return &Reconciler{data: data, observer: observer, providerID: providerID}
}

func (r *Reconciler) RunOnce(ctx context.Context) (int, error) {
	if r.data == nil || r.observer == nil || r.providerID == "" {
		return 0, errors.New("withdrawal reconciler is not configured")
	}
	candidates, err := r.data.WithdrawalReconciliationCandidates(ctx, r.providerID, 25)
	if err != nil {
		return 0, err
	}
	processed := 0
	for _, candidate := range candidates {
		observation, err := r.observer.FindWithdrawalByExternalID(ctx, candidate.WithdrawalID)
		if err != nil {
			return processed, err
		}
		if observation.ExternalID != candidate.WithdrawalID {
			return processed, datamanager.ErrWithdrawalDispatchChanged
		}
		if observation.Found {
			if observation.ProviderTransactionID == "" || observation.Status == "" {
				return processed, datamanager.ErrWithdrawalDispatchChanged
			}
			if err := r.data.MarkWithdrawalSubmitted(ctx, candidate.WithdrawalID, observation.ProviderTransactionID); err != nil {
				return processed, err
			}
		}
		recorded, err := r.data.RecordWithdrawalReconciliationObservation(ctx, candidate, datamanager.WithdrawalReconciliationObservation{
			Found: observation.Found, ProviderTransactionID: observation.ProviderTransactionID,
			ExternalID: observation.ExternalID, Status: observation.Status, TransactionHash: observation.TransactionHash,
		})
		if err != nil {
			return processed, err
		}
		if !recorded {
			continue
		}
		if observation.Found {
			if _, err := r.data.RecordWithdrawalCustodyUpdate(ctx, datamanager.WithdrawalCustodyUpdate{
				ProviderTransactionID: observation.ProviderTransactionID,
				Status:                observation.Status, TransactionHash: observation.TransactionHash,
			}); err != nil {
				return processed, err
			}
		}
		processed++
	}
	return processed, nil
}
