package marketmaker

import (
	"errors"
	"testing"
)

func validDryRunActivation() ActivationInput {
	return ActivationInput{
		Action: "configure_dry_run", MarketMakerEmail: "maker@example.com", Reason: "approved staging inventory", IdempotencyKey: "activation-1",
		Inventory: []InventoryGrant{{Asset: "USDT", SourceAccountID: "treasury", AmountAtomic: "100000000"}},
		Pairs:     []PairPolicy{{Pair: "BTCUSDT", SpreadBPS: 30, QuantityAtomic: "1000", MaxBaseInventoryAtomic: "10000", MaxQuoteNotionalAtomic: "500000000", MaxDailyLossAtomic: "1000000", MaxDivergenceBPS: 100, StaleAfterSeconds: 10}},
	}
}

func TestActivationRequiresExplicitPositiveAtomicRiskLimits(t *testing.T) {
	input := validDryRunActivation()
	if err := validateActivation(input); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"", "0", "-1", "1.5", "NaN"} {
		input.Pairs[0].MaxDailyLossAtomic = invalid
		if err := validateActivation(input); !errors.Is(err, ErrActivationInput) {
			t.Fatalf("limit %q: %v", invalid, err)
		}
	}
}

func TestActivationRejectsDuplicatePairsAndInventoryAssets(t *testing.T) {
	input := validDryRunActivation()
	input.Pairs = append(input.Pairs, input.Pairs[0])
	if err := validateActivation(input); !errors.Is(err, ErrActivationInput) {
		t.Fatalf("duplicate pair: %v", err)
	}
	input = validDryRunActivation()
	input.Inventory = append(input.Inventory, input.Inventory[0])
	if err := validateActivation(input); !errors.Is(err, ErrActivationInput) {
		t.Fatalf("duplicate asset: %v", err)
	}
}

func TestLiveReleaseRequiresAllIndependentEvidence(t *testing.T) {
	input := ActivationInput{Action: "release_live", Reason: "dry run evidence approved", IdempotencyKey: "live-1", ReferenceEvidence: true, EmergencyStopTested: true, LedgerReconciled: true, OrderLifecycleTested: true}
	if err := validateActivation(input); err != nil {
		t.Fatal(err)
	}
	input.LedgerReconciled = false
	if err := validateActivation(input); !errors.Is(err, ErrActivationInput) {
		t.Fatalf("missing reconciliation: %v", err)
	}
}

func TestLiveReleaseCannotMutateInventoryOrPairPolicy(t *testing.T) {
	input := ActivationInput{Action: "release_live", Reason: "dry run evidence approved", IdempotencyKey: "live-1", ReferenceEvidence: true, EmergencyStopTested: true, LedgerReconciled: true, OrderLifecycleTested: true, Inventory: []InventoryGrant{{Asset: "BTC"}}}
	if err := validateActivation(input); !errors.Is(err, ErrActivationInput) {
		t.Fatalf("live mutation: %v", err)
	}
}
