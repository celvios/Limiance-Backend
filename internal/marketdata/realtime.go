package marketdata

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	redis "github.com/redis/go-redis/v9"
)

const (
	marketRedisPrefix = "limiance:market:v1:"
	marketRedisEvents = marketRedisPrefix + "events"
)

type RealtimeMessage struct {
	Type       string          `json:"type"`
	Channel    string          `json:"channel"`
	Pair       string          `json:"pair"`
	SequenceID uint64          `json:"sequence_id"`
	Data       json.RawMessage `json:"data"`
}

func NewRealtimeMessage(messageType, channel, pair string, sequence uint64, value any) (RealtimeMessage, error) {
	if messageType == "" || channel == "" || ValidatePair(pair) != nil || sequence == 0 {
		return RealtimeMessage{}, fmt.Errorf("%w: realtime envelope", ErrInvalidMarketEvent)
	}
	data, err := json.Marshal(value)
	if err != nil {
		return RealtimeMessage{}, err
	}
	return RealtimeMessage{Type: messageType, Channel: channel, Pair: pair, SequenceID: sequence, Data: data}, nil
}

type SnapshotCache interface {
	SaveOrderBook(context.Context, OrderBookSnapshot) error
	OrderBook(context.Context, string) (OrderBookSnapshot, bool, error)
	Publish(context.Context, RealtimeMessage) error
}

type RedisStore struct{ client *redis.Client }

func NewRedisStore(rawURL string) (*RedisStore, error) {
	options, err := redis.ParseURL(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse Redis market URL: %w", err)
	}
	return &RedisStore{client: redis.NewClient(options)}, nil
}

func (store *RedisStore) SaveOrderBook(ctx context.Context, snapshot OrderBookSnapshot) error {
	if err := snapshot.Validate(); err != nil {
		return err
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	return store.client.Set(ctx, marketRedisPrefix+"orderbook:"+snapshot.Pair, payload, 0).Err()
}

func (store *RedisStore) OrderBook(ctx context.Context, pair string) (OrderBookSnapshot, bool, error) {
	payload, err := store.client.Get(ctx, marketRedisPrefix+"orderbook:"+pair).Bytes()
	if err == redis.Nil {
		return OrderBookSnapshot{}, false, nil
	}
	if err != nil {
		return OrderBookSnapshot{}, false, err
	}
	var snapshot OrderBookSnapshot
	if err = json.Unmarshal(payload, &snapshot); err != nil {
		return OrderBookSnapshot{}, false, err
	}
	if err = snapshot.Validate(); err != nil {
		return OrderBookSnapshot{}, false, err
	}
	return snapshot, true, nil
}

func (store *RedisStore) Publish(ctx context.Context, message RealtimeMessage) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	return store.client.Publish(ctx, marketRedisEvents, payload).Err()
}

func (store *RedisStore) Subscribe(ctx context.Context, handle func(RealtimeMessage)) error {
	subscription := store.client.Subscribe(ctx, marketRedisEvents)
	defer subscription.Close()
	if _, err := subscription.Receive(ctx); err != nil {
		return err
	}
	channel := subscription.Channel(redis.WithChannelSize(1024))
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-channel:
			if !ok {
				return nil
			}
			var message RealtimeMessage
			if json.Unmarshal([]byte(event.Payload), &message) == nil && message.Type != "" && strings.Contains(message.Channel, ".") {
				handle(message)
			}
		}
	}
}

func (store *RedisStore) Close() error { return store.client.Close() }

type MemoryStore struct {
	mu        sync.RWMutex
	snapshots map[string]OrderBookSnapshot
	messages  []RealtimeMessage
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{snapshots: make(map[string]OrderBookSnapshot)}
}
func (store *MemoryStore) SaveOrderBook(_ context.Context, snapshot OrderBookSnapshot) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.snapshots[snapshot.Pair] = snapshot
	return nil
}
func (store *MemoryStore) OrderBook(_ context.Context, pair string) (OrderBookSnapshot, bool, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	snapshot, ok := store.snapshots[pair]
	return snapshot, ok, nil
}
func (store *MemoryStore) Publish(_ context.Context, message RealtimeMessage) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.messages = append(store.messages, message)
	return nil
}
