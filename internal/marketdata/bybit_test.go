package marketdata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBybitSpotTicker(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("category") != "spot" || r.URL.Query().Get("symbol") != "BTCUSDT" {
			t.Fatal("unexpected query")
		}
		_, _ = w.Write([]byte(`{"retCode":0,"result":{"list":[{"symbol":"BTCUSDT","bid1Price":"100","ask1Price":"101","lastPrice":"100.5"}]}}`))
	}))
	defer server.Close()
	client := server.Client()
	provider, err := NewBybit(server.URL, client)
	if err != nil {
		t.Fatal(err)
	}
	ticker, err := provider.SpotTicker(context.Background(), "btcusdt")
	if err != nil {
		t.Fatal(err)
	}
	if ticker.Bid != "100" || ticker.Ask != "101" {
		t.Fatalf("unexpected ticker: %#v", ticker)
	}
}
