package custody

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestProviderAmountToAtomicIsExactAndBounded(t *testing.T) {
	valid := []struct {
		amount   string
		decimals int16
		want     string
	}{
		{"0", 18, "0"},
		{"0.000000000000000001", 18, "1"},
		{"12.3400", 6, "12340000"},
		{"001.2", 2, "120"},
		{"999999999999999999999999999999999999999999999999999999999999999999999999999999", 0,
			"999999999999999999999999999999999999999999999999999999999999999999999999999999"},
	}
	for _, tc := range valid {
		got, err := ProviderAmountToAtomic(tc.amount, tc.decimals)
		if err != nil || got != tc.want {
			t.Fatalf("%q/%d = %q, %v", tc.amount, tc.decimals, got, err)
		}
	}
	invalid := []struct {
		amount   string
		decimals int16
	}{
		{"", 18}, {" 1", 18}, {"1 ", 18}, {"+1", 18}, {"-1", 18},
		{".1", 18}, {"1.", 18}, {"1.2.3", 18}, {"1e3", 18},
		{"0.0000001", 6}, {"1", -1}, {"1", 37},
		{"1000000000000000000000000000000000000000000000000000000000000000000000000000000", 0},
	}
	for _, tc := range invalid {
		if got, err := ProviderAmountToAtomic(tc.amount, tc.decimals); err == nil {
			t.Fatalf("accepted %q/%d as %q", tc.amount, tc.decimals, got)
		}
	}
	if _, err := AtomicToProviderAmount("1", 37); err == nil {
		t.Fatal("atomic conversion accepted unsupported precision")
	}
}

func TestFireblocksWithdrawalObserverUsesDocumentedReadPaths(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	now := time.Unix(1_700_000_000, 0).UTC()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		verifyFireblocksRequest(t, r, body, &key.PublicKey, "observer-api-user", now)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.RequestURI() {
		case "/v1/vault/accounts/77/USDC_TEST5":
			if r.Method != http.MethodGet {
				t.Fatalf("balance method=%s", r.Method)
			}
			_, _ = w.Write([]byte(`{"id":"USDC_TEST5","available":"12.340001","lockedAmount":"0.2","blockHeight":"123","blockHash":"abc"}`))
		case "/v1/transactions/estimate_fee":
			if r.Method != http.MethodPost {
				t.Fatalf("estimate method=%s", r.Method)
			}
			var request map[string]any
			if err := json.Unmarshal(body, &request); err != nil {
				t.Fatal(err)
			}
			if request["assetId"] != "USDC_TEST5" || request["amount"] != "1.25" || request["externalTxId"] != nil {
				t.Fatalf("estimate request=%v", request)
			}
			_, _ = w.Write([]byte(`{"low":{"networkFee":"0.0001"},"medium":{"networkFee":"0.0002"},"high":{"networkFee":"0.0003"}}`))
		case "/v1/transactions/external_tx_id/withdrawal-1":
			if r.Method != http.MethodGet {
				t.Fatalf("lookup method=%s", r.Method)
			}
			_, _ = w.Write([]byte(`{"id":"provider-1","externalTxId":"withdrawal-1","status":"BROADCASTING","txHash":"hash-1"}`))
		case "/v1/transactions/external_tx_id/not-found":
			http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := NewFireblocksClient(FireblocksConfig{APIKey: "observer-api-user", PrivateKey: string(keyPEM), BaseURL: server.URL, HTTPClient: server.Client(), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	balance, err := client.GetVaultAssetBalance(context.Background(), "77", "USDC_TEST5")
	if err != nil || balance.Available != "12.340001" || balance.Locked != "0.2" || balance.BlockHeight != "123" || balance.BlockHash != "abc" {
		t.Fatalf("balance=%+v err=%v", balance, err)
	}
	if atomic, err := ProviderAmountToAtomic(balance.Available, 6); err != nil || atomic != "12340001" {
		t.Fatalf("available=%q err=%v", atomic, err)
	}
	request := WithdrawalRequest{SourceVaultID: "77", AssetID: "USDC_TEST5", Destination: "0x123", DestinationTag: "memo", Amount: "1.25", ExternalID: "ignored-for-estimate"}
	fee, err := client.EstimateWithdrawalFee(context.Background(), request)
	if err != nil || fee.NetworkFee != "0.0002" {
		t.Fatalf("fee=%+v err=%v", fee, err)
	}
	lookup, err := client.FindWithdrawalByExternalID(context.Background(), "withdrawal-1")
	if err != nil || !lookup.Found || lookup.ProviderTransactionID != "provider-1" || lookup.ExternalID != "withdrawal-1" || lookup.Status != "BROADCASTING" || lookup.TransactionHash != "hash-1" {
		t.Fatalf("lookup=%+v err=%v", lookup, err)
	}
	missing, err := client.FindWithdrawalByExternalID(context.Background(), "not-found")
	if err != nil || missing.Found || missing.ExternalID != "not-found" {
		t.Fatalf("missing=%+v err=%v", missing, err)
	}
}

func TestFireblocksWithdrawalObserverFailsClosed(t *testing.T) {
	for _, tc := range []struct{ name, path, response string }{
		{"balance_missing_available", "/v1/vault/accounts/77/ASSET", `{"id":"ASSET"}`},
		{"fee_missing_network_fee", "/v1/transactions/estimate_fee", `{"medium":{"gasPrice":"1"}}`},
		{"lookup_identity_mismatch", "/v1/transactions/external_tx_id/want", `{"id":"provider","externalTxId":"other","status":"SUBMITTED"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key, err := rsa.GenerateKey(rand.Reader, 2048)
			if err != nil {
				t.Fatal(err)
			}
			keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.path {
					t.Fatalf("path=%s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.response))
			}))
			defer server.Close()
			client, err := NewFireblocksClient(FireblocksConfig{APIKey: "key", PrivateKey: string(keyPEM), BaseURL: server.URL, HTTPClient: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			switch tc.name {
			case "balance_missing_available":
				_, err = client.GetVaultAssetBalance(context.Background(), "77", "ASSET")
			case "fee_missing_network_fee":
				_, err = client.EstimateWithdrawalFee(context.Background(), WithdrawalRequest{SourceVaultID: "77", AssetID: "ASSET", Destination: "address", Amount: "1"})
			default:
				_, err = client.FindWithdrawalByExternalID(context.Background(), "want")
			}
			if err == nil {
				t.Fatal("accepted incomplete provider response")
			}
		})
	}
}
