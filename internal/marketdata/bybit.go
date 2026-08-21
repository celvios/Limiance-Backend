// Package marketdata contains read-only market-data adapters. It never places
// orders with an external venue: Phase 1 Convert settles against Limiance's own
// configured treasury after it obtains an independently controlled quote.
package marketdata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var ErrUnavailable = errors.New("market data unavailable")

type SpotTicker struct {
	Symbol     string
	Bid        string
	Ask        string
	Last       string
	ObservedAt time.Time
}

type SpotProvider interface {
	SpotTicker(context.Context, string) (SpotTicker, error)
}

type Bybit struct {
	baseURL string
	client  *http.Client
}

func NewBybit(baseURL string, client *http.Client) (*Bybit, error) {
	if baseURL == "" {
		baseURL = "https://api.bybit.com"
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "https" {
		return nil, errors.New("Bybit base URL must use HTTPS")
	}
	if client == nil {
		client = &http.Client{Timeout: 4 * time.Second}
	}
	return &Bybit{baseURL: strings.TrimRight(baseURL, "/"), client: client}, nil
}

// SpotTicker uses Bybit V5's public ticker endpoint. It uses best bid/ask,
// not last price, so a customer buy/sell quote can be conservative.
func (b *Bybit) SpotTicker(ctx context.Context, symbol string) (SpotTicker, error) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if symbol == "" {
		return SpotTicker{}, ErrUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.baseURL+"/v5/market/tickers?category=spot&symbol="+url.QueryEscape(symbol), nil)
	if err != nil {
		return SpotTicker{}, err
	}
	response, err := b.client.Do(req)
	if err != nil {
		return SpotTicker{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return SpotTicker{}, fmt.Errorf("%w: HTTP %d", ErrUnavailable, response.StatusCode)
	}
	var payload struct {
		RetCode int `json:"retCode"`
		Result  struct {
			List []struct {
				Symbol string `json:"symbol"`
				Bid    string `json:"bid1Price"`
				Ask    string `json:"ask1Price"`
				Last   string `json:"lastPrice"`
			} `json:"list"`
		} `json:"result"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&payload); err != nil {
		return SpotTicker{}, fmt.Errorf("%w: invalid response", ErrUnavailable)
	}
	if payload.RetCode != 0 || len(payload.Result.List) != 1 {
		return SpotTicker{}, ErrUnavailable
	}
	item := payload.Result.List[0]
	if item.Symbol != symbol || item.Bid == "" || item.Ask == "" {
		return SpotTicker{}, ErrUnavailable
	}
	return SpotTicker{Symbol: item.Symbol, Bid: item.Bid, Ask: item.Ask, Last: item.Last, ObservedAt: time.Now().UTC()}, nil
}
