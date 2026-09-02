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

type Binance struct {
	baseURL string
	client  *http.Client
}

func NewBinance(baseURL string, client *http.Client) (*Binance, error) {
	if baseURL == "" {
		baseURL = "https://api.binance.com"
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "https" {
		return nil, errors.New("Binance base URL must use HTTPS")
	}
	if client == nil {
		client = &http.Client{Timeout: 4 * time.Second}
	}
	return &Binance{baseURL: strings.TrimRight(baseURL, "/"), client: client}, nil
}

func (provider *Binance) SpotTicker(ctx context.Context, symbol string) (SpotTicker, error) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if !strings.HasSuffix(symbol, "USDT") || len(symbol) <= 4 {
		return SpotTicker{}, ErrUnavailable
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, provider.baseURL+"/api/v3/ticker/bookTicker?symbol="+url.QueryEscape(symbol), nil)
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
	var payload struct {
		Symbol string `json:"symbol"`
		Bid    string `json:"bidPrice"`
		Ask    string `json:"askPrice"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&payload); err != nil || payload.Symbol != symbol || payload.Bid == "" || payload.Ask == "" {
		return SpotTicker{}, ErrUnavailable
	}
	return SpotTicker{Symbol: symbol, Bid: payload.Bid, Ask: payload.Ask, ObservedAt: time.Now().UTC()}, nil
}
