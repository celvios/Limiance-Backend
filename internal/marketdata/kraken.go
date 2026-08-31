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

type Kraken struct {
	baseURL string
	client  *http.Client
}

func NewKraken(baseURL string, client *http.Client) (*Kraken, error) {
	if baseURL == "" {
		baseURL = "https://api.kraken.com"
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "https" {
		return nil, errors.New("Kraken base URL must use HTTPS")
	}
	if client == nil {
		client = &http.Client{Timeout: 4 * time.Second}
	}
	return &Kraken{baseURL: strings.TrimRight(baseURL, "/"), client: client}, nil
}

func (provider *Kraken) SpotTicker(ctx context.Context, symbol string) (SpotTicker, error) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if !strings.HasSuffix(symbol, "USDT") || len(symbol) <= 4 {
		return SpotTicker{}, ErrUnavailable
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, provider.baseURL+"/0/public/Ticker?pair="+url.QueryEscape(symbol), nil)
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
		Error  []string `json:"error"`
		Result map[string]struct {
			Ask  []string `json:"a"`
			Bid  []string `json:"b"`
			Last []string `json:"c"`
		} `json:"result"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&payload); err != nil || len(payload.Error) != 0 || len(payload.Result) != 1 {
		return SpotTicker{}, ErrUnavailable
	}
	for _, item := range payload.Result {
		if len(item.Bid) == 0 || len(item.Ask) == 0 {
			return SpotTicker{}, ErrUnavailable
		}
		last := ""
		if len(item.Last) != 0 {
			last = item.Last[0]
		}
		return SpotTicker{Symbol: symbol, Bid: item.Bid[0], Ask: item.Ask[0], Last: last, ObservedAt: time.Now().UTC()}, nil
	}
	return SpotTicker{}, ErrUnavailable
}
