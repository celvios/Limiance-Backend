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

type Gate struct {
	baseURL string
	client  *http.Client
}

func NewGate(baseURL string, client *http.Client) (*Gate, error) {
	if baseURL == "" {
		baseURL = "https://api.gateio.ws"
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "https" {
		return nil, errors.New("Gate base URL must use HTTPS")
	}
	if client == nil {
		client = &http.Client{Timeout: 4 * time.Second}
	}
	return &Gate{baseURL: strings.TrimRight(baseURL, "/"), client: client}, nil
}

func (provider *Gate) SpotTicker(ctx context.Context, symbol string) (SpotTicker, error) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if !strings.HasSuffix(symbol, "USDT") || len(symbol) <= 4 {
		return SpotTicker{}, ErrUnavailable
	}
	base := strings.TrimSuffix(symbol, "USDT")
	pair := base + "_USDT"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, provider.baseURL+"/api/v4/spot/tickers?currency_pair="+url.QueryEscape(pair), nil)
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
	var payload []struct {
		Pair string `json:"currency_pair"`
		Bid  string `json:"highest_bid"`
		Ask  string `json:"lowest_ask"`
		Last string `json:"last"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&payload); err != nil || len(payload) != 1 || payload[0].Pair != pair || payload[0].Bid == "" || payload[0].Ask == "" {
		return SpotTicker{}, ErrUnavailable
	}
	return SpotTicker{Symbol: symbol, Bid: payload[0].Bid, Ask: payload[0].Ask, Last: payload[0].Last, ObservedAt: time.Now().UTC()}, nil
}
