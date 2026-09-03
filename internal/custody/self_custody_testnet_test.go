package custody

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSelfCustodyTestnetClientUsesIsolatedSignerContract(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/testnet/wallets":
			if r.Method != http.MethodPost {
				t.Fatalf("method = %s", r.Method)
			}
			_, _ = w.Write([]byte(`{"id":"wallet-1"}`))
		case "/v1/testnet/wallets/wallet-1/addresses":
			_, _ = w.Write([]byte(`{"id":"address-1","address":"0x1111111111111111111111111111111111111111","tag":""}`))
		case "/v1/testnet/withdrawals":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["source_wallet_id"] != "wallet-1" || body["asset_id"] != "ETH_SEPOLIA" || body["amount"] != "0.25" || body["external_id"] != "withdrawal-1" {
				t.Fatalf("withdrawal body = %#v", body)
			}
			_, _ = w.Write([]byte(`{"provider_transaction_id":"testnet-transaction-1","status":"submitted"}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client := server.Client()
	client.Transport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // test server only
	provider, err := NewSelfCustodyTestnetClient(server.URL, client)
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := provider.CreateCustomerWallet(context.Background(), "customer-1")
	if err != nil || wallet.ID != "wallet-1" {
		t.Fatalf("wallet = %#v, %v", wallet, err)
	}
	address, err := provider.GetDepositAddress(context.Background(), wallet.ID, "ETH_SEPOLIA", "address-request-1")
	if err != nil || address.ID != "address-1" {
		t.Fatalf("address = %#v, %v", address, err)
	}
	withdrawal, err := provider.CreateWithdrawal(context.Background(), WithdrawalRequest{SourceVaultID: wallet.ID, AssetID: "ETH_SEPOLIA", Destination: "0x2222222222222222222222222222222222222222", Amount: "0.25", ExternalID: "withdrawal-1"})
	if err != nil || withdrawal.ProviderTransactionID != "testnet-transaction-1" {
		t.Fatalf("withdrawal = %#v, %v", withdrawal, err)
	}
}

func TestSelfCustodySignerRequiresHTTPS(t *testing.T) {
	if _, err := NewSelfCustodyTestnetClient("http://localhost:19090", nil); err == nil {
		t.Fatal("HTTP signer URL was accepted")
	}
}
