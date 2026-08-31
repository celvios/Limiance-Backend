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
