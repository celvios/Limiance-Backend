package marketdata

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/limiance/backend/internal/trading/protocol"
	"github.com/limiance/backend/internal/trading/transport"
)

type consumerStoreStub struct {
	candle         Candle
	ticker         Ticker
	duplicate      bool
	claimAccepted  bool
	claimDuplicate bool
	err            error
}

func (store *consumerStoreStub) ApplyTrade(_ context.Context, trade Trade) (Candle, bool, error) {
	store.candle, _ = NewMinuteCandle(trade)
	return store.candle, store.duplicate, store.err
}
func (store *consumerStoreStub) ClaimOrderBookSequence(context.Context, string, uint64) (bool, bool, error) {
	return store.claimAccepted, store.claimDuplicate, store.err
}
func (store *consumerStoreStub) TakerSide(context.Context, string) (string, error) { return "BUY", nil }
func (store *consumerStoreStub) Ticker(_ context.Context, pair string, _ time.Time) (Ticker, error) {
	store.ticker = Ticker{Pair: pair, SequenceID: 7, LastPrice: "50000", High24H: "51000", Low24H: "49000", Volume24H: "2", QuoteVolume24H: "100000", Change24H: "1000"}
	return store.ticker, nil
}

type replayStub struct {
	pair string
	from uint64
}

func (replay *replayStub) RequestReplay(_ context.Context, pair string, from uint64) error {
	replay.pair, replay.from = pair, from
	return nil
}

func TestConsumerPublishesTradeTickerAndCandle(t *testing.T) {
	store := &consumerStoreStub{}
	cache := NewMemoryStore()
	consumer := NewConsumer(store, cache, nil)
	payload, err := protocol.EncodeTradeEvent(protocol.TradeEvent{SequenceID: 7, TimestampNS: uint64(time.Date(2026, 8, 29, 10, 0, 5, 0, time.UTC).UnixNano()), Pair: "BTCUSDT", MakerOrderID: "maker", TakerOrderID: "taker", MakerUserID: "maker-user", TakerUserID: "taker-user", Price: 50000, Quantity: 200000000, MakerFeeBPS: 1, TakerFeeBPS: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err = consumer.Process(context.Background(), transport.Event{Topic: transport.TopicTrade, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if len(cache.messages) != 3 {
		t.Fatalf("expected trade, ticker, and candle messages, got %d", len(cache.messages))
	}
	for index, want := range []string{"trade", "ticker", "kline"} {
		if cache.messages[index].Type != want {
			t.Fatalf("message %d type=%s", index, cache.messages[index].Type)
		}
	}
	if store.candle.OpenTime.Second() != 0 || store.candle.Volume != "200000000" {
		t.Fatalf("unexpected candle: %#v", store.candle)
	}
}

func TestConsumerStoresOrderBookBeforePublishing(t *testing.T) {
	store := &consumerStoreStub{claimAccepted: true}
	cache := NewMemoryStore()
	snapshot := OrderBookSnapshot{SequenceID: 10, TimestampNS: 1, Pair: "BTCUSDT", Bids: []OrderBookLevel{{Price: 100, Quantity: 2, OrderCount: 1}}, Asks: []OrderBookLevel{{Price: 101, Quantity: 3, OrderCount: 1}}}
	payload, err := EncodeOrderBookSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err = NewConsumer(store, cache, nil).Process(context.Background(), transport.Event{Topic: transport.TopicOrderBook, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	stored, ok, _ := cache.OrderBook(context.Background(), "BTCUSDT")
	if !ok || stored.SequenceID != 10 || len(cache.messages) != 1 || cache.messages[0].Type != "l2update" {
		t.Fatalf("snapshot was not stored and published: %#v %#v", stored, cache.messages)
	}
}

func TestConsumerRequestsReplayOnSequenceGap(t *testing.T) {
	gap := &SequenceGapError{Pair: "BTCUSDT", Stream: "trades", Expected: 8, Received: 10}
	store := &consumerStoreStub{err: gap}
	replay := &replayStub{}
	payload, _ := protocol.EncodeTradeEvent(protocol.TradeEvent{SequenceID: 10, TimestampNS: 1, Pair: "BTCUSDT", MakerOrderID: "maker", TakerOrderID: "taker", MakerUserID: "maker-user", TakerUserID: "taker-user", Price: 1, Quantity: 100000000})
	err := NewConsumer(store, NewMemoryStore(), replay).Process(context.Background(), transport.Event{Topic: transport.TopicTrade, Payload: payload})
	if !errors.Is(err, ErrMarketSequenceGap) || replay.pair != "BTCUSDT" || replay.from != 8 {
		t.Fatalf("gap handling failed: %v %#v", err, replay)
	}
}
