package trading

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/limiance/backend/internal/trading/protocol"
)

type storeStub struct {
	create        func(CreateOrderCommand) (Order, error)
	apply         func(string, protocol.OrderStatusEvent) (Order, error)
	get           func(string, string, string) (Order, error)
	prepareCancel func(CancelOrderCommand) (Order, error)
	applyCancel   func(string, string, protocol.ControlAck) (Order, error)
	activate      func(string, uint64, int) ([]Order, error)
	failureCount  int
	lastFailure   string
}

func (store *storeStub) CreateOrder(_ context.Context, command CreateOrderCommand) (Order, error) {
	return store.create(command)
}

func (store *storeStub) ApplyEngineStatus(_ context.Context, orderID string, status protocol.OrderStatusEvent) (Order, error) {
	return store.apply(orderID, status)
}

func (store *storeStub) ListOrders(context.Context, string, string, OrderFilter) ([]Order, error) {
	return nil, nil
}

func (store *storeStub) GetOrder(_ context.Context, userID, accountID, orderID string) (Order, error) {
	if store.get != nil {
		return store.get(userID, accountID, orderID)
	}
	return Order{ID: orderID, UserID: userID, AccountID: accountID, Pair: "BTCUSDT", Status: "OPEN"}, nil
}

func (store *storeStub) PrepareCancel(_ context.Context, command CancelOrderCommand) (Order, error) {
	if store.prepareCancel != nil {
		return store.prepareCancel(command)
	}
	return Order{ID: command.OrderID, UserID: command.UserID, AccountID: command.AccountID, Pair: "BTCUSDT", Status: "PENDING_CANCEL"}, nil
}

func (store *storeStub) ApplyCancelAck(_ context.Context, userID, orderID string, ack protocol.ControlAck) (Order, error) {
	if store.applyCancel != nil {
		return store.applyCancel(userID, orderID, ack)
	}
	return Order{ID: orderID, UserID: userID, Pair: "BTCUSDT", Status: "CANCELED"}, nil
}

func (store *storeStub) ActivateConditionalOrders(_ context.Context, pair string, markPrice uint64, limit int) ([]Order, error) {
	if store.activate != nil {
		return store.activate(pair, markPrice, limit)
	}
	return nil, nil
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

type controlEngineStub struct {
	requests int
	err      error
}

func (engine *controlEngineStub) Request(payload []byte) ([]byte, error) {
	engine.requests++
	if engine.err != nil {
		return nil, engine.err
	}
	command, err := protocol.DecodeCancelOrderCommand(payload)
	if err != nil {
		return nil, err
	}
	return protocol.EncodeControlAck(protocol.ControlAck{CommandID: command.CommandID, Accepted: true, SequenceID: 9})
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

func TestOrderGatewayKeepsConditionalOrderOutOfEngineUntilTriggered(t *testing.T) {
	var captured CreateOrderCommand
	store := &storeStub{
		create: func(command CreateOrderCommand) (Order, error) {
			captured = command
			order := pendingOrder(command)
			order.Status = "CONDITIONAL"
			return order, nil
		},
		apply: func(string, protocol.OrderStatusEvent) (Order, error) { return Order{}, nil },
	}
	engine := &engineStub{status: protocol.OrderStatusOpen}
	service := NewService(store, feeResolverStub{tier: FeeTier{TakerFeeBPS: 10}}, engine)
	service.newOrderID = func() (string, error) { return "00000000-0000-4000-8000-000000000001", nil }
	input := validLimitBuy()
	input.Type = "STOP_LIMIT"
	input.TriggerPrice = "5100000000000"
	order, err := service.PlaceOrder(context.Background(), input)
	if err != nil {
		t.Fatalf("place conditional order: %v", err)
	}
	if order.Status != "CONDITIONAL" || engine.requests != 0 {
		t.Fatalf("conditional order reached engine early: order=%#v calls=%d", order, engine.requests)
	}
	if !captured.Conditional || captured.TriggerPrice != 5100000000000 || captured.TriggerDirection != "UP" {
		t.Fatalf("conditional trigger was not persisted: %#v", captured)
	}
	ingress, err := protocol.DecodeOrderIngress(captured.EnginePayload)
	if err != nil || ingress.OrderType != protocol.OrderTypeLimit {
		t.Fatalf("triggered child must be a normal limit order: ingress=%#v err=%v", ingress, err)
	}
}

func TestOrderGatewaySupportsStopMarketAndTakeProfitDirections(t *testing.T) {
	tests := []struct {
		name, orderType, side, price, wantDirection string
	}{
		{name: "stop market sell", orderType: "STOP_MARKET", side: "SELL", price: "", wantDirection: "DOWN"},
		{name: "take profit buy", orderType: "TAKE_PROFIT_LIMIT", side: "BUY", price: "5000000000000", wantDirection: "DOWN"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var captured CreateOrderCommand
			store := &storeStub{
				create: func(command CreateOrderCommand) (Order, error) {
					captured = command
					order := pendingOrder(command)
					order.Status = "CONDITIONAL"
					return order, nil
				},
				apply: func(string, protocol.OrderStatusEvent) (Order, error) { return Order{}, nil },
			}
			service := NewService(store, feeResolverStub{}, &engineStub{})
			service.newOrderID = func() (string, error) { return "00000000-0000-4000-8000-000000000001", nil }
			input := validLimitBuy()
			input.Type, input.Side, input.Price, input.TriggerPrice = test.orderType, test.side, test.price, "4900000000000"
			if _, err := service.PlaceOrder(context.Background(), input); err != nil {
				t.Fatalf("place conditional order: %v", err)
			}
			if captured.TriggerDirection != test.wantDirection {
				t.Fatalf("got trigger direction %q, want %q", captured.TriggerDirection, test.wantDirection)
			}
		})
	}
}

func TestOrderGatewayCancelsThroughControlContract(t *testing.T) {
	control := &controlEngineStub{}
	store := &storeStub{
		create: func(CreateOrderCommand) (Order, error) { return Order{}, nil },
		apply:  func(string, protocol.OrderStatusEvent) (Order, error) { return Order{}, nil },
		get: func(userID, accountID, orderID string) (Order, error) {
			return Order{ID: orderID, UserID: userID, AccountID: accountID, Pair: "BTCUSDT", Status: "OPEN"}, nil
		},
		prepareCancel: func(command CancelOrderCommand) (Order, error) {
			decoded, err := protocol.DecodeCancelOrderCommand(command.Payload)
			if err != nil || decoded.OrderID != command.OrderID || decoded.Pair != "BTCUSDT" {
				t.Fatalf("invalid persisted cancel command: decoded=%#v err=%v", decoded, err)
			}
			return Order{ID: command.OrderID, UserID: command.UserID, AccountID: command.AccountID, Pair: "BTCUSDT", Status: "PENDING_CANCEL"}, nil
		},
		applyCancel: func(_ string, orderID string, ack protocol.ControlAck) (Order, error) {
			if ack.SequenceID != 9 {
				t.Fatalf("unexpected cancel sequence %d", ack.SequenceID)
			}
			return Order{ID: orderID, Status: "CANCELED"}, nil
		},
	}
	service := NewService(store, feeResolverStub{}, &engineStub{}).WithControlEngine(control)
	service.now = func() time.Time { return time.Unix(1724880000, 0) }
	order, err := service.CancelOrder(context.Background(), CancelOrderInput{UserID: "user-1", AccountID: "account-1", OrderID: "order-1", IdempotencyKey: "cancel-1"})
	if err != nil {
		t.Fatalf("cancel order: %v", err)
	}
	if order.Status != "CANCELED" || control.requests != 1 {
		t.Fatalf("unexpected cancellation: order=%#v calls=%d", order, control.requests)
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

func TestOrderGatewayRejectsInvalidListFilters(t *testing.T) {
	service := NewService(&storeStub{}, feeResolverStub{}, &engineStub{})
	if _, err := service.ListOrders(context.Background(), "user-1", "account-1", OrderFilter{Status: "DELETED"}); !errors.Is(err, ErrInvalidOrder) {
		t.Fatalf("expected invalid status error, got %v", err)
	}
	if _, err := service.ListOrders(context.Background(), "user-1", "account-1", OrderFilter{Pair: "BTC/USDT"}); !errors.Is(err, ErrInvalidOrder) {
		t.Fatalf("expected invalid pair error, got %v", err)
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
