package marketdata

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/limiance/backend/internal/trading"
	"github.com/limiance/backend/internal/trading/protocol"
	"github.com/limiance/backend/internal/trading/transport"
)

type ConsumerStore interface {
	ApplyTrade(context.Context, Trade) (Candle, bool, error)
	ClaimOrderBookSequence(context.Context, string, uint64) (bool, bool, error)
	TakerSide(context.Context, string) (string, error)
	Ticker(context.Context, string, time.Time) (Ticker, error)
}

type MarketEventReceiver interface {
	Receive() (transport.Event, error)
}

type Consumer struct {
	store  ConsumerStore
	cache  SnapshotCache
	replay trading.ReplayRequester
	now    func() time.Time
}

func NewConsumer(store ConsumerStore, cache SnapshotCache, replay trading.ReplayRequester) *Consumer {
	return &Consumer{store: store, cache: cache, replay: replay, now: time.Now}
}

func (consumer *Consumer) Process(ctx context.Context, event transport.Event) error {
	if consumer == nil || consumer.store == nil || consumer.cache == nil {
		return fmt.Errorf("%w: consumer dependencies", ErrInvalidMarketEvent)
	}
	switch event.Topic {
	case transport.TopicOrderBook:
		return consumer.processOrderBook(ctx, event.Payload)
	case transport.TopicTrade:
		return consumer.processTrade(ctx, event.Payload)
	default:
		return nil
	}
}

func (consumer *Consumer) processOrderBook(ctx context.Context, payload []byte) error {
	snapshot, err := DecodeOrderBookSnapshot(payload)
	if err != nil {
		return err
	}
	accepted, duplicate, err := consumer.store.ClaimOrderBookSequence(ctx, snapshot.Pair, snapshot.SequenceID)
	if err != nil {
		return consumer.handleGap(ctx, err)
	}
	if !accepted || duplicate {
		return nil
	}
	if err = consumer.cache.SaveOrderBook(ctx, snapshot); err != nil {
		return err
	}
	message, _ := NewRealtimeMessage("l2update", "orderbook."+snapshot.Pair, snapshot.Pair, snapshot.SequenceID, snapshot.Public())
	return consumer.cache.Publish(ctx, message)
}

func (consumer *Consumer) processTrade(ctx context.Context, payload []byte) error {
	event, err := protocol.DecodeTradeEvent(payload)
	if err != nil {
		return err
	}
	if event.TimestampNS > math.MaxInt64 {
		return fmt.Errorf("%w: trade timestamp exceeds Unix nanosecond range", ErrInvalidMarketEvent)
	}
	side, err := consumer.store.TakerSide(ctx, event.TakerOrderID)
	if err != nil {
		return err
	}
	quote, err := trading.QuoteAmount(event.Price, event.Quantity)
	if err != nil {
		return err
	}
	trade := Trade{Pair: event.Pair, SequenceID: event.SequenceID, Timestamp: time.Unix(0, int64(event.TimestampNS)).UTC(), PriceAtomic: fmt.Sprint(event.Price), QuantityAtomic: fmt.Sprint(event.Quantity), QuoteAtomic: quote.String(), Side: strings.ToUpper(side), PayloadHash: sha256.Sum256(payload)}
	candle, duplicate, err := consumer.store.ApplyTrade(ctx, trade)
	if err != nil {
		return consumer.handleGap(ctx, err)
	}
	if duplicate {
		return nil
	}
	tradeMessage, _ := NewRealtimeMessage("trade", "trades."+event.Pair, event.Pair, event.SequenceID, trade)
	if err = consumer.cache.Publish(ctx, tradeMessage); err != nil {
		return err
	}
	ticker, err := consumer.store.Ticker(ctx, event.Pair, consumer.now())
	if err != nil {
		return err
	}
	tickerMessage, _ := NewRealtimeMessage("ticker", "ticker."+event.Pair, event.Pair, event.SequenceID, ticker)
	if err = consumer.cache.Publish(ctx, tickerMessage); err != nil {
		return err
	}
	candleMessage, _ := NewRealtimeMessage("kline", "klines."+event.Pair+".1m", event.Pair, event.SequenceID, candle)
	return consumer.cache.Publish(ctx, candleMessage)
}

func (consumer *Consumer) handleGap(ctx context.Context, err error) error {
	var gap *SequenceGapError
	if !errors.As(err, &gap) || consumer.replay == nil {
		return err
	}
	if replayErr := consumer.replay.RequestReplay(ctx, gap.Pair, gap.Expected); replayErr != nil {
		return fmt.Errorf("market replay request: %w", replayErr)
	}
	return err
}

func (consumer *Consumer) Run(ctx context.Context, receiver MarketEventReceiver) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		event, err := receiver.Receive()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if err = consumer.Process(ctx, event); err != nil && !errors.Is(err, ErrMarketSequenceGap) {
			return err
		}
	}
}
