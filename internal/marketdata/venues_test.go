package marketdata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCoinbaseSpotTicker(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/products/BTC-USDT/ticker" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"bid":"100.00","ask":"102.00","price":"101.00"}`))
	}))
	defer server.Close()
	provider, err := NewCoinbase(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ticker, err := provider.SpotTicker(context.Background(), "btcusdt")
	if err != nil || ticker.Bid != "100.00" || ticker.Ask != "102.00" {
		t.Fatalf("ticker=%+v err=%v", ticker, err)
	}
}

func TestKrakenSpotTicker(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("pair") != "BTCUSDT" {
			t.Fatalf("unexpected pair %s", r.URL.Query().Get("pair"))
		}
		_, _ = w.Write([]byte(`{"error":[],"result":{"XBTUSDT":{"a":["102.00","1","1"],"b":["100.00","1","1"],"c":["101.00","1"]}}}`))
	}))
	defer server.Close()
	provider, err := NewKraken(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ticker, err := provider.SpotTicker(context.Background(), "BTCUSDT")
	if err != nil || ticker.Bid != "100.00" || ticker.Ask != "102.00" {
		t.Fatalf("ticker=%+v err=%v", ticker, err)
	}
}

func TestBinanceSpotTicker(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/ticker/bookTicker" || r.URL.Query().Get("symbol") != "BTCUSDT" {
			t.Fatalf("unexpected request %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","bidPrice":"100.00","askPrice":"102.00"}`))
	}))
	defer server.Close()
	provider, err := NewBinance(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ticker, err := provider.SpotTicker(context.Background(), "btcusdt")
	if err != nil || ticker.Bid != "100.00" || ticker.Ask != "102.00" {
		t.Fatalf("ticker=%+v err=%v", ticker, err)
	}
}

func TestGateSpotTicker(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/spot/tickers" || r.URL.Query().Get("currency_pair") != "BTC_USDT" {
			t.Fatalf("unexpected request %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`[{"currency_pair":"BTC_USDT","highest_bid":"100.00","lowest_ask":"102.00","last":"101.00"}]`))
	}))
	defer server.Close()
	provider, err := NewGate(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ticker, err := provider.SpotTicker(context.Background(), "btcusdt")
	if err != nil || ticker.Bid != "100.00" || ticker.Ask != "102.00" || ticker.Last != "101.00" {
		t.Fatalf("ticker=%+v err=%v", ticker, err)
	}
}

func TestAdditionalVenuesFailClosed(t *testing.T) {
	for name, create := range map[string]func(string, *http.Client) (SpotProvider, error){
		"binance": func(url string, client *http.Client) (SpotProvider, error) { return NewBinance(url, client) },
		"gate":    func(url string, client *http.Client) (SpotProvider, error) { return NewGate(url, client) },
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := create("http://insecure.example", nil); err == nil {
				t.Fatal("accepted insecure reference URL")
			}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
			}))
			defer server.Close()
			provider, err := create(server.URL, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			if _, err = provider.SpotTicker(context.Background(), "BTCUSDT"); err == nil {
				t.Fatal("accepted unavailable venue response")
			}
		})
	}
}
