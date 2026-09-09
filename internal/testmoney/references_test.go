package testmoney

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/limiance/backend/internal/marketdata"
)

type referenceProvider struct {
	ticker marketdata.SpotTicker
	err    error
}

func (provider referenceProvider) SpotTicker(context.Context, string) (marketdata.SpotTicker, error) {
	return provider.ticker, provider.err
}

func TestSpotReferenceSourceUsesTwoDistinctServerProviders(t *testing.T) {
	now := time.Now().UTC()
	lookup := func(context.Context, string) (string, string, error) { return "ETH", "internal_spot", nil }
	source := NewSpotReferenceSource(lookup, []NamedSpotProvider{
		{Name: "Bybit", Provider: referenceProvider{ticker: marketdata.SpotTicker{Symbol: "ETHUSDT", Bid: "2499.999999999", Ask: "2500.000000001", ObservedAt: now}}},
		{Name: "Binance", Provider: referenceProvider{ticker: marketdata.SpotTicker{Symbol: "ETHUSDT", Bid: "2501", Ask: "2501.00000000", ObservedAt: now}}},
		{Name: "BYBIT", Provider: referenceProvider{ticker: marketdata.SpotTicker{Symbol: "ETHUSDT", Bid: "1", Ask: "1", ObservedAt: now}}},
	})
	observations, err := source.Observations(context.Background(), "asset-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(observations) != 2 || observations[0].Venue != "binance" || observations[0].PriceUSDTAtomic != "250100000000" || observations[1].Venue != "bybit" || observations[1].PriceUSDTAtomic != "250000000001" {
		t.Fatalf("unexpected observations: %#v", observations)
	}
}

func TestSpotReferenceSourceCanonicalizesCatalogSymbolCase(t *testing.T) {
	now := time.Now().UTC()
	lookup := func(context.Context, string) (string, string, error) { return "stETH", "internal_spot", nil }
	provider := referenceProvider{ticker: marketdata.SpotTicker{Symbol: "STETHUSDT", Bid: "2500", Ask: "2500", ObservedAt: now}}
	observations, err := NewSpotReferenceSource(lookup, []NamedSpotProvider{{Name: "one", Provider: provider}, {Name: "two", Provider: provider}}).Observations(context.Background(), "asset-1")
	if err != nil || len(observations) != 2 {
		t.Fatalf("mixed-case catalog symbol was not canonicalized: %#v, %v", observations, err)
	}
}

func TestSpotReferenceSourceFailsClosed(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name      string
		network   string
		symbol    string
		providers []NamedSpotProvider
	}{
		{name: "custody asset", network: "ethereum_sepolia", symbol: "ETH", providers: []NamedSpotProvider{{Name: "one", Provider: referenceProvider{ticker: marketdata.SpotTicker{Symbol: "ETHUSDT", Bid: "1", Ask: "1", ObservedAt: now}}}, {Name: "two", Provider: referenceProvider{ticker: marketdata.SpotTicker{Symbol: "ETHUSDT", Bid: "1", Ask: "1", ObservedAt: now}}}}},
		{name: "stablecoin self pair", network: "internal_spot", symbol: "USDT"},
		{name: "one venue", network: "internal_spot", symbol: "ETH", providers: []NamedSpotProvider{{Name: "one", Provider: referenceProvider{ticker: marketdata.SpotTicker{Symbol: "ETHUSDT", Bid: "1", Ask: "1", ObservedAt: now}}}, {Name: "two", Provider: referenceProvider{err: errors.New("offline")}}}},
		{name: "crossed quote", network: "internal_spot", symbol: "ETH", providers: []NamedSpotProvider{{Name: "one", Provider: referenceProvider{ticker: marketdata.SpotTicker{Symbol: "ETHUSDT", Bid: "2", Ask: "1", ObservedAt: now}}}, {Name: "two", Provider: referenceProvider{ticker: marketdata.SpotTicker{Symbol: "ETHUSDT", Bid: "1", Ask: "1", ObservedAt: now}}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lookup := func(context.Context, string) (string, string, error) { return test.symbol, test.network, nil }
			if _, err := NewSpotReferenceSource(lookup, test.providers).Observations(context.Background(), "asset-1"); !errors.Is(err, ErrReference) {
				t.Fatalf("expected ErrReference, got %v", err)
			}
		})
	}
}

func TestDecimalPriceToUSDTAtomicIsExactAndRoundsUp(t *testing.T) {
	tests := map[string]string{
		"2500":          "250000000000",
		"0.000000001":   "1",
		"1.234567891":   "123456790",
		"0001.00000000": "100000000",
	}
	for input, expected := range tests {
		actual, err := decimalPriceToUSDTAtomic(input)
		if err != nil || actual.String() != expected {
			t.Fatalf("%s: got %v, %v", input, actual, err)
		}
	}
	for _, invalid := range []string{"", " 1", "+1", "-1", "1e3", "1.", ".1"} {
		if _, err := decimalPriceToUSDTAtomic(invalid); !errors.Is(err, ErrReference) {
			t.Fatalf("%q: expected ErrReference, got %v", invalid, err)
		}
	}
}
