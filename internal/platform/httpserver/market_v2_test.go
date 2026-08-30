package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/limiance/backend/internal/marketdata"
)

type marketReaderStub struct {
	exists  bool
	candles []marketdata.Candle
}

func (stub marketReaderStub) Markets(context.Context) ([]marketdata.Market, error) {
	return []marketdata.Market{{Pair: "BTCUSDT", Status: "active"}}, nil
}
func (stub marketReaderStub) PairExists(context.Context, string) (bool, error) {
	return stub.exists, nil
}
func (stub marketReaderStub) RecentTrades(context.Context, string, int) ([]marketdata.Trade, error) {
	return []marketdata.Trade{}, nil
}
func (stub marketReaderStub) Ticker(context.Context, string, time.Time) (marketdata.Ticker, error) {
	return marketdata.Ticker{Pair: "BTCUSDT", SequenceID: 1, LastPrice: "100", High24H: "110", Low24H: "90", Volume24H: "5", Change24H: "10", ChangeBPS24H: "1111"}, nil
}
func (stub marketReaderStub) Tickers(context.Context, time.Time) ([]marketdata.Ticker, error) {
	return []marketdata.Ticker{
		{Pair: "BTCUSDT", LastPrice: "100", Change24H: "10", ChangeBPS24H: "1111"},
		{Pair: "ETHUSDT", LastPrice: "50", Change24H: "-5", ChangeBPS24H: "-909"},
	}, nil
}
func (stub marketReaderStub) MinuteCandles(context.Context, string, time.Time, int) ([]marketdata.Candle, error) {
	return stub.candles, nil
}

func testMarketHandler() (*MarketV2Handler, *MarketHub) {
	cache := marketdata.NewMemoryStore()
	_ = cache.SaveOrderBook(context.Background(), marketdata.OrderBookSnapshot{SequenceID: 1, TimestampNS: 1, Pair: "BTCUSDT", Bids: []marketdata.OrderBookLevel{{Price: 99, Quantity: 2, OrderCount: 1}}, Asks: []marketdata.OrderBookLevel{{Price: 101, Quantity: 3, OrderCount: 1}}})
	hub := NewMarketHub()
	return NewMarketV2Handler(marketReaderStub{exists: true}, cache, hub, nil), hub
}

func TestMarketRESTIsPublicAndReturnsAtomicStrings(t *testing.T) {
	handler, _ := testMarketHandler()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v2/market/ticker/{pair}", handler.Ticker)
	mux.HandleFunc("GET /v2/market/orderbook/{pair}", handler.OrderBook)
	for _, path := range []string{"/v2/market/ticker/BTCUSDT", "/v2/market/orderbook/BTCUSDT?depth=100"} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s returned %d: %s", path, response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), ".0") {
			t.Fatalf("money value was encoded as floating point: %s", response.Body.String())
		}
		if strings.Contains(path, "orderbook") && !strings.Contains(response.Body.String(), `"99"`) {
			t.Fatalf("order book atomic values were not encoded as strings: %s", response.Body.String())
		}
	}
}

func TestAllTickersReturnsRankingReadyIntegerChanges(t *testing.T) {
	handler, _ := testMarketHandler()
	response := httptest.NewRecorder()
	handler.Tickers(response, httptest.NewRequest(http.MethodGet, "/v2/market/tickers", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("tickers returned %d: %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, expected := range []string{`"pair":"BTCUSDT"`, `"change_bps_24h":"1111"`, `"pair":"ETHUSDT"`, `"change_bps_24h":"-909"`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("missing %s in %s", expected, body)
		}
	}
}

func TestMarketWebSocketSnapshotUpdatesAndUnsubscribe(t *testing.T) {
	handler, hub := testMarketHandler()
	server := httptest.NewServer(http.HandlerFunc(handler.WebSocket))
	defer server.Close()
	connection, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err = connection.WriteJSON(map[string]any{"action": "subscribe", "channel": "orderbook.BTCUSDT", "depth": 20}); err != nil {
		t.Fatal(err)
	}
	var ack map[string]any
	if err = connection.ReadJSON(&ack); err != nil || ack["type"] != "subscribed" {
		t.Fatalf("subscription acknowledgement: %#v %v", ack, err)
	}
	var snapshot marketdata.RealtimeMessage
	if err = connection.ReadJSON(&snapshot); err != nil || snapshot.Type != "snapshot" || snapshot.SequenceID != 1 {
		t.Fatalf("initial snapshot: %#v %v", snapshot, err)
	}
	update, _ := marketdata.NewRealtimeMessage("l2update", "orderbook.BTCUSDT", "BTCUSDT", 2, map[string]any{"bids": [][]string{{"100", "1"}}})
	hub.Publish(update)
	var received marketdata.RealtimeMessage
	if err = connection.ReadJSON(&received); err != nil || received.SequenceID != 2 {
		t.Fatalf("incremental update: %#v %v", received, err)
	}
	if err = connection.WriteJSON(map[string]string{"action": "unsubscribe", "channel": "orderbook.BTCUSDT"}); err != nil {
		t.Fatal(err)
	}
	if err = connection.ReadJSON(&ack); err != nil || ack["type"] != "unsubscribed" {
		t.Fatalf("unsubscribe acknowledgement: %#v %v", ack, err)
	}
	hub.Publish(update)
	_ = connection.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, _, err = connection.ReadMessage(); err == nil {
		t.Fatal("received update after unsubscribe")
	}
}

func TestMarketHubFansOneUpdateToOneThousandClients(t *testing.T) {
	hub := NewMarketHub()
	clients := make([]*marketClient, 1000)
	for index := range clients {
		clients[index] = &marketClient{send: make(chan []byte, 1), done: make(chan struct{}), subscriptions: map[string]struct{}{"orderbook.BTCUSDT": {}}}
		hub.add(clients[index])
	}
	message, _ := marketdata.NewRealtimeMessage("l2update", "orderbook.BTCUSDT", "BTCUSDT", 2, map[string]string{"price": "100"})
	hub.Publish(message)
	for index, client := range clients {
		select {
		case payload := <-client.send:
			var got marketdata.RealtimeMessage
			if json.Unmarshal(payload, &got) != nil || got.SequenceID != 2 {
				t.Fatalf("client %d invalid payload", index)
			}
		default:
			t.Fatalf("client %d missed update", index)
		}
	}
	if hub.ClientCount() != 1000 {
		t.Fatalf("unexpected client count %d", hub.ClientCount())
	}
}

func TestMarketWebSocketOneThousandConnections(t *testing.T) {
	if os.Getenv("LIMIANCE_WS_LOAD_TEST") != "1" {
		t.Skip("set LIMIANCE_WS_LOAD_TEST=1 to run the network WebSocket load test")
	}
	handler, hub := testMarketHandler()
	server := httptest.NewServer(http.HandlerFunc(handler.WebSocket))
	defer server.Close()
	endpoint := "ws" + strings.TrimPrefix(server.URL, "http")
	connections := make([]*websocket.Conn, 1000)
	for index := range connections {
		connection, _, err := websocket.DefaultDialer.Dial(endpoint, nil)
		if err != nil {
			t.Fatalf("dial client %d: %v", index, err)
		}
		connections[index] = connection
		defer connection.Close()
		if err = connection.WriteJSON(map[string]any{"action": "subscribe", "channel": "orderbook.BTCUSDT", "depth": 20}); err != nil {
			t.Fatal(err)
		}
		for message := 0; message < 2; message++ {
			if _, _, err = connection.ReadMessage(); err != nil {
				t.Fatalf("initialize client %d: %v", index, err)
			}
		}
	}
	if hub.ClientCount() != len(connections) {
		t.Fatalf("connected clients=%d want=%d", hub.ClientCount(), len(connections))
	}
	start := time.Now()
	update, _ := marketdata.NewRealtimeMessage("l2update", "orderbook.BTCUSDT", "BTCUSDT", 2, map[string]any{"bids": [][]string{{"100", "1"}}})
	hub.Publish(update)
	latencies := make(chan time.Duration, len(connections))
	errorsFound := make(chan error, len(connections))
	var wait sync.WaitGroup
	for index, connection := range connections {
		wait.Add(1)
		go func(index int, connection *websocket.Conn) {
			defer wait.Done()
			_ = connection.SetReadDeadline(time.Now().Add(2 * time.Second))
			var event marketdata.RealtimeMessage
			if err := connection.ReadJSON(&event); err != nil || event.SequenceID != 2 {
				errorsFound <- fmt.Errorf("client %d: event=%+v err=%v", index, event, err)
				return
			}
			latencies <- time.Since(start)
		}(index, connection)
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Fatal(err)
	}
	close(latencies)
	for latency := range latencies {
		if latency >= 500*time.Millisecond {
			t.Fatalf("update latency %s exceeds 500ms", latency)
		}
	}
}
