package withdrawals

import (
	"context"
	"errors"
	"testing"

	"github.com/limiance/backend/internal/custody"
	"github.com/limiance/backend/internal/datamanager"
)

type withdrawalDataStub struct {
	items    []datamanager.WithdrawalForCustody
	marked   int
	claimed  bool
	claimErr error
	markErr  error
}

func (stub *withdrawalDataStub) BeginWithdrawalDispatch(context.Context, string, datamanager.WithdrawalForCustody) (bool, error) {
	if stub.claimErr != nil {
		return false, stub.claimErr
	}
	if stub.claimed {
		return false, nil
	}
	stub.claimed = true
	return true, nil
}

func (stub *withdrawalDataStub) ApprovedWithdrawals(context.Context, string, int) ([]datamanager.WithdrawalForCustody, error) {
	return stub.items, nil
}

func (stub *withdrawalDataStub) MarkWithdrawalSubmitted(context.Context, string, string) error {
	if stub.markErr != nil {
		return stub.markErr
	}
	stub.marked++
	return nil
}

type submitterStub struct {
	calls   int
	err     error
	empty   bool
	request custody.WithdrawalRequest
}

func (stub *submitterStub) CreateWithdrawal(_ context.Context, request custody.WithdrawalRequest) (custody.Withdrawal, error) {
	stub.calls++
	stub.request = request
	if stub.err != nil {
		return custody.Withdrawal{}, stub.err
	}
	if stub.empty {
		return custody.Withdrawal{}, nil
	}
	return custody.Withdrawal{ProviderTransactionID: "testnet-transaction"}, nil
}

func TestWorkerNeverRetriesUncertainSubmission(t *testing.T) {
	for _, failure := range []string{"lost_ack", "empty_ack", "database_ack_failure"} {
		t.Run(failure, func(t *testing.T) {
			data := &withdrawalDataStub{items: []datamanager.WithdrawalForCustody{{
				ID: "stable-external-id", Network: "ethereum_sepolia", AmountAtomic: "12345", AssetDecimals: 6,
				SourceVaultID: "source", CustodyAssetID: "token", DestinationAddress: "destination", DestinationTag: "memo",
			}}}
			provider := &submitterStub{}
			switch failure {
			case "lost_ack":
				provider.err = errors.New("timeout after send")
			case "empty_ack":
				provider.empty = true
			case "database_ack_failure":
				data.markErr = errors.New("database unavailable")
			}
			worker := NewWorker(data, provider, "fireblocks")
			if n, err := worker.RunOnce(context.Background()); err == nil || n != 0 {
				t.Fatalf("first: %d %v", n, err)
			}
			// New worker, same durable repository. Even a stale candidate list
			// cannot authorize resubmission after restart.
			worker = NewWorker(data, provider, "fireblocks")
			if n, err := worker.RunOnce(context.Background()); err != nil || n != 0 {
				t.Fatalf("retry: %d %v", n, err)
			}
			if provider.calls != 1 || data.marked != 0 {
				t.Fatalf("calls=%d marked=%d", provider.calls, data.marked)
			}
			want := custody.WithdrawalRequest{SourceVaultID: "source", AssetID: "token", Destination: "destination", DestinationTag: "memo", Amount: "0.012345", ExternalID: "stable-external-id"}
			if provider.request != want {
				t.Fatalf("payload: %+v", provider.request)
			}
		})
	}
}

func TestWorkerDoesNotCallCustodyWhenDispatchCommitFails(t *testing.T) {
	sentinel := errors.New("dispatch commit failed")
	data := &withdrawalDataStub{claimErr: sentinel, items: []datamanager.WithdrawalForCustody{{ID: "id", AmountAtomic: "1", AssetDecimals: 0}}}
	provider := &submitterStub{}
	if n, err := NewWorker(data, provider, "fireblocks").RunOnce(context.Background()); n != 0 || !errors.Is(err, sentinel) {
		t.Fatalf("%d %v", n, err)
	}
	if provider.calls != 0 || data.marked != 0 {
		t.Fatal("submitted without durable boundary")
	}
}

func TestWorkerAllowsApprovedTestnetAndRejectsMainnet(t *testing.T) {
	data := &withdrawalDataStub{items: []datamanager.WithdrawalForCustody{{ID: "withdrawal-1", Network: "ethereum_sepolia", AmountAtomic: "250000", AssetDecimals: 6}}}
	provider := &submitterStub{}
	policy := custody.RoutePolicy{Mode: "self_custody_testnet", SelfCustodyTestnetEnabled: true}
	worker := NewWorker(data, provider, "limiance_self_custody_testnet", policy)
	processed, err := worker.RunOnce(context.Background())
	if err != nil || processed != 1 || provider.calls != 1 || data.marked != 1 {
		t.Fatalf("approved testnet: processed=%d calls=%d marked=%d err=%v", processed, provider.calls, data.marked, err)
	}

	data.items[0].Network = "ethereum"
	processed, err = worker.RunOnce(context.Background())
	if !errors.Is(err, custody.ErrNetworkNotApproved) || processed != 0 || provider.calls != 1 || data.marked != 1 {
		t.Fatalf("mainnet: processed=%d calls=%d marked=%d err=%v", processed, provider.calls, data.marked, err)
	}
}
