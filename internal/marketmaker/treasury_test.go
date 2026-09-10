package marketmaker

import (
	"context"
	"errors"
	"testing"
)

func TestTreasuryInventoryIsStagingOnly(t *testing.T) {
	service := NewTreasuryInventoryService(nil, "production", nil)
	_, err := service.Propose(context.Background(), "actor", TreasuryInventoryInput{
		AssetID: "asset", AmountAtomic: "1", Reason: "production must reject", IdempotencyKey: "key",
	})
	if !errors.Is(err, ErrActivationState) {
		t.Fatalf("production proposal error=%v", err)
	}
}

func TestTreasuryUSDTValuationUsesExactAtomicScale(t *testing.T) {
	service := NewTreasuryInventoryService(nil, "staging", nil)
	value, evidence, err := service.value(context.Background(), nil, TreasuryInventoryRequest{AmountAtomic: "1234567"}, "USDT", 6)
	if err != nil {
		t.Fatal(err)
	}
	if value != "123456700" {
		t.Fatalf("value=%s want 123456700", value)
	}
	if len(evidence) == 0 {
		t.Fatal("missing exact quote-unit evidence")
	}
}

func TestTreasuryUSDTValuationRejectsUnsupportedPrecision(t *testing.T) {
	service := NewTreasuryInventoryService(nil, "staging", nil)
	_, _, err := service.value(context.Background(), nil, TreasuryInventoryRequest{AmountAtomic: "1"}, "USDT", 9)
	if !errors.Is(err, ErrActivationState) {
		t.Fatalf("precision error=%v", err)
	}
}
