package httpserver

import "testing"

func TestCapabilitiesForTier(t *testing.T) {
	cases := []struct {
		name     string
		tier     int16
		deposit  bool
		trade    bool
		withdraw bool
		fiat     bool
	}{
		{name: "unverified", tier: 0, deposit: true, trade: false, withdraw: false, fiat: false},
		{name: "basic", tier: 1, deposit: true, trade: true, withdraw: true, fiat: false},
		{name: "full", tier: 2, deposit: true, trade: true, withdraw: true, fiat: true},
	}

	for _, tc := range cases {
		got := capabilitiesForTier(tc.tier)
		if got.CryptoDeposits != tc.deposit {
			t.Fatalf("tier %d: crypto deposits mismatch: got %v want %v", tc.tier, got.CryptoDeposits, tc.deposit)
		}
		if got.CryptoTrading != tc.trade {
			t.Fatalf("tier %d: crypto trading mismatch: got %v want %v", tc.tier, got.CryptoTrading, tc.trade)
		}
		if got.CryptoWithdrawals != tc.withdraw {
			t.Fatalf("tier %d: crypto withdrawals mismatch: got %v want %v", tc.tier, got.CryptoWithdrawals, tc.withdraw)
		}
		if got.FiatTransfers != tc.fiat {
			t.Fatalf("tier %d: fiat transfers mismatch: got %v want %v", tc.tier, got.FiatTransfers, tc.fiat)
		}
	}
}

func TestLimitsResponseForTier(t *testing.T) {
	cases := []struct {
		name     string
		tier     int16
		wantTier int16
	}{
		{name: "unverified", tier: 0, wantTier: 0},
		{name: "basic", tier: 1, wantTier: 1},
		{name: "full", tier: 2, wantTier: 2},
	}

	for _, tc := range cases {
		got := limitsResponseForTier(tc.tier)
		if got.KYCTier != tc.wantTier {
			t.Fatalf("tier %d: expected kyc tier %d got %d", tc.tier, tc.wantTier, got.KYCTier)
		}
		if got.Capabilities != capabilitiesForTier(tc.tier) {
			t.Fatalf("tier %d: capabilities mismatch", tc.tier)
		}
	}
}
