package custody

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFireblocksClientUsesSignedJWTAndDocumentedVaultPaths(t *testing.T) {
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
		verifyFireblocksRequest(t, r, body, &key.PublicKey, "api-user-id", now)
		switch r.URL.Path {
		case "/v1/vault/accounts":
			if r.Method != http.MethodPost || !strings.HasPrefix(r.Header.Get("Idempotency-Key"), "wallet-") || len(r.Header.Get("Idempotency-Key")) > 40 {
				t.Fatalf("unexpected create wallet request: method=%s idempotency=%q", r.Method, r.Header.Get("Idempotency-Key"))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"77"}`))
		case "/v1/vault/accounts/77/ETH_TEST5":
			if r.Header.Get("Idempotency-Key") != "address-request-1" {
				t.Fatalf("unexpected address idempotency key %q", r.Header.Get("Idempotency-Key"))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"ETH_TEST5","address":"0x123","tag":""}`))
		case "/v1/assets":
			if r.Method != http.MethodGet {
				t.Fatalf("unexpected supported-assets method %s", r.Method)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"uuid","legacyId":"BTC_TEST","displayName":"Test Bitcoin","displaySymbol":"tBTC","blockchainId":"test-bitcoin","assetClass":"NATIVE","onchain":{"symbol":"BTC","name":"Bitcoin","decimals":8}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewFireblocksClient(FireblocksConfig{APIKey: "api-user-id", PrivateKey: string(keyPEM), BaseURL: server.URL, HTTPClient: server.Client(), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := client.CreateCustomerWallet(context.Background(), "customer-123")
	if err != nil {
		t.Fatal(err)
	}
	if wallet.ID != "77" {
		t.Fatalf("wallet ID = %q", wallet.ID)
	}
	address, err := client.GetDepositAddress(context.Background(), wallet.ID, "ETH_TEST5", "address-request-1")
	if err != nil {
		t.Fatal(err)
	}
	if address.Address != "0x123" {
		t.Fatalf("address = %q", address.Address)
	}
	supported, err := client.ListSupportedAssets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(supported) != 1 || supported[0].LegacyID != "BTC_TEST" || supported[0].Onchain.Decimals != 8 {
		t.Fatalf("supported assets = %+v", supported)
	}
	if got := DeterministicIdempotencyKey("deposit-address", strings.Repeat("u", 36), strings.Repeat("a", 36)); len(got) != 40 || !strings.HasPrefix(got, "deposit-address-") || got != DeterministicIdempotencyKey("deposit-address", strings.Repeat("u", 36), strings.Repeat("a", 36)) {
		t.Fatalf("provider-safe idempotency key = %q", got)
	}
}

func verifyFireblocksRequest(t *testing.T, request *http.Request, body []byte, key *rsa.PublicKey, apiKey string, now time.Time) {
	t.Helper()
	if request.Header.Get("X-API-Key") != apiKey {
		t.Fatalf("X-API-Key = %q", request.Header.Get("X-API-Key"))
	}
	parts := strings.Split(strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer "), ".")
	if len(parts) != 3 {
		t.Fatalf("invalid Authorization header")
	}
	signingInput := parts[0] + "." + parts[1]
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(signingInput))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, hash[:], signature); err != nil {
		t.Fatalf("JWT signature: %v", err)
	}
	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims struct {
		URI      string `json:"uri"`
		Nonce    string `json:"nonce"`
		IAT      int64  `json:"iat"`
		EXP      int64  `json:"exp"`
		Sub      string `json:"sub"`
		BodyHash string `json:"bodyHash"`
	}
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	if claims.URI != request.URL.RequestURI() || claims.Nonce == "" || claims.IAT != now.Unix() || claims.EXP != now.Add(29*time.Second).Unix() || claims.Sub != apiKey || claims.BodyHash != hex.EncodeToString(digest[:]) {
		t.Fatalf("invalid Fireblocks claims: %+v", claims)
	}
}
