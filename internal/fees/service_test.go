package fees

import (
	"context"
	"testing"
	"time"

	"github.com/limiance/backend/internal/datamanager"
)

type repositoryStub struct {
	fees        datamanager.UserFees
	reads       int
	policyInput datamanager.FeePolicyInput
}

func (r *repositoryStub) UserFees(context.Context, string) (datamanager.UserFees, error) {
	r.reads++
	return r.fees, nil
}

func (*repositoryStub) RefreshUserFeeTiers(context.Context) error { return nil }

func (r *repositoryStub) UpdateFeePolicy(_ context.Context, input datamanager.FeePolicyInput) error {
	r.policyInput = input
	return nil
}

func TestGetUserFeesCachesByUserAndPair(t *testing.T) {
	repository := &repositoryStub{fees: datamanager.UserFees{TierLevel: 2, MakerFeeBPS: 6, TakerFeeBPS: 10}}
	service := NewService(repository, nil)

	first, err := service.GetUserFees(context.Background(), "user-1", "BTC/USDC")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.GetUserFees(context.Background(), "user-1", "BTC/USDC")
	if err != nil {
		t.Fatal(err)
	}
	if first != second || repository.reads != 1 {
		t.Fatalf("expected one repository read and equal results, reads=%d", repository.reads)
	}
}

func TestGetUserFeesDoesNotSharePairs(t *testing.T) {
	repository := &repositoryStub{fees: datamanager.UserFees{TierLevel: 1}}
	service := NewService(repository, nil)

	if _, err := service.GetUserFees(context.Background(), "user-1", "BTC/USDC"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetUserFees(context.Background(), "user-1", "ETH/USDC"); err != nil {
		t.Fatal(err)
	}
	if repository.reads != 2 {
		t.Fatalf("expected separate pair cache entries, reads=%d", repository.reads)
	}
}

func TestFeeCacheExpiresAndRefreshes(t *testing.T) {
	repository := &repositoryStub{fees: datamanager.UserFees{TierLevel: 1}}
	service := NewServiceWithTTL(repository, nil, time.Millisecond)
	if _, err := service.GetUserFees(context.Background(), "user-1", "BTCUSDT"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	repository.fees = datamanager.UserFees{TierLevel: 2}
	resolved, err := service.GetUserFees(context.Background(), "user-1", "BTCUSDT")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.TierLevel != 2 || repository.reads != 2 {
		t.Fatalf("expired fee was not refreshed: fees=%+v reads=%d", resolved, repository.reads)
	}
}

func TestTierRefreshInvalidatesCachedFees(t *testing.T) {
	repository := &repositoryStub{fees: datamanager.UserFees{TierLevel: 1}}
	service := NewService(repository, nil)
	if _, err := service.GetUserFees(context.Background(), "user-1", "BTCUSDT"); err != nil {
		t.Fatal(err)
	}
	repository.fees = datamanager.UserFees{TierLevel: 3}
	if err := service.RefreshUserFeeTiers(context.Background()); err != nil {
		t.Fatal(err)
	}
	resolved, err := service.GetUserFees(context.Background(), "user-1", "BTCUSDT")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.TierLevel != 3 || repository.reads != 2 {
		t.Fatalf("tier refresh left stale fee: fees=%+v reads=%d", resolved, repository.reads)
	}
}

func TestPolicyChangeInvalidatesCachedFees(t *testing.T) {
	repository := &repositoryStub{fees: datamanager.UserFees{TierLevel: 5, MakerFeeBPS: 0, TakerFeeBPS: 6}}
	service := NewService(repository, nil)
	if _, err := service.GetUserFees(context.Background(), "user-1", "BTCUSDT"); err != nil {
		t.Fatal(err)
	}
	input := datamanager.FeePolicyInput{TierLevel: 5, MakerFeeBPS: -2, TakerFeeBPS: 6, MinimumVolume: "20000000", Reason: "approved rebate"}
	if err := service.ConfigureTier(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	repository.fees.MakerFeeBPS = -2
	resolved, err := service.GetUserFees(context.Background(), "user-1", "BTCUSDT")
	if err != nil {
		t.Fatal(err)
	}
	if repository.policyInput != input || resolved.MakerFeeBPS != -2 || repository.reads != 2 {
		t.Fatalf("policy invalidation failed: input=%+v fees=%+v reads=%d", repository.policyInput, resolved, repository.reads)
	}
}
