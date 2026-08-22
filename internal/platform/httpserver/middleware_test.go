package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

var testBrowserOrigins = []string{"https://limiance-main.vercel.app"}

func TestCustomerCORSAllowsExactConfiguredOrigin(t *testing.T) {
	handler := customerCORS(testBrowserOrigins, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	request := httptest.NewRequest(http.MethodGet, "/v1/auth/session", nil)
	request.Header.Set("Origin", "https://limiance-main.vercel.app")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if got := response.Header().Get("Access-Control-Allow-Origin"); got != "https://limiance-main.vercel.app" {
		t.Fatalf("Access-Control-Allow-Origin = %q", got)
	}
	if got := response.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("Access-Control-Allow-Credentials = %q", got)
	}
}

func TestCustomerCORSDeniesUntrustedPreflight(t *testing.T) {
	handler := customerCORS(testBrowserOrigins, http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("denied preflight reached application handler")
	}))
	request := httptest.NewRequest(http.MethodOptions, "/v1/withdrawals", nil)
	request.Header.Set("Origin", "https://attacker.example")
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
	}
}

func TestCustomerCORSAllowsConfiguredPreflightHeaders(t *testing.T) {
	handler := customerCORS(testBrowserOrigins, http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("preflight reached application handler")
	}))
	request := httptest.NewRequest(http.MethodOptions, "/v1/conversions/confirm", nil)
	request.Header.Set("Origin", "https://limiance-main.vercel.app")
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)
	request.Header.Set("Access-Control-Request-Headers", "content-type, idempotency-key")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
	}
}

func TestCSRFOriginCheckRejectsUntrustedUnsafeRequest(t *testing.T) {
	handler := csrfOriginCheck(testBrowserOrigins, http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("untrusted unsafe request reached application handler")
	}))
	request := httptest.NewRequest(http.MethodPost, "/v1/withdrawals", nil)
	request.Header.Set("Origin", "https://attacker.example")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
	}
}
