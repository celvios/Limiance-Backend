package custody

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWebhookVerifierValidatesDetachedRS512JWS(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/jwks.json" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"keys":[{"kty":"RSA","kid":"key-1","use":"sig","alg":"RS512","n":"` + base64.RawURLEncoding.EncodeToString(key.N.Bytes()) + `","e":"` + base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()) + `"}]}`))
	}))
	defer server.Close()
	verifier, err := NewWebhookVerifier(server.URL+"/.well-known/jwks.json", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"type":"TRANSACTION_CREATED","data":{"id":"tx-1"}}`)
	signature := detachedJWS(t, key, "key-1", body)
	if err := verifier.Verify(context.Background(), body, signature); err != nil {
		t.Fatalf("verify valid JWS: %v", err)
	}
	if err := verifier.Verify(context.Background(), []byte(`{"type":"changed"}`), signature); err == nil {
		t.Fatal("expected tampered payload rejection")
	}
}

func detachedJWS(t *testing.T, key *rsa.PrivateKey, keyID string, body []byte) string {
	t.Helper()
	header, err := json.Marshal(map[string]string{"alg": "RS512", "kid": keyID})
	if err != nil {
		t.Fatal(err)
	}
	encodedHeader := base64.RawURLEncoding.EncodeToString(header)
	input := encodedHeader + "." + base64.RawURLEncoding.EncodeToString(body)
	hash := sha512.Sum512([]byte(input))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA512, hash[:])
	if err != nil {
		t.Fatal(err)
	}
	return encodedHeader + ".." + base64.RawURLEncoding.EncodeToString(signature)
}
