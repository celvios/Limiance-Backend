package trading

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/limiance/backend/internal/trading/protocol"
)

type storeStub struct {
	create       func(CreateOrderCommand) (Order, error)
	apply        func(string, protocol.OrderStatusEvent) (Order, error)
	failureCount int
	lastFailure  string
}

func (store *storeStub) CreateOrder(_ context.Context, command CreateOrderCommand) (Order, error) {
	return store.create(command)
}

func (store *storeStub) ApplyEngineStatus(_ context.Context, orderID string, status protocol.OrderStatusEvent) (Order, error) {
	return store.apply(orderID, status)
}

func (store *storeStub) RecordDispatchFailure(_ context.Context, _ string, reason string) error {
	store.failureCount++
	store.lastFailure = reason
	return nil
}

type feeResolverStub struct {
	tier FeeTier
	err  error
}

func (resolver feeResolverStub) Resolve(context.Context, string, string) (FeeTier, error) {
	return resolver.tier, resolver.err
}

type engineStub struct {
	requests int
	status   protocol.OrderStatus
	err      error
}

func (engine *engineStub) Request(payload []byte) ([]byte, error) {
	engine.requests++
	if engine.err != nil {
		return nil, engine.err
	}
	order, err := protocol.DecodeOrderIngress(payload)
	if err != nil {
		return nil, err
	}
	return protocol.EncodeOrderStatusEvent(protocol.OrderStatusEvent{
		SequenceID: 1, OrderID: order.ID, Status: engine.status, RemainingQuantity: order.Quantity,
	})
}

func TestOrderGatewayPlacesLimitBuy(t *testing.T) {
	var captured CreateOrderCommand
	store := &storeStub{}
	store.create = func(command CreateOrderCommand) (Order, error) {
		captured = command
		return pendingOrder(command), nil
	}
	store.apply = func(orderID string, status protocol.OrderStatusEvent) (Order, error) {
		return Order{ID: orderID, Status: "OPEN", Quantity: "1000000", RemainingQuantity: "1000000"}, nil
	}
	engine := &engineStub{status: protocol.OrderStatusOpen}
	service := NewService(store, feeResolverStub{tier: FeeTier{Level: 2, MakerFeeBPS: 6, TakerFeeBPS: 10}}, engine)
	service.now = func() time.Time { return time.Unix(1724880000, 0) }
	service.newOrderID = func() (string, error) { return "00000000-0000-4000-8000-000000000001", nil }

	order, err := service.PlaceOrder(context.Background(), validLimitBuy())
	if err != nil {
		t.Fatalf("place order: %v", err)
	}
	if order.Status != "OPEN" || engine.requests != 1 {
		t.Fatalf("unexpected order or engine calls: %#v, calls=%d", order, engine.requests)
	}
	if captured.HoldAmount != "50050000000" || captured.HoldAllAvailable {
		t.Fatalf("unexpected reservation: amount=%q all=%v", captured.HoldAmount, captured.HoldAllAvailable)
	}
	if captured.FeeTier != 2 || len(captured.EnginePayload) == 0 {
		t.Fatalf("fee tier or engine payload not persisted: %#v", captured)
	}
	ingress, err := protocol.DecodeOrderIngress(captured.EnginePayload)
	if err != nil {
		t.Fatalf("decode captured ingress: %v", err)
	}
	if ingress.Price != 5000000000000 || ingress.Quantity != 1000000 || ingress.FeeTier != 2 {
		t.Fatalf("wrong ingress values: %#v", ingress)
	}
}

func TestOrderGatewayMarketBuyHoldsAllAvailableQuote(t *testing.T) {
	var captured CreateOrderCommand
	store := &storeStub{
		create: func(command CreateOrderCommand) (Order, error) {
			captured = command
			return pendingOrder(command), nil
		},
		apply: func(orderID string, _ protocol.OrderStatusEvent) (Order, error) {
			return Order{ID: orderID, Status: "OPEN"}, nil
		},
	}
	service := NewService(store, feeResolverStub{tier: FeeTier{Level: 0, TakerFeeBPS: 10}}, &engineStub{status: protocol.OrderStatusOpen})
	service.newOrderID = func() (string, error) { return "00000000-0000-4000-8000-000000000001", nil }
	input := validLimitBuy()
	input.Type, input.Price, input.TimeInForce = "MARKET", "", ""
	if _, err := service.PlaceOrder(context.Background(), input); err != nil {
		t.Fatalf("place market buy: %v", err)
	}
	if !captured.HoldAllAvailable || captured.HoldAmount != "" || captured.TimeInForce != "IOC" {
		t.Fatalf("market buy did not use safe reservation: %#v", captured)
	}
}

func TestOrderGatewayReturnsExistingIdempotentOrder(t *testing.T) {
	store := &storeStub{
		create: func(command CreateOrderCommand) (Order, error) {
			order := pendingOrder(command)
			order.Status, order.Duplicate = "OPEN", true
			return order, nil
		},
		apply: func(string, protocol.OrderStatusEvent) (Order, error) {
			t.Fatal("engine status must not be applied for completed duplicate")
			return Order{}, nil
		},
	}
	engine := &engineStub{status: protocol.OrderStatusOpen}
	service := NewService(store, feeResolverStub{tier: FeeTier{}}, engine)
	service.newOrderID = func() (string, error) { return "00000000-0000-4000-8000-000000000001", nil }
	order, err := service.PlaceOrder(context.Background(), validLimitBuy())
	if err != nil {
		t.Fatalf("place duplicate order: %v", err)
	}
	if !order.Duplicate || engine.requests != 0 {
		t.Fatalf("duplicate was dispatched again: %#v, calls=%d", order, engine.requests)
	}
}

func TestOrderGatewayPropagatesInsufficientBalanceWithoutDispatch(t *testing.T) {
	store := &storeStub{
		create: func(CreateOrderCommand) (Order, error) { return Order{}, ErrInsufficientBalance },
		apply:  func(string, protocol.OrderStatusEvent) (Order, error) { return Order{}, nil },
	}
	engine := &engineStub{status: protocol.OrderStatusOpen}
	service := NewService(store, feeResolverStub{tier: FeeTier{}}, engine)
	service.newOrderID = func() (string, error) { return "00000000-0000-4000-8000-000000000001", nil }
	_, err := service.PlaceOrder(context.Background(), validLimitBuy())
	if !errors.Is(err, ErrInsufficientBalance) || engine.requests != 0 {
		t.Fatalf("expected balance error without dispatch, got err=%v calls=%d", err, engine.requests)
	}
}

func TestOrderGatewayRecordsEngineFailure(t *testing.T) {
	store := &storeStub{
		create: func(command CreateOrderCommand) (Order, error) { return pendingOrder(command), nil },
		apply:  func(string, protocol.OrderStatusEvent) (Order, error) { return Order{}, nil },
	}
	service := NewService(store, feeResolverStub{tier: FeeTier{}}, &engineStub{err: errors.New("connection reset")})
	service.newOrderID = func() (string, error) { return "00000000-0000-4000-8000-000000000001", nil }
	_, err := service.PlaceOrder(context.Background(), validLimitBuy())
	if !errors.Is(err, ErrEngineUnavailable) || store.failureCount != 1 || store.lastFailure == "" {
		t.Fatalf("engine failure was not recorded: err=%v count=%d reason=%q", err, store.failureCount, store.lastFailure)
	}
}

func TestOrderGatewayRejectsInvalidRequestsBeforeSideEffects(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*PlaceOrderInput)
		want   error
	}{
		{name: "missing idempotency", mutate: func(input *PlaceOrderInput) { input.IdempotencyKey = "" }, want: ErrIdempotencyRequired},
		{name: "funding account", mutate: func(input *PlaceOrderInput) { input.AccountKind = "funding" }, want: ErrTradingAccountRequired},
		{name: "decimal quantity", mutate: func(input *PlaceOrderInput) { input.Quantity = "0.01" }, want: ErrInvalidOrder},
		{name: "float price", mutate: func(input *PlaceOrderInput) { input.Price = "50000.00" }, want: ErrInvalidOrder},
		{name: "post only market", mutate: func(input *PlaceOrderInput) { input.Type, input.Price, input.PostOnly = "MARKET", "", true }, want: ErrInvalidOrder},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := validLimitBuy()
			test.mutate(&input)
			store := &storeStub{
				create: func(CreateOrderCommand) (Order, error) { t.Fatal("store should not be called"); return Order{}, nil },
				apply:  func(string, protocol.OrderStatusEvent) (Order, error) { return Order{}, nil },
			}
			service := NewService(store, feeResolverStub{}, &engineStub{})
			if _, err := service.PlaceOrder(context.Background(), input); !errors.Is(err, test.want) {
				t.Fatalf("got %v, want %v", err, test.want)
			}
		})
	}
}

func validLimitBuy() PlaceOrderInput {
	return PlaceOrderInput{
		UserID: "user-1", AccountID: "account-1", AccountKind: "uta", IdempotencyKey: "request-1",
		Pair: "BTCUSDT", Side: "BUY", Type: "LIMIT", Price: "5000000000000", Quantity: "1000000", TimeInForce: "GTC",
	}
}

func pendingOrder(command CreateOrderCommand) Order {
	return Order{
		ID: command.OrderID, UserID: command.UserID, AccountID: command.AccountID, Pair: command.Pair,
		Side: command.Side, Type: command.Type, Price: "5000000000000", Quantity: "1000000",
		FilledQuantity: "0", RemainingQuantity: "1000000", Status: "PENDING", EnginePayload: command.EnginePayload,
	}
}
