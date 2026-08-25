package conversions

import (
	"context"
	"testing"

	"github.com/limiance/backend/internal/marketdata"
)

func TestQuotedAmountsAvoidsFloatAndAppliesFee(t *testing.T) {
	net, fee, err := quotedAmounts(100000000, "100", 8, 6, true, 0, 25)
	if err != nil || net != 99750000 || fee != 250000 {
		t.Fatalf("net=%d fee=%d err=%v", net, fee, err)
	}
}

func TestQuotedAmountsBuyBaseUsesAsk(t *testing.T) {
	net, fee, err := quotedAmounts(100000000, "20000", 6, 8, false, 0, 0)
	if err != nil || net != 500000 || fee != 0 {
		t.Fatalf("net=%d fee=%d err=%v", net, fee, err)
	}
}

func TestBuildMarketSymbolUsesAssetPairFallback(t *testing.T) {
	if got := buildMarketSymbol("BTC", "USDC"); got != "BTCUSDC" {
		t.Fatalf("expected BTCUSDC, got %q", got)
	}
	if got := buildMarketSymbol("ETH", "USDC"); got != "ETHUSDC" {
		t.Fatalf("expected ETHUSDC, got %q", got)
	}
	if got := buildMarketSymbol("USDC", "BTC"); got != "BTCUSDC" {
		t.Fatalf("expected BTCUSDC, got %q", got)
	}
}

func TestConversionPriceUsesUSDTBridgeForNonDirectPair(t *testing.T) {
	provider := &stubMarketProvider{prices: map[string]string{"SOLUSDT": "100", "POLUSDT": "0.25"}}
	service := &Service{market: provider}
	price, sellBase, err := service.conversionPrice(context.Background(), "SOL", "POL", "SOLPOL")
	if err != nil || !sellBase || price != "400.000000000000000000" {
		t.Fatalf("price=%q sellBase=%t err=%v", price, sellBase, err)
	}
}

type stubMarketProvider struct{ prices map[string]string }

func (s *stubMarketProvider) SpotTicker(_ context.Context, symbol string) (marketdata.SpotTicker, error) {
	return marketdata.SpotTicker{Symbol: symbol, Bid: s.prices[symbol], Ask: s.prices[symbol], Last: s.prices[symbol]}, nil
}
