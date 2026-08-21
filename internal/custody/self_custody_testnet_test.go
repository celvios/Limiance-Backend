package custody

import (
	"context"
	"crypto/tls"
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
}

func TestSelfCustodySignerRequiresHTTPS(t *testing.T) {
	if _, err := NewSelfCustodyTestnetClient("http://localhost:19090", nil); err == nil {
		t.Fatal("HTTP signer URL was accepted")
	}
}
