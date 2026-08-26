package fees

import (
	"context"
	"testing"

	"github.com/limiance/backend/internal/datamanager"
)

type repositoryStub struct {
	fees  datamanager.UserFees
	reads int
}

func (r *repositoryStub) UserFees(context.Context, string) (datamanager.UserFees, error) {
	r.reads++
	return r.fees, nil
}

func (*repositoryStub) RefreshUserFeeTiers(context.Context) error { return nil }

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
