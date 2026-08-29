package main

import (
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

func main() {
	address := flag.String("address", ":4010", "mock server listen address")
	flag.Parse()
	log.Printf("Limiance v2 mock listening on %s", *address)
	log.Fatal(http.ListenAndServe(*address, newMockHandler()))
}

func newMockHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v2/market/markets", func(w http.ResponseWriter, _ *http.Request) {
		jsonResponse(w, http.StatusOK, `{"markets":[{"pair":"BTCUSDT","base_asset":"BTC","quote_asset":"USDT","price_scale":8,"quantity_scale":8,"status":"active"}]}`)
	})
	mux.HandleFunc("GET /v2/market/orderbook/{pair}", func(w http.ResponseWriter, _ *http.Request) {
		jsonResponse(w, http.StatusOK, `{"sequence_id":1,"timestamp_ns":1788000000000000000,"pair":"BTCUSDT","bids":[["5000000000000","1000000","1"]],"asks":[["5000100000000","1000000","1"]]}`)
	})
	mux.HandleFunc("GET /v2/market/ticker/{pair}", func(w http.ResponseWriter, _ *http.Request) {
		jsonResponse(w, http.StatusOK, `{"pair":"BTCUSDT","sequence_id":1,"last_price":"5000000000000","high_24h":"5100000000000","low_24h":"4900000000000","volume_24h":"100000000","quote_volume_24h":"5000000000000","change_24h":"100000000000"}`)
	})
	mux.HandleFunc("GET /v2/market/trades/{pair}", func(w http.ResponseWriter, _ *http.Request) { jsonResponse(w, http.StatusOK, `{"trades":[]}`) })
	mux.HandleFunc("GET /v2/market/klines/{pair}", func(w http.ResponseWriter, _ *http.Request) { jsonResponse(w, http.StatusOK, `{"candles":[]}`) })
	mux.HandleFunc("GET /v2/orders", func(w http.ResponseWriter, _ *http.Request) {
		jsonResponse(w, http.StatusOK, `{"orders":[],"limit":50,"offset":0,"has_more":false}`)
	})
	mux.HandleFunc("POST /v2/orders", func(w http.ResponseWriter, _ *http.Request) { jsonResponse(w, http.StatusCreated, mockOrder()) })
	mux.HandleFunc("GET /v2/orders/{order_id}", func(w http.ResponseWriter, _ *http.Request) { jsonResponse(w, http.StatusOK, mockOrder()) })
	mux.HandleFunc("DELETE /v2/orders/{order_id}", func(w http.ResponseWriter, _ *http.Request) { jsonResponse(w, http.StatusOK, mockOrder()) })
	mux.HandleFunc("GET /v2/ws", mockWebSocket)
	return mux
}

func jsonResponse(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}
func mockOrder() string {
	return `{"id":"00000000-0000-4000-8000-000000000001","pair":"BTCUSDT","side":"BUY","type":"LIMIT","price":"5000000000000","quantity":"1000000","filled_quantity":"0","remaining_quantity":"1000000","avg_price":"0","time_in_force":"GTC","status":"OPEN","post_only":false,"reduce_only":false,"created_at":"2026-08-29T12:00:00Z","updated_at":"2026-08-29T12:00:00Z"}`
}

func mockWebSocket(w http.ResponseWriter, r *http.Request) {
	connection, err := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer connection.Close()
	for {
		var request map[string]any
		if connection.ReadJSON(&request) != nil {
			return
		}
		channel, _ := request["channel"].(string)
		_ = connection.WriteJSON(map[string]any{"type": "subscribed", "channel": channel})
		_ = connection.WriteJSON(map[string]any{"type": "snapshot", "channel": channel, "pair": "BTCUSDT", "sequence_id": 1, "timestamp": time.Now().UTC()})
	}
}
