package custody

import "testing"

func TestCanonicalNetworkUsesSinglePersistentCatalogCode(t *testing.T) {
	tests := map[string]string{
		"ethereum-sepolia": "ethereum_sepolia",
		"Ethereum_Sepolia": "ethereum_sepolia",
		"bitcoin-testnet":  "bitcoin_testnet4",
		"BTC":              "btc",
	}
	for input, want := range tests {
		if got := canonicalNetwork(input); got != want {
			t.Errorf("canonicalNetwork(%q) = %q, want %q", input, got, want)
		}
	}
}
