package trading

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/limiance/backend/internal/trading/protocol"
)

type settlementStoreStub struct {
	trade Trade
	err   error
	calls int
}

func (stub *settlementStoreStub) SettleTrade(_ context.Context, _ protocol.TradeEvent, _ [32]byte) (Trade, error) {
	stub.calls++
	return stub.trade, stub.err
}

type replayRequesterStub struct {
	pair string
	from uint64
	err  error
}

type replayControlEngineStub struct {
	request protocol.ReplayRequest
}

func (stub *replayControlEngineStub) Request(payload []byte) ([]byte, error) {
	request, err := protocol.DecodeReplayRequest(payload)
	if err != nil {
		return nil, err
	}
	stub.request = request
	return protocol.EncodeControlAck(protocol.ControlAck{CommandID: request.CommandID, Accepted: true, SequenceID: 12})
}

func (stub *replayRequesterStub) RequestReplay(_ context.Context, pair string, from uint64) error {
	stub.pair, stub.from = pair, from
	return stub.err
}

func TestSettlementIntegerFeeCalculation(t *testing.T) {
	notional, err := QuoteAmount(5000000000000, 1000000)
	if err != nil || notional.String() != "50000000000" {
		t.Fatalf("quote amount=%v err=%v", notional, err)
	}
	positive, err := FeeAmount(notional, 10)
	if err != nil || positive.String() != "50000000" {
		t.Fatalf("positive fee=%v err=%v", positive, err)
	}
	rebate, err := FeeAmount(notional, -4)
	if err != nil || rebate.String() != "-20000000" {
		t.Fatalf("maker rebate=%v err=%v", rebate, err)
	}
	rounded, err := FeeAmount(big.NewInt(9999), 1)
	if err != nil || rounded.Sign() != 0 {
		t.Fatalf("fee must round down, got %v err=%v", rounded, err)
	}
}

func TestQuoteAmountUsesPairScales(t *testing.T) {
	notional, err := QuoteAmountForScales(300000000000, 1000000000000000000, PairRules{PriceScale: 8, QuantityScale: 18, QuoteScale: 6})
	if err != nil || notional.String() != "3000000000" {
		t.Fatalf("scaled quote amount=%v err=%v", notional, err)
	}
}

func TestSettlementConsumerRequestsReplayOnSequenceGap(t *testing.T) {
	event := validTradeEvent()
	payload, err := protocol.EncodeTradeEvent(event)
	if err != nil {
		t.Fatalf("encode trade event: %v", err)
	}
	store := &settlementStoreStub{err: &SequenceGapError{Pair: event.Pair, Expected: 3, Received: 5}}
	replay := &replayRequesterStub{}
	consumer := NewSettlementConsumer(store, replay)
	_, err = consumer.ProcessPayload(context.Background(), payload)
	if !errors.Is(err, ErrSequenceGap) {
		t.Fatalf("expected sequence gap, got %v", err)
	}
	if replay.pair != "BTCUSDT" || replay.from != 3 {
		t.Fatalf("unexpected replay request: pair=%q from=%d", replay.pair, replay.from)
	}
}

func TestSettlementConsumerDoesNotHideReplayFailure(t *testing.T) {
	payload, err := protocol.EncodeTradeEvent(validTradeEvent())
	if err != nil {
		t.Fatalf("encode trade event: %v", err)
	}
	replayFailure := errors.New("control channel unavailable")
	consumer := NewSettlementConsumer(
		&settlementStoreStub{err: &SequenceGapError{Pair: "BTCUSDT", Expected: 3, Received: 5}},
		&replayRequesterStub{err: replayFailure},
	)
	_, err = consumer.ProcessPayload(context.Background(), payload)
	if !errors.Is(err, replayFailure) || errors.Is(err, ErrSequenceGap) {
		t.Fatalf("replay failure was hidden by sequence gap: %v", err)
	}
}

func TestSettlementConsumerRejectsMalformedPayloadBeforeStore(t *testing.T) {
	store := &settlementStoreStub{}
	consumer := NewSettlementConsumer(store, nil)
	if _, err := consumer.ProcessPayload(context.Background(), []byte{1, 2, 3}); !errors.Is(err, ErrSettlementInvalid) {
		t.Fatalf("expected invalid settlement event, got %v", err)
	}
	if store.calls != 0 {
		t.Fatalf("malformed payload reached store %d times", store.calls)
	}
}

func TestSettlementReplayUsesControlContract(t *testing.T) {
	engine := &replayControlEngineStub{}
	requester := NewControlReplayRequester(engine)
	requester.newID = func() (string, error) { return "00000000-0000-4000-8000-000000000010", nil }
	if err := requester.RequestReplay(context.Background(), "BTCUSDT", 7); err != nil {
		t.Fatalf("request replay: %v", err)
	}
	if engine.request.Pair != "BTCUSDT" || engine.request.FromSequence != 7 {
		t.Fatalf("unexpected replay control request: %#v", engine.request)
	}
}

func validTradeEvent() protocol.TradeEvent {
	return protocol.TradeEvent{
		SequenceID: 1, TimestampNS: 1724880000000000000, Pair: "BTCUSDT",
		MakerOrderID: "00000000-0000-4000-8000-000000000001", TakerOrderID: "00000000-0000-4000-8000-000000000002",
		MakerUserID: "00000000-0000-4000-8000-000000000003", TakerUserID: "00000000-0000-4000-8000-000000000004",
		Price: 5000000000000, Quantity: 1000000, MakerFeeBPS: 10, TakerFeeBPS: 10,
	}
}
