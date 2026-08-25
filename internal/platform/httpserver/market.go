package httpserver

import (
	"net/http"
	"strings"

	"github.com/limiance/backend/internal/marketdata"
)

type MarketHandler struct {
	provider marketdata.SpotProvider
}

func NewMarketHandler(provider marketdata.SpotProvider) *MarketHandler {
	return &MarketHandler{provider: provider}
}

func (h *MarketHandler) Prices(w http.ResponseWriter, r *http.Request) {
	prices := make(map[string]string)
	for _, raw := range strings.Split(r.URL.Query().Get("assets"), ",") {
		asset := strings.ToUpper(strings.TrimSpace(raw))
		if asset == "" {
			continue
		}
		if asset == "USD" || asset == "USDC" || asset == "USDT" {
			prices[asset] = "1"
			continue
		}
		ticker, err := h.provider.SpotTicker(r.Context(), asset+"USDT")
		if err == nil && ticker.Last != "" {
			prices[asset] = ticker.Last
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"prices": prices})
}
