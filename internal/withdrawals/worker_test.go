package withdrawals

import (
	"context"
	"errors"
	"testing"

	"github.com/limiance/backend/internal/custody"
	"github.com/limiance/backend/internal/datamanager"
)

type withdrawalDataStub struct {
	items  []datamanager.WithdrawalForCustody
	marked int
}

func (stub *withdrawalDataStub) ApprovedWithdrawals(context.Context, string, int) ([]datamanager.WithdrawalForCustody, error) {
	return stub.items, nil
}

func (stub *withdrawalDataStub) MarkWithdrawalSubmitted(context.Context, string, string) error {
	stub.marked++
	return nil
}

type submitterStub struct{ calls int }

func (stub *submitterStub) CreateWithdrawal(context.Context, custody.WithdrawalRequest) (custody.Withdrawal, error) {
	stub.calls++
	return custody.Withdrawal{ProviderTransactionID: "testnet-transaction"}, nil
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
