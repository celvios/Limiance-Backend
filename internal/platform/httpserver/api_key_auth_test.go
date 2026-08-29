package httpserver

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/limiance/backend/internal/datamanager"
	"github.com/limiance/backend/internal/security/envelope"
)

const testEnvelopeKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="

type apiKeyRepositoryStub struct {
	authentication datamanager.APIKeyAuthentication
	mu             sync.Mutex
	nonces         map[string]struct{}
}

func (stub *apiKeyRepositoryStub) APIKeyAuthentication(context.Context, []byte) (datamanager.APIKeyAuthentication, error) {
	return stub.authentication, nil
}
func (stub *apiKeyRepositoryStub) MarkAPIKeyUsed(context.Context, []byte) error { return nil }
func (stub *apiKeyRepositoryStub) ConsumeAPIKeyNonce(_ context.Context, _ []byte, nonce []byte, _ time.Time) (bool, error) {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	key := string(nonce)
	if _, exists := stub.nonces[key]; exists {
		return false, nil
	}
	stub.nonces[key] = struct{}{}
	return true, nil
}

func TestV2APIKeyAuthenticationRejectsTamperExpiryAndReplay(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	secret := "test-api-secret"
	sealed, err := envelope.Seal(testEnvelopeKey, secret)
	if err != nil {
		t.Fatal(err)
	}
	repository := &apiKeyRepositoryStub{authentication: datamanager.APIKeyAuthentication{UserID: "user-1", UID: 1, Email: "user@example.test", AccountID: "account-1", AccountKind: "uta", Scope: "trade", SecretCiphertext: sealed}, nonces: make(map[string]struct{})}
	authenticator := apiKeyAuthenticator{data: repository, encryptionKey: testEnvelopeKey, now: func() time.Time { return now }}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := principalFromContext(r)
		if !ok || principal.APIKeyScope != "trade" {
			t.Error("API principal missing")
		}
		w.WriteHeader(http.StatusNoContent)
	})

	success := signedV2Request(t, now, secret, "nonce-1234567890", "limit=10", `{"pair":"BTCUSDT"}`)
	response := httptest.NewRecorder()
	authenticator.authenticate(next).ServeHTTP(response, success)
	if response.Code != http.StatusNoContent {
		t.Fatalf("valid signature status=%d body=%s", response.Code, response.Body.String())
	}

	replay := signedV2Request(t, now, secret, "nonce-1234567890", "limit=10", `{"pair":"BTCUSDT"}`)
	response = httptest.NewRecorder()
	authenticator.authenticate(next).ServeHTTP(response, replay)
	assertErrorCode(t, response, http.StatusConflict, "API_NONCE_REPLAY")

	tampered := signedV2Request(t, now, secret, "nonce-abcdefghijk", "limit=10", `{"pair":"BTCUSDT"}`)
	tampered.URL.RawQuery = "limit=100"
	response = httptest.NewRecorder()
	authenticator.authenticate(next).ServeHTTP(response, tampered)
	assertErrorCode(t, response, http.StatusUnauthorized, "INVALID_API_SIGNATURE")

	expired := signedV2Request(t, now.Add(-31*time.Second), secret, "nonce-expired-1234", "", ``)
	response = httptest.NewRecorder()
	authenticator.authenticate(next).ServeHTTP(response, expired)
	assertErrorCode(t, response, http.StatusUnauthorized, "INVALID_API_SIGNATURE")
}

func TestV2APIKeyAuthenticationEnforcesIPAllowlist(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	sealed, _ := envelope.Seal(testEnvelopeKey, "secret")
	repository := &apiKeyRepositoryStub{authentication: datamanager.APIKeyAuthentication{SecretCiphertext: sealed, IPWhitelist: []string{"192.0.2.10"}}, nonces: make(map[string]struct{})}
	request := signedV2Request(t, now, "secret", "nonce-ip-12345678", "", ``)
	request.RemoteAddr = "192.0.2.11:1234"
	response := httptest.NewRecorder()
	apiKeyAuthenticator{data: repository, encryptionKey: testEnvelopeKey, now: func() time.Time { return now }}.authenticate(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("disallowed IP reached handler") })).ServeHTTP(response, request)
	assertErrorCode(t, response, http.StatusForbidden, "API_IP_NOT_ALLOWED")
}

func TestAPIIPAllowlistNormalizesAddressesAndCIDRs(t *testing.T) {
	values, err := normalizeAPIIPWhitelist([]string{"192.0.2.10", "192.0.2.0/24", "192.0.2.10"})
	if err != nil || len(values) != 2 || !apiIPAllowed(values, "192.0.2.25") || apiIPAllowed(values, "198.51.100.1") {
		t.Fatalf("normalized=%v err=%v", values, err)
	}
	if _, err = normalizeAPIIPWhitelist([]string{"not-an-ip"}); err == nil {
		t.Fatal("invalid allowlist entry accepted")
	}
}

func signedV2Request(t *testing.T, at time.Time, secret, nonce, rawQuery, body string) *http.Request {
	t.Helper()
	path := "/v2/orders"
	if rawQuery != "" {
		path += "?" + rawQuery
	}
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	timestamp := strconv.FormatInt(at.Unix(), 10)
	request.Header.Set("X-API-Key", "lm_test")
	request.Header.Set("X-API-Timestamp", timestamp)
	request.Header.Set("X-API-Nonce", nonce)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(v2SignaturePayload(timestamp, nonce, request.Method, request.URL.EscapedPath(), request.URL.RawQuery, []byte(body)))
	request.Header.Set("X-API-Signature", hex.EncodeToString(mac.Sum(nil)))
	return request
}
