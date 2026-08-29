package protocol

import (
	"encoding/hex"
	"reflect"
	"testing"

	tradingwire "github.com/limiance/backend/internal/trading/wire/tradingwire"
)

const orderIngressGoldenHex = "200000001c003c003800340030002f00000020001800170016000000080007001c0000000000000300002da03801f017000000000000010140420f00000000008040342a8c04000000000000000000010c000000140000001c00000007000000425443555344540006000000757365722d310000070000006f726465722d3100"

func TestOrderIngressWireRoundTrip(t *testing.T) {
	want := OrderIngress{
		ID: "order-1", UserID: "user-1", Pair: "BTCUSDT", Side: OrderSideSell,
		OrderType: OrderTypeLimit, Price: 5000050000000, Quantity: 1000000,
		TimeInForce: TimeInForceIOC, PostOnly: true, TimestampNS: 1724880000000000000, FeeTier: 3,
	}
	encoded, err := EncodeOrderIngress(want)
	if err != nil {
		t.Fatalf("encode order: %v", err)
	}
	got, err := DecodeOrderIngress(encoded)
	if err != nil {
		t.Fatalf("decode order: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip mismatch\ngot:  %#v\nwant: %#v", got, want)
	}
}

func TestOrderIngressWireContractIsFrozen(t *testing.T) {
	order := OrderIngress{
		ID: "order-1", UserID: "user-1", Pair: "BTCUSDT", Side: OrderSideSell,
		OrderType: OrderTypeLimit, Price: 5000050000000, Quantity: 1000000,
		TimeInForce: TimeInForceIOC, PostOnly: true, TimestampNS: 1724880000000000000, FeeTier: 3,
	}
	encoded, err := EncodeOrderIngress(order)
	if err != nil {
		t.Fatalf("encode order: %v", err)
	}
	if got := hex.EncodeToString(encoded); got != orderIngressGoldenHex {
		t.Fatalf("wire bytes changed\ngot:  %s\nwant: %s", got, orderIngressGoldenHex)
	}
}

func TestTradeEventWireRoundTrip(t *testing.T) {
	want := TradeEvent{
		SequenceID: 44, TimestampNS: 1724880000000000100, Pair: "BTCUSDT",
		MakerOrderID: "maker-order", TakerOrderID: "taker-order", MakerUserID: "maker-user", TakerUserID: "taker-user",
		Price: 5000050000000, Quantity: 1000000, MakerFeeBPS: -2, TakerFeeBPS: 10,
	}
	encoded, err := EncodeTradeEvent(want)
	if err != nil {
		t.Fatalf("encode trade: %v", err)
	}
	got, err := DecodeTradeEvent(encoded)
	if err != nil {
		t.Fatalf("decode trade: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip mismatch\ngot:  %#v\nwant: %#v", got, want)
	}
}

func TestOrderStatusEventWireRoundTrip(t *testing.T) {
	want := OrderStatusEvent{SequenceID: 45, OrderID: "order-1", Status: OrderStatusPartial, FilledQuantity: 400000, RemainingQuantity: 600000, AvgPrice: 5000050000000}
	encoded, err := EncodeOrderStatusEvent(want)
	if err != nil {
		t.Fatalf("encode status: %v", err)
	}
	got, err := DecodeOrderStatusEvent(encoded)
	if err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip mismatch\ngot:  %#v\nwant: %#v", got, want)
	}
}

func TestMalformedWireMessageRejected(t *testing.T) {
	for _, data := range [][]byte{nil, {1, 2, 3}, {255, 255, 255, 255}, make([]byte, 32)} {
		if _, err := DecodeOrderIngress(data); err == nil {
			t.Fatalf("expected malformed message %v to be rejected", data)
		}
	}
}

func TestInvalidEnumWireMessageRejected(t *testing.T) {
	encoded, err := EncodeOrderIngress(OrderIngress{
		ID: "order-1", UserID: "user-1", Pair: "BTCUSDT", Side: OrderSideSell,
		OrderType: OrderTypeLimit, Price: 5000050000000, Quantity: 1000000, TimeInForce: TimeInForceGTC,
	})
	if err != nil {
		t.Fatalf("encode order: %v", err)
	}
	message := tradingwire.GetRootAsOrderIngress(encoded, 0)
	if !message.MutateSide(tradingwire.OrderSide(99)) {
		t.Fatal("expected side field to be mutable")
	}
	if _, err := DecodeOrderIngress(encoded); err == nil {
		t.Fatal("expected invalid side to be rejected")
	}
}
