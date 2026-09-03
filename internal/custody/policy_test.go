package custody

import "testing"

func TestRoutePolicyFailsClosedForSelfCustody(t *testing.T) {
	policy := RoutePolicy{Mode: "self_custody_testnet", SelfCustodyTestnetEnabled: true}
	for _, network := range []string{"ethereum_sepolia", "bitcoin_testnet4"} {
		if err := policy.Validate(network); err != nil {
			t.Fatalf("approved testnet %q was rejected: %v", network, err)
		}
	}
	if err := policy.Validate("ethereum"); err != ErrNetworkNotApproved {
		t.Fatalf("mainnet error = %v, want %v", err, ErrNetworkNotApproved)
	}
	if err := (RoutePolicy{Mode: "self_custody_testnet"}).Validate("ethereum-sepolia"); err != ErrNetworkNotApproved {
		t.Fatalf("disabled testnet error = %v, want %v", err, ErrNetworkNotApproved)
	}
}

func TestStagingPolicyRejectsMainnetForEveryProvider(t *testing.T) {
	policy := RoutePolicy{Mode: "fireblocks", TestnetOnly: true}
	if err := policy.Validate("ethereum"); err != ErrNetworkNotApproved {
		t.Fatalf("mainnet error = %v, want %v", err, ErrNetworkNotApproved)
	}
	if err := policy.Validate("ethereum_sepolia"); err != nil {
		t.Fatalf("testnet error = %v", err)
	}
}
