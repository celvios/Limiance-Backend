package withdrawals

import (
	"context"
	"errors"
	"testing"

	"github.com/limiance/backend/internal/custody"
	"github.com/limiance/backend/internal/datamanager"
)

type reconciliationDataStub struct {
	candidates []datamanager.WithdrawalReconciliationCandidate
	observed   []datamanager.WithdrawalReconciliationObservation
	marked     []string
	updates    []datamanager.WithdrawalCustodyUpdate
}

func (s *reconciliationDataStub) WithdrawalReconciliationCandidates(context.Context, string, int) ([]datamanager.WithdrawalReconciliationCandidate, error) {
	return s.candidates, nil
}
func (s *reconciliationDataStub) RecordWithdrawalReconciliationObservation(_ context.Context, _ datamanager.WithdrawalReconciliationCandidate, observation datamanager.WithdrawalReconciliationObservation) (bool, error) {
	s.observed = append(s.observed, observation)
	return true, nil
}
func (s *reconciliationDataStub) MarkWithdrawalSubmitted(_ context.Context, _, providerID string) error {
	s.marked = append(s.marked, providerID)
	return nil
}
func (s *reconciliationDataStub) RecordWithdrawalCustodyUpdate(_ context.Context, update datamanager.WithdrawalCustodyUpdate) (bool, error) {
	s.updates = append(s.updates, update)
	return true, nil
}

type reconciliationObserverStub struct {
	lookup custody.WithdrawalLookup
	err    error
}

func (s *reconciliationObserverStub) GetVaultAssetBalance(context.Context, string, string) (custody.VaultAssetBalance, error) {
	return custody.VaultAssetBalance{}, errors.New("unexpected balance call")
}
func (s *reconciliationObserverStub) EstimateWithdrawalFee(context.Context, custody.WithdrawalRequest) (custody.FeeEstimate, error) {
	return custody.FeeEstimate{}, errors.New("unexpected fee call")
}
func (s *reconciliationObserverStub) FindWithdrawalByExternalID(context.Context, string) (custody.WithdrawalLookup, error) {
	return s.lookup, s.err
}

func TestReconcilerPreservesNotFoundAsUnresolvedEvidence(t *testing.T) {
	data := &reconciliationDataStub{candidates: []datamanager.WithdrawalReconciliationCandidate{{WithdrawalID: "withdrawal-1", Provider: "fireblocks"}}}
	observer := &reconciliationObserverStub{lookup: custody.WithdrawalLookup{ExternalID: "withdrawal-1"}}
	processed, err := NewReconciler(data, observer, "fireblocks").RunOnce(context.Background())
	if err != nil || processed != 1 || len(data.observed) != 1 || len(data.marked) != 0 || len(data.updates) != 0 {
		t.Fatalf("processed=%d observed=%d marked=%d updates=%d err=%v", processed, len(data.observed), len(data.marked), len(data.updates), err)
	}
}

func TestReconcilerBindsAndAppliesExplicitProviderOutcome(t *testing.T) {
	data := &reconciliationDataStub{candidates: []datamanager.WithdrawalReconciliationCandidate{{WithdrawalID: "withdrawal-1", Provider: "fireblocks"}}}
	lookup := custody.WithdrawalLookup{Found: true, ExternalID: "withdrawal-1", ProviderTransactionID: "provider-1", Status: "COMPLETED", TransactionHash: "hash-1"}
	processed, err := NewReconciler(data, &reconciliationObserverStub{lookup: lookup}, "fireblocks").RunOnce(context.Background())
	if err != nil || processed != 1 || len(data.observed) != 1 || len(data.marked) != 1 || len(data.updates) != 1 {
		t.Fatalf("processed=%d observed=%d marked=%d updates=%d err=%v", processed, len(data.observed), len(data.marked), len(data.updates), err)
	}
	if data.marked[0] != "provider-1" || data.updates[0].Status != "COMPLETED" || data.updates[0].TransactionHash != "hash-1" {
		t.Fatalf("marked=%v update=%+v", data.marked, data.updates[0])
	}
}

func TestReconcilerFailsClosedOnLookupErrorOrIdentityMismatch(t *testing.T) {
	candidate := datamanager.WithdrawalReconciliationCandidate{WithdrawalID: "withdrawal-1", Provider: "fireblocks"}
	for _, observer := range []*reconciliationObserverStub{
		{err: errors.New("provider unavailable")},
		{lookup: custody.WithdrawalLookup{ExternalID: "other"}},
	} {
		data := &reconciliationDataStub{candidates: []datamanager.WithdrawalReconciliationCandidate{candidate}}
		if processed, err := NewReconciler(data, observer, "fireblocks").RunOnce(context.Background()); err == nil || processed != 0 {
			t.Fatalf("processed=%d err=%v", processed, err)
		}
		if len(data.observed) != 0 || len(data.marked) != 0 || len(data.updates) != 0 {
			t.Fatal("provider uncertainty changed local state")
		}
	}
}
