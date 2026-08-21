package custody

import "testing"

func TestRoutePolicyFailsClosedForSelfCustody(t *testing.T) {
	policy := RoutePolicy{Mode: "self_custody_testnet", SelfCustodyTestnetEnabled: true}
	if err := policy.Validate("ethereum-sepolia"); err != nil {
		t.Fatalf("approved testnet was rejected: %v", err)
	}
	if err := policy.Validate("ethereum"); err != ErrNetworkNotApproved {
		t.Fatalf("mainnet error = %v, want %v", err, ErrNetworkNotApproved)
	}
	if err := (RoutePolicy{Mode: "self_custody_testnet"}).Validate("ethereum-sepolia"); err != ErrNetworkNotApproved {
		t.Fatalf("disabled testnet error = %v, want %v", err, ErrNetworkNotApproved)
	}
}
