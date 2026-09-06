package withdrawals

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/limiance/backend/internal/custody"
	"github.com/limiance/backend/internal/datamanager"
)

type withdrawalDataStub struct {
	items    []datamanager.WithdrawalForCustody
	route    datamanager.WithdrawalCapacityRoute
	evidence *datamanager.WithdrawalCapacityEvidence
	marked   int
	claimed  bool
	claimErr error
	markErr  error
}

func (stub *withdrawalDataStub) WithdrawalCapacityRoute(context.Context, string, string, datamanager.WithdrawalForCustody) (datamanager.WithdrawalCapacityRoute, error) {
	return stub.route, nil
}

func (stub *withdrawalDataStub) BeginWithdrawalDispatch(_ context.Context, _ string, _ datamanager.WithdrawalForCustody, evidence *datamanager.WithdrawalCapacityEvidence) (bool, error) {
	stub.evidence = evidence
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

type observerSubmitterStub struct {
	submitterStub
	balances map[string]custody.VaultAssetBalance
	fee      custody.FeeEstimate
	observed []string
}

func (stub *observerSubmitterStub) GetVaultAssetBalance(_ context.Context, _, assetID string) (custody.VaultAssetBalance, error) {
	stub.observed = append(stub.observed, assetID)
	return stub.balances[assetID], nil
}

func (stub *observerSubmitterStub) EstimateWithdrawalFee(context.Context, custody.WithdrawalRequest) (custody.FeeEstimate, error) {
	return stub.fee, nil
}

func (stub *observerSubmitterStub) FindWithdrawalByExternalID(context.Context, string) (custody.WithdrawalLookup, error) {
	return custody.WithdrawalLookup{}, nil
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

func TestWorkerObservesTokenAndGasBeforeClaim(t *testing.T) {
	item := datamanager.WithdrawalForCustody{ID: "withdrawal-1", AssetID: "asset-1", Network: "ethereum_sepolia",
		AmountAtomic: "250000", AssetDecimals: 6, SourceVaultID: "vault", CustodyAssetID: "USDC_TEST5",
		DestinationAddress: "destination"}
	data := &withdrawalDataStub{items: []datamanager.WithdrawalForCustody{item}, route: datamanager.WithdrawalCapacityRoute{
		Required: true, RouteID: "route-1", AssetID: "asset-1", Environment: "staging", Provider: "fireblocks",
		Network: "ethereum_sepolia", ProviderAssetID: "USDC_TEST5", AssetDecimals: 6,
		FeeProviderAssetID: "ETH_TEST5", FeeAssetDecimals: 18, MaxFeeAtomic: "10000000000000000", MaxObservationAgeSeconds: 10,
	}}
	provider := &observerSubmitterStub{balances: map[string]custody.VaultAssetBalance{
		"USDC_TEST5": {AssetID: "USDC_TEST5", Available: "12.345678", BlockHeight: "1", BlockHash: "token-block"},
		"ETH_TEST5":  {AssetID: "ETH_TEST5", Available: "0.25", BlockHeight: "2", BlockHash: "gas-block"},
	}, fee: custody.FeeEstimate{NetworkFee: "0.0015"}}
	worker := NewWorker(data, provider, "fireblocks", custody.RoutePolicy{Environment: "staging"})
	fixed := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return fixed }
	processed, err := worker.RunOnce(context.Background())
	if err != nil || processed != 1 {
		t.Fatalf("processed=%d err=%v", processed, err)
	}
	if len(provider.observed) != 2 || provider.observed[0] != "USDC_TEST5" || provider.observed[1] != "ETH_TEST5" {
		t.Fatalf("observations=%v", provider.observed)
	}
	want := datamanager.WithdrawalCapacityEvidence{RouteID: "route-1", Environment: "staging", ObservedAt: fixed,
		AssetAvailable: "12345678", FeeAvailable: "250000000000000000", FeeRequired: "1500000000000000",
		AssetBlockHeight: "1", AssetBlockHash: "token-block", FeeBlockHeight: "2", FeeBlockHash: "gas-block"}
	if data.evidence == nil || *data.evidence != want {
		t.Fatalf("evidence=%+v", data.evidence)
	}
}

func TestWorkerFailsClosedWithoutRequiredObserver(t *testing.T) {
	data := &withdrawalDataStub{items: []datamanager.WithdrawalForCustody{{ID: "id", AssetID: "asset", AmountAtomic: "1", AssetDecimals: 0}},
		route: datamanager.WithdrawalCapacityRoute{Required: true}}
	provider := &submitterStub{}
	if n, err := NewWorker(data, provider, "fireblocks").RunOnce(context.Background()); n != 0 || !errors.Is(err, datamanager.ErrWithdrawalCapacityObservation) {
		t.Fatalf("processed=%d err=%v", n, err)
	}
	if data.claimed || provider.calls != 0 {
		t.Fatal("dispatch progressed without custody observation support")
	}
}
