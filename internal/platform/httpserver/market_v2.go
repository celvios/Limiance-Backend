package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/limiance/backend/internal/marketdata"
)

const (
	websocketQueueSize = 1024
	websocketWriteWait = 5 * time.Second
	websocketPongWait  = 60 * time.Second
)

type MarketReader interface {
	Markets(context.Context) ([]marketdata.Market, error)
	PairExists(context.Context, string) (bool, error)
	RecentTrades(context.Context, string, int) ([]marketdata.Trade, error)
	Ticker(context.Context, string, time.Time) (marketdata.Ticker, error)
	MinuteCandles(context.Context, string, time.Time, int) ([]marketdata.Candle, error)
}

type MarketV2Handler struct {
	repository MarketReader
	books      marketdata.SnapshotCache
	hub        *MarketHub
	upgrader   websocket.Upgrader
	now        func() time.Time
}

func NewMarketV2Handler(repository MarketReader, books marketdata.SnapshotCache, hub *MarketHub, allowedOrigins []string) *MarketV2Handler {
	allowed := make(map[string]struct{}, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		allowed[origin] = struct{}{}
	}
	return &MarketV2Handler{repository: repository, books: books, hub: hub, now: time.Now, upgrader: websocket.Upgrader{
		ReadBufferSize: 1024, WriteBufferSize: 4096,
		CheckOrigin: func(request *http.Request) bool {
			origin := request.Header.Get("Origin")
			if origin == "" {
				return true
			}
			_, ok := allowed[origin]
			return ok
		},
	}}
}

func (handler *MarketV2Handler) Markets(w http.ResponseWriter, r *http.Request) {
	markets, err := handler.repository.Markets(r.Context())
	if err != nil {
		marketError(w, http.StatusInternalServerError, "MARKET_DATA_UNAVAILABLE", "market catalog unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"markets": markets})
}

func (handler *MarketV2Handler) OrderBook(w http.ResponseWriter, r *http.Request) {
	pair, ok := handler.validPair(w, r)
	if !ok {
		return
	}
	depth, valid := boundedInt(r.URL.Query().Get("depth"), 20, 1, marketdata.MaxSnapshotDepth)
	if !valid {
		marketError(w, http.StatusBadRequest, "INVALID_DEPTH", "depth must be between 1 and 1000")
		return
	}
	snapshot, found, err := handler.books.OrderBook(r.Context(), pair)
	if err != nil {
		marketError(w, http.StatusServiceUnavailable, "MARKET_DATA_UNAVAILABLE", "order book unavailable")
		return
	}
	if !found {
		marketError(w, http.StatusServiceUnavailable, "SNAPSHOT_UNAVAILABLE", "order book snapshot unavailable")
		return
	}
	if len(snapshot.Bids) > depth {
		snapshot.Bids = snapshot.Bids[:depth]
	}
	if len(snapshot.Asks) > depth {
		snapshot.Asks = snapshot.Asks[:depth]
	}
	writeJSON(w, http.StatusOK, snapshot.Public())
}

func (handler *MarketV2Handler) Trades(w http.ResponseWriter, r *http.Request) {
	pair, ok := handler.validPair(w, r)
	if !ok {
		return
	}
	limit, valid := boundedInt(r.URL.Query().Get("limit"), 100, 1, 1000)
	if !valid {
		marketError(w, http.StatusBadRequest, "INVALID_LIMIT", "limit must be between 1 and 1000")
		return
	}
	trades, err := handler.repository.RecentTrades(r.Context(), pair, limit)
	if err != nil {
		marketError(w, http.StatusServiceUnavailable, "MARKET_DATA_UNAVAILABLE", "recent trades unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"trades": trades})
}

func (handler *MarketV2Handler) Ticker(w http.ResponseWriter, r *http.Request) {
	pair, ok := handler.validPair(w, r)
	if !ok {
		return
	}
	ticker, err := handler.repository.Ticker(r.Context(), pair, handler.now())
	if err != nil {
		marketError(w, http.StatusServiceUnavailable, "MARKET_DATA_UNAVAILABLE", "ticker unavailable")
		return
	}
	writeJSON(w, http.StatusOK, ticker)
}

func (handler *MarketV2Handler) Klines(w http.ResponseWriter, r *http.Request) {
	pair, ok := handler.validPair(w, r)
	if !ok {
		return
	}
	interval := r.URL.Query().Get("interval")
	duration, validInterval := marketdata.IntervalDuration(interval)
	if !validInterval {
		marketError(w, http.StatusBadRequest, "INVALID_INTERVAL", "supported intervals: 1m, 5m, 15m, 1h, 4h, 1d, 1w, 1M")
		return
	}
	limit, valid := boundedInt(r.URL.Query().Get("limit"), 100, 1, 1000)
	if !valid {
		marketError(w, http.StatusBadRequest, "INVALID_LIMIT", "limit must be between 1 and 1000")
		return
	}
	minutesPerBucket := int(duration / time.Minute)
	if interval == "1M" {
		minutesPerBucket = 31 * 24 * 60
	}
	fetch := limit * minutesPerBucket
	if fetch < limit {
		fetch = limit
	}
	if fetch > 100000 {
		fetch = 100000
	}
	minutes, err := handler.repository.MinuteCandles(r.Context(), pair, handler.now().UTC().Add(time.Minute), fetch)
	if err != nil {
		marketError(w, http.StatusServiceUnavailable, "MARKET_DATA_UNAVAILABLE", "candles unavailable")
		return
	}
	candles, err := marketdata.AggregateCandles(marketdata.FillMinuteGaps(minutes), interval)
	if err != nil {
		marketError(w, http.StatusBadRequest, "INVALID_INTERVAL", err.Error())
		return
	}
	if len(candles) > limit {
		candles = candles[len(candles)-limit:]
	}
	writeJSON(w, http.StatusOK, map[string]any{"candles": candles})
}

func (handler *MarketV2Handler) validPair(w http.ResponseWriter, r *http.Request) (string, bool) {
	pair := strings.ToUpper(strings.TrimSpace(r.PathValue("pair")))
	if marketdata.ValidatePair(pair) != nil {
		marketError(w, http.StatusBadRequest, "INVALID_PAIR", "pair is invalid")
		return "", false
	}
	exists, err := handler.repository.PairExists(r.Context(), pair)
	if err != nil {
		marketError(w, http.StatusServiceUnavailable, "MARKET_DATA_UNAVAILABLE", "pair catalog unavailable")
		return "", false
	}
	if !exists {
		marketError(w, http.StatusNotFound, "INVALID_PAIR", "pair is not enabled")
		return "", false
	}
	return pair, true
}

func boundedInt(raw string, fallback, minimum, maximum int) (int, bool) {
	if raw == "" {
		return fallback, true
	}
	value, err := strconv.Atoi(raw)
	return value, err == nil && value >= minimum && value <= maximum
}

func marketError(w http.ResponseWriter, status int, code, message string) {
	writeVersionedError(w, status, code, message)
}

type MarketHub struct {
	mu      sync.RWMutex
	clients map[*marketClient]struct{}
}

type marketClient struct {
	connection    *websocket.Conn
	send          chan []byte
	done          chan struct{}
	closeOnce     sync.Once
	mu            sync.RWMutex
	subscriptions map[string]struct{}
}

func NewMarketHub() *MarketHub { return &MarketHub{clients: make(map[*marketClient]struct{})} }

func (hub *MarketHub) Publish(message marketdata.RealtimeMessage) {
	payload, err := json.Marshal(message)
	if err != nil {
		return
	}
	hub.mu.RLock()
	clients := make([]*marketClient, 0, len(hub.clients))
	for client := range hub.clients {
		clients = append(clients, client)
	}
	hub.mu.RUnlock()
	for _, client := range clients {
		client.mu.RLock()
		_, subscribed := client.subscriptions[message.Channel]
		client.mu.RUnlock()
		if !subscribed {
			continue
		}
		select {
		case client.send <- payload:
		default:
			hub.remove(client)
		}
	}
}

func (hub *MarketHub) add(client *marketClient) {
	hub.mu.Lock()
	hub.clients[client] = struct{}{}
	hub.mu.Unlock()
}
func (hub *MarketHub) remove(client *marketClient) {
	hub.mu.Lock()
	if _, exists := hub.clients[client]; exists {
		delete(hub.clients, client)
	}
	hub.mu.Unlock()
	client.closeOnce.Do(func() {
		close(client.done)
		if client.connection != nil {
			_ = client.connection.Close()
		}
	})
}
func (hub *MarketHub) ClientCount() int {
	hub.mu.RLock()
	defer hub.mu.RUnlock()
	return len(hub.clients)
}

func (handler *MarketV2Handler) WebSocket(w http.ResponseWriter, r *http.Request) {
	connection, err := handler.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	client := &marketClient{connection: connection, send: make(chan []byte, websocketQueueSize), done: make(chan struct{}), subscriptions: make(map[string]struct{})}
	handler.hub.add(client)
	go handler.writePump(client)
	handler.readPump(r.Context(), client)
}

func (handler *MarketV2Handler) readPump(ctx context.Context, client *marketClient) {
	defer handler.hub.remove(client)
	client.connection.SetReadLimit(4096)
	_ = client.connection.SetReadDeadline(time.Now().Add(websocketPongWait))
	client.connection.SetPongHandler(func(string) error { return client.connection.SetReadDeadline(time.Now().Add(websocketPongWait)) })
	for {
		var request struct {
			Action  string `json:"action"`
			Channel string `json:"channel"`
			Depth   int    `json:"depth"`
		}
		if err := client.connection.ReadJSON(&request); err != nil {
			return
		}
		if request.Action != "subscribe" && request.Action != "unsubscribe" {
			handler.queueJSON(client, map[string]any{"type": "error", "error": map[string]string{"code": "INVALID_ACTION", "message": "action must be subscribe or unsubscribe"}})
			continue
		}
		pair, interval, err := parseChannel(request.Channel)
		if err != nil {
			handler.queueJSON(client, map[string]any{"type": "error", "error": map[string]string{"code": "INVALID_CHANNEL", "message": err.Error()}})
			continue
		}
		exists, lookupErr := handler.repository.PairExists(ctx, pair)
		if lookupErr != nil || !exists {
			handler.queueJSON(client, map[string]any{"type": "error", "error": map[string]string{"code": "INVALID_PAIR", "message": "pair is not enabled"}})
			continue
		}
		if request.Action == "unsubscribe" {
			client.mu.Lock()
			delete(client.subscriptions, request.Channel)
			client.mu.Unlock()
			handler.queueJSON(client, map[string]any{"type": "unsubscribed", "channel": request.Channel})
			continue
		}
		client.mu.Lock()
		client.subscriptions[request.Channel] = struct{}{}
		client.mu.Unlock()
		handler.queueJSON(client, map[string]any{"type": "subscribed", "channel": request.Channel})
		handler.sendInitial(ctx, client, request.Channel, pair, interval, request.Depth)
	}
}

func (handler *MarketV2Handler) writePump(client *marketClient) {
	defer handler.hub.remove(client)
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-client.done:
			return
		case payload := <-client.send:
			_ = client.connection.SetWriteDeadline(time.Now().Add(websocketWriteWait))
			if err := client.connection.WriteMessage(websocket.TextMessage, payload); err != nil {
				return
			}
		case <-ticker.C:
			_ = client.connection.SetWriteDeadline(time.Now().Add(websocketWriteWait))
			if err := client.connection.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (handler *MarketV2Handler) queueJSON(client *marketClient, value any) {
	payload, _ := json.Marshal(value)
	select {
	case client.send <- payload:
	default:
		handler.hub.remove(client)
	}
}

func (handler *MarketV2Handler) sendInitial(ctx context.Context, client *marketClient, channel, pair, interval string, depth int) {
	switch {
	case strings.HasPrefix(channel, "orderbook."):
		snapshot, found, _ := handler.books.OrderBook(ctx, pair)
		if !found {
			return
		}
		if depth <= 0 {
			depth = 20
		}
		if depth > marketdata.MaxSnapshotDepth {
			depth = marketdata.MaxSnapshotDepth
		}
		if len(snapshot.Bids) > depth {
			snapshot.Bids = snapshot.Bids[:depth]
		}
		if len(snapshot.Asks) > depth {
			snapshot.Asks = snapshot.Asks[:depth]
		}
		message, _ := marketdata.NewRealtimeMessage("snapshot", channel, pair, snapshot.SequenceID, snapshot.Public())
		handler.queueJSON(client, message)
	case strings.HasPrefix(channel, "trades."):
		trades, _ := handler.repository.RecentTrades(ctx, pair, 100)
		handler.queueJSON(client, map[string]any{"type": "snapshot", "channel": channel, "pair": pair, "data": trades})
	case strings.HasPrefix(channel, "ticker."):
		ticker, _ := handler.repository.Ticker(ctx, pair, handler.now())
		handler.queueJSON(client, map[string]any{"type": "snapshot", "channel": channel, "pair": pair, "data": ticker})
	case strings.HasPrefix(channel, "klines."):
		minutes, _ := handler.repository.MinuteCandles(ctx, pair, handler.now().Add(time.Minute), 1000)
		candles, _ := marketdata.AggregateCandles(marketdata.FillMinuteGaps(minutes), interval)
		handler.queueJSON(client, map[string]any{"type": "snapshot", "channel": channel, "pair": pair, "data": candles})
	}
}

func parseChannel(channel string) (string, string, error) {
	parts := strings.Split(channel, ".")
	if len(parts) < 2 {
		return "", "", errors.New("channel is invalid")
	}
	if parts[0] != "orderbook" && parts[0] != "trades" && parts[0] != "ticker" && parts[0] != "klines" {
		return "", "", errors.New("channel is invalid")
	}
	if marketdata.ValidatePair(parts[1]) != nil {
		return "", "", errors.New("pair is invalid")
	}
	if parts[0] == "klines" {
		if len(parts) != 3 {
			return "", "", errors.New("kline interval is required")
		}
		if _, ok := marketdata.IntervalDuration(parts[2]); !ok {
			return "", "", errors.New("kline interval is invalid")
		}
		return parts[1], parts[2], nil
	}
	if len(parts) != 2 {
		return "", "", errors.New("channel is invalid")
	}
	return parts[1], "", nil
}
