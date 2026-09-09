package testmoney

import (
	"context"
	"math/big"
	"sort"
	"strings"

	"github.com/limiance/backend/internal/marketdata"
)

type AssetLookup func(context.Context, string) (symbol, network string, err error)

type NamedSpotProvider struct {
	Name     string
	Provider marketdata.SpotProvider
}

// SpotReferenceSource is used only by the staging administration command. It
// never reads a price supplied by the grant proposer or approver.
type SpotReferenceSource struct {
	lookup    AssetLookup
	providers []NamedSpotProvider
}

func NewSpotReferenceSource(lookup AssetLookup, providers []NamedSpotProvider) *SpotReferenceSource {
	return &SpotReferenceSource{lookup: lookup, providers: providers}
}

func (source *SpotReferenceSource) Observations(ctx context.Context, assetID string) ([]Observation, error) {
	if source == nil || source.lookup == nil {
		return nil, ErrReference
	}
	symbol, network, err := source.lookup(ctx, assetID)
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if err != nil || symbol == "" || symbol == "USDT" || network != "internal_spot" {
		return nil, ErrReference
	}
	providers := make([]NamedSpotProvider, 0, len(source.providers))
	configured := make(map[string]struct{}, len(source.providers))
	for _, named := range source.providers {
		name := strings.ToLower(strings.TrimSpace(named.Name))
		if name == "" || named.Provider == nil {
			continue
		}
		if _, duplicate := configured[name]; duplicate {
			continue
		}
		configured[name] = struct{}{}
		named.Name = name
		providers = append(providers, named)
	}
	type result struct {
		observation Observation
		valid       bool
	}
	results := make(chan result, len(providers))
	for _, named := range providers {
		go func(named NamedSpotProvider) {
			ticker, tickerErr := named.Provider.SpotTicker(ctx, symbol+"USDT")
			if tickerErr != nil || ticker.Symbol != symbol+"USDT" || ticker.ObservedAt.IsZero() {
				results <- result{}
				return
			}
			bid, bidErr := decimalPriceToUSDTAtomic(ticker.Bid)
			ask, askErr := decimalPriceToUSDTAtomic(ticker.Ask)
			if bidErr != nil || askErr != nil || bid.Sign() <= 0 || ask.Cmp(bid) < 0 {
				results <- result{}
				return
			}
			midpoint := ceilDiv(new(big.Int).Add(bid, ask), big.NewInt(2))
			results <- result{valid: true, observation: Observation{Venue: named.Name, PriceUSDTAtomic: midpoint.String(), ObservedAt: ticker.ObservedAt.UTC()}}
		}(named)
	}
	observations := make([]Observation, 0, len(providers))
	seen := make(map[string]struct{}, len(providers))
	for range providers {
		result := <-results
		if !result.valid {
			continue
		}
		if _, duplicate := seen[result.observation.Venue]; duplicate {
			continue
		}
		seen[result.observation.Venue] = struct{}{}
		observations = append(observations, result.observation)
	}
	if len(observations) < 2 {
		return nil, ErrReference
	}
	sort.Slice(observations, func(i, j int) bool { return observations[i].Venue < observations[j].Venue })
	return observations[:2], nil
}

func decimalPriceToUSDTAtomic(value string) (*big.Int, error) {
	if value == "" || strings.TrimSpace(value) != value || strings.HasPrefix(value, "+") || strings.HasPrefix(value, "-") {
		return nil, ErrReference
	}
	parts := strings.Split(value, ".")
	if len(parts) > 2 || parts[0] == "" || (len(parts) == 2 && parts[1] == "") {
		return nil, ErrReference
	}
	for _, part := range parts {
		for _, character := range part {
			if character < '0' || character > '9' {
				return nil, ErrReference
			}
		}
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
	}
	if len(fraction) > 36 || len(parts[0])+len(fraction) > 78 {
		return nil, ErrReference
	}
	digits := strings.TrimLeft(parts[0]+fraction, "0")
	if digits == "" {
		return new(big.Int), nil
	}
	n, ok := new(big.Int).SetString(digits, 10)
	if !ok {
		return nil, ErrReference
	}
	if len(fraction) < 8 {
		n.Mul(n, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(8-len(fraction))), nil))
	} else if len(fraction) > 8 {
		n = ceilDiv(n, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(len(fraction)-8)), nil))
	}
	if len(n.String()) > 78 {
		return nil, ErrReference
	}
	return n, nil
}
