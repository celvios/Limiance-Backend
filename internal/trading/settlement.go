package trading

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/limiance/backend/internal/trading/protocol"
	"github.com/limiance/backend/internal/trading/transport"
)

var (
	ErrSequenceGap       = errors.New("engine trade sequence gap")
	ErrSequenceConflict  = errors.New("engine trade sequence conflict")
	ErrSettlementInvalid = errors.New("trade settlement is invalid")
)

type SequenceGapError struct {
	Pair     string
	Expected uint64
	Received uint64
}

func (err *SequenceGapError) Error() string {
	return fmt.Sprintf("%v for %s: expected %d, received %d", ErrSequenceGap, err.Pair, err.Expected, err.Received)
}

func (err *SequenceGapError) Unwrap() error { return ErrSequenceGap }

type Trade struct {
	ID              string
	Pair            string
	SequenceID      uint64
	MakerOrderID    string
	TakerOrderID    string
	PriceAtomic     string
	QuantityAtomic  string
	QuoteAtomic     string
	MakerFeeAtomic  string
	TakerFeeAtomic  string
	Duplicate       bool
	SettlementState string
}

type SettlementStore interface {
	SettleTrade(context.Context, protocol.TradeEvent, [32]byte) (Trade, error)
}

type ReplayRequester interface {
	RequestReplay(context.Context, string, uint64) error
}

type TradeEventReceiver interface {
	Receive() (transport.Event, error)
}

type SettlementConsumer struct {
	store  SettlementStore
	replay ReplayRequester
}

func NewSettlementConsumer(store SettlementStore, replay ReplayRequester) *SettlementConsumer {
	return &SettlementConsumer{store: store, replay: replay}
}

func (consumer *SettlementConsumer) ProcessPayload(ctx context.Context, payload []byte) (Trade, error) {
	event, err := protocol.DecodeTradeEvent(payload)
	if err != nil {
		return Trade{}, fmt.Errorf("%w: decode trade event: %v", ErrSettlementInvalid, err)
	}
	trade, err := consumer.store.SettleTrade(ctx, event, sha256.Sum256(payload))
	if err != nil {
		var gap *SequenceGapError
		if errors.As(err, &gap) && consumer.replay != nil {
			if replayErr := consumer.replay.RequestReplay(ctx, gap.Pair, gap.Expected); replayErr != nil {
				return Trade{}, fmt.Errorf("request replay for %s from %d: %w", gap.Pair, gap.Expected, replayErr)
			}
		}
		return Trade{}, err
	}
	return trade, nil
}

func (consumer *SettlementConsumer) Run(ctx context.Context, receiver TradeEventReceiver) error {
	if consumer == nil || consumer.store == nil || receiver == nil {
		return fmt.Errorf("%w: settlement consumer dependencies are required", ErrSettlementInvalid)
	}
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
		if event.Topic != transport.TopicTrade {
			continue
		}
		if _, err = consumer.ProcessPayload(ctx, event.Payload); err != nil && !errors.Is(err, ErrSequenceGap) {
			return err
		}
	}
}

type ControlReplayRequester struct {
	engine Engine
	now    func() time.Time
	newID  func() (string, error)
}

func NewControlReplayRequester(engine Engine) *ControlReplayRequester {
	return &ControlReplayRequester{engine: engine, now: time.Now, newID: randomUUID}
}

func (requester *ControlReplayRequester) RequestReplay(_ context.Context, pair string, fromSequence uint64) error {
	if requester == nil || requester.engine == nil {
		return ErrEngineUnavailable
	}
	commandID, err := requester.newID()
	if err != nil {
		return err
	}
	payload, err := protocol.EncodeReplayRequest(protocol.ReplayRequest{
		CommandID: commandID, Pair: pair, FromSequence: fromSequence, TimestampNS: uint64(requester.now().UTC().UnixNano()),
	})
	if err != nil {
		return err
	}
	reply, err := requester.engine.Request(payload)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrEngineUnavailable, err)
	}
	ack, err := protocol.DecodeControlAck(reply)
	if err != nil || ack.CommandID != commandID || !ack.Accepted {
		return fmt.Errorf("%w: replay acknowledgement is invalid", ErrEngineResponse)
	}
	return nil
}

func QuoteAmount(price, quantity uint64) (*big.Int, error) {
	if price == 0 || quantity == 0 {
		return nil, ErrSettlementInvalid
	}
	product := new(big.Int).Mul(new(big.Int).SetUint64(price), new(big.Int).SetUint64(quantity))
	quote := new(big.Int).Quo(product, new(big.Int).SetUint64(atomicScale))
	if quote.Sign() <= 0 {
		return nil, fmt.Errorf("%w: trade notional rounds to zero", ErrSettlementInvalid)
	}
	return quote, nil
}

func FeeAmount(notional *big.Int, basisPoints int16) (*big.Int, error) {
	if notional == nil || notional.Sign() <= 0 || basisPoints < -10000 || basisPoints > 10000 {
		return nil, ErrSettlementInvalid
	}
	magnitude := new(big.Int).Quo(
		new(big.Int).Mul(new(big.Int).Set(notional), big.NewInt(absInt64(int64(basisPoints)))),
		big.NewInt(10000),
	)
	if basisPoints < 0 {
		magnitude.Neg(magnitude)
	}
	return magnitude, nil
}

func absInt64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}
