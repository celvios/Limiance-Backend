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

type Coinbase struct {
	baseURL string
	client  *http.Client
}

func NewCoinbase(baseURL string, client *http.Client) (*Coinbase, error) {
	if baseURL == "" {
		baseURL = "https://api.exchange.coinbase.com"
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "https" {
		return nil, errors.New("Coinbase base URL must use HTTPS")
	}
	if client == nil {
		client = &http.Client{Timeout: 4 * time.Second}
	}
	return &Coinbase{baseURL: strings.TrimRight(baseURL, "/"), client: client}, nil
}

func (provider *Coinbase) SpotTicker(ctx context.Context, symbol string) (SpotTicker, error) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if !strings.HasSuffix(symbol, "USDT") || len(symbol) <= 4 {
		return SpotTicker{}, ErrUnavailable
	}
	product := strings.TrimSuffix(symbol, "USDT") + "-USDT"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, provider.baseURL+"/products/"+url.PathEscape(product)+"/ticker", nil)
	if err != nil {
		return SpotTicker{}, err
	}
	response, err := provider.client.Do(request)
	if err != nil {
		return SpotTicker{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return SpotTicker{}, fmt.Errorf("%w: HTTP %d", ErrUnavailable, response.StatusCode)
	}
	var payload struct{ Bid, Ask, Price string }
	if err = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&payload); err != nil || payload.Bid == "" || payload.Ask == "" {
		return SpotTicker{}, ErrUnavailable
	}
	return SpotTicker{Symbol: symbol, Bid: payload.Bid, Ask: payload.Ask, Last: payload.Price, ObservedAt: time.Now().UTC()}, nil
}
