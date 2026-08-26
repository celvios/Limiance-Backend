package httpserver

import (
	"math/big"
	"net/http"
	"strings"

	"github.com/limiance/backend/internal/marketdata"
)

type MarketHandler struct {
	provider marketdata.SpotProvider
}

var fiatPerUSD = map[string]string{
	"USD": "1", "AOA": "920", "ARS": "1400", "AUD": "1.54", "BIF": "2950",
	"BRL": "5.45", "BWP": "13.7", "CAD": "1.36", "CHF": "0.88", "CNY": "7.18",
	"CVE": "101", "DJF": "178", "DZD": "130", "EGP": "48.5", "ERN": "15",
	"ETB": "135", "EUR": "0.92", "GBP": "0.79", "GHS": "15.6", "GMD": "72",
	"GNF": "8600", "HKD": "7.8", "JPY": "150", "KES": "141", "KMF": "453",
	"LRD": "193", "LSL": "18.2", "LYD": "4.8", "MAD": "9.9", "MGA": "4500",
	"MRU": "39.5", "MUR": "46", "MWK": "1740", "MZN": "63.9", "NAD": "18.2",
	"NGN": "1560", "NZD": "1.68", "RWF": "1450", "SCR": "13.5", "SDG": "600",
	"SLL": "23000", "SOS": "570", "SSP": "1300", "STN": "22.5", "SZL": "18.2",
	"TND": "3.1", "TRY": "41", "TWD": "32.5", "TZS": "2700", "UAH": "41",
	"UGX": "3700", "XAF": "604", "XOF": "604", "ZAR": "18.2", "ZMW": "27", "ZWL": "26",
}

func NewMarketHandler(provider marketdata.SpotProvider) *MarketHandler {
	return &MarketHandler{provider: provider}
}

func (h *MarketHandler) Prices(w http.ResponseWriter, r *http.Request) {
	displayCurrency := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("currency")))
	if displayCurrency == "" {
		displayCurrency = "USD"
	}
	usdRate, ok := new(big.Rat).SetString(fiatPerUSD[displayCurrency])
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported_display_currency"})
		return
	}
	prices := make(map[string]string)
	usdPrices := make(map[string]string)
	for _, raw := range strings.Split(r.URL.Query().Get("assets"), ",") {
		asset := strings.ToUpper(strings.TrimSpace(raw))
		if asset == "" {
			continue
		}
		if asset == "USD" || asset == "USDC" || asset == "USDT" {
			usdPrices[asset] = "1"
			prices[asset] = usdRate.FloatString(8)
			continue
		}
		ticker, err := h.provider.SpotTicker(r.Context(), asset+"USDT")
		if err == nil && ticker.Last != "" {
			price, valid := new(big.Rat).SetString(ticker.Last)
			if valid && price.Sign() > 0 {
				usdPrices[asset] = price.FloatString(18)
				prices[asset] = new(big.Rat).Mul(price, usdRate).FloatString(8)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"prices": prices, "usd_prices": usdPrices, "currency": displayCurrency, "usd_to_currency": usdRate.FloatString(8)})
}
