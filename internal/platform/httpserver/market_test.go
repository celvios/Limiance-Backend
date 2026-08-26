package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/limiance/backend/internal/marketdata"
)

type marketProviderStub struct{}

func (marketProviderStub) SpotTicker(context.Context, string) (marketdata.SpotTicker, error) {
	return marketdata.SpotTicker{Last: "100"}, nil
}

func TestMarketPricesConvertsPrimaryAndReturnsUSDSecondaryPrices(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/v1/market/prices?currency=ARS&assets=BTC,USDT", nil)
	response := httptest.NewRecorder()

	NewMarketHandler(marketProviderStub{}).Prices(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
	var body struct {
		Prices       map[string]string `json:"prices"`
		USDPrices    map[string]string `json:"usd_prices"`
		Currency     string            `json:"currency"`
		USDToCurrency string           `json:"usd_to_currency"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Currency != "ARS" || body.USDToCurrency != "1400.00000000" {
		t.Fatalf("unexpected currency response: %+v", body)
	}
	if body.Prices["BTC"] != "140000.00000000" || body.Prices["USDT"] != "1400.00000000" {
		t.Fatalf("unexpected primary prices: %+v", body.Prices)
	}
	if body.USDPrices["BTC"] != "100.000000000000000000" || body.USDPrices["USDT"] != "1" {
		t.Fatalf("unexpected secondary prices: %+v", body.USDPrices)
	}
}

func TestMarketPricesRejectsUnsupportedCurrency(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/v1/market/prices?currency=XYZ&assets=BTC", nil)
	response := httptest.NewRecorder()

	NewMarketHandler(marketProviderStub{}).Prices(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", response.Code)
	}
}