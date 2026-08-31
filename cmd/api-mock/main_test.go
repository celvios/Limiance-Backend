package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMockServerMatchesAtomicStringContract(t *testing.T) {
	response := httptest.NewRecorder()
	newMockHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v2/market/orderbook/BTCUSDT", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"5000000000000"`) {
		t.Fatalf("mock response status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestMockAllTickersSupportsFrontendRankings(t *testing.T) {
	response := httptest.NewRecorder()
	newMockHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v2/market/tickers", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"change_bps_24h":"204"`) {
		t.Fatalf("unexpected all-tickers response: %d %s", response.Code, response.Body.String())
	}
}

func TestMockP2POffersUseAtomicCryptoAndDecimalFiatStrings(t *testing.T) {
	response := httptest.NewRecorder()
	newMockHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v2/p2p/offers", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"amount_atomic":"100000000"`) || !strings.Contains(response.Body.String(), `"fiat_amount":"150000.00000000"`) {
		t.Fatalf("unexpected P2P offers response: %d %s", response.Code, response.Body.String())
	}
}
