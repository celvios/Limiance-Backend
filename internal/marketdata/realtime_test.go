package marketdata

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestRedisMarketSnapshotAndRealtimeDelivery(t *testing.T) {
	redisURL := os.Getenv("LIMIANCE_TEST_REDIS_URL")
	if redisURL == "" {
		t.Skip("set LIMIANCE_TEST_REDIS_URL to run Redis market tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, err := NewRedisStore(redisURL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	pair := "REDISTEST"
	defer store.client.Del(context.Background(), marketRedisPrefix+"orderbook:"+pair)
	snapshot := OrderBookSnapshot{SequenceID: 77, TimestampNS: 1, Pair: pair, Bids: []OrderBookLevel{{Price: 100, Quantity: 2, OrderCount: 1}}, Asks: []OrderBookLevel{{Price: 101, Quantity: 3, OrderCount: 1}}}
	if err = store.SaveOrderBook(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	got, found, err := store.OrderBook(ctx, pair)
	if err != nil || !found || got.SequenceID != 77 {
		t.Fatalf("Redis snapshot: found=%v got=%+v err=%v", found, got, err)
	}

	received := make(chan RealtimeMessage, 1)
	subscribeContext, stopSubscribe := context.WithCancel(ctx)
	defer stopSubscribe()
	go func() {
		_ = store.Subscribe(subscribeContext, func(message RealtimeMessage) {
			select {
			case received <- message:
			default:
			}
		})
	}()
	time.Sleep(25 * time.Millisecond)
	message, _ := NewRealtimeMessage("ticker", "ticker."+pair, pair, 78, map[string]string{"last_price": "100"})
	deadline := time.Now().Add(2 * time.Second)
	for {
		if err = store.Publish(ctx, message); err != nil {
			t.Fatal(err)
		}
		select {
		case event := <-received:
			if event.SequenceID != 78 || event.Channel != "ticker."+pair {
				t.Fatalf("unexpected Redis event: %+v", event)
			}
			return
		case <-time.After(20 * time.Millisecond):
			if time.Now().After(deadline) {
				t.Fatal("Redis pub/sub event was not delivered")
			}
		}
	}
}
