package postgres

import (
	"testing"

	"github.com/limiance/backend/internal/trading/protocol"
)

func TestPairLimitsUseAtomicIntegerArithmetic(t *testing.T) {
	tests := []struct {
		name     string
		quantity uint64
		price    uint64
		want     bool
	}{
		{name: "valid ticks", quantity: 1000000, price: 5000000000000, want: true},
		{name: "below minimum", quantity: 99, price: 5000000000000, want: false},
		{name: "above maximum", quantity: 1000001, price: 5000000000000, want: false},
		{name: "quantity off step", quantity: 100050, price: 5000000000000, want: false},
		{name: "price off tick", quantity: 100000, price: 5000000000001, want: false},
		{name: "market price omitted", quantity: 100000, price: 0, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := withinPairLimits(test.quantity, test.price, "100", "1000000", "10", "100")
			if got != test.want {
				t.Fatalf("got %v, want %v", got, test.want)
			}
		})
	}
}

func TestEngineStatusTransitions(t *testing.T) {
	if statusName(protocol.OrderStatusPartial) != "PARTIALLY_FILLED" {
		t.Fatal("partial status does not match API/database contract")
	}
	if !validTransition("PENDING", "OPEN") || !validTransition("OPEN", "FILLED") || !validTransition("PARTIALLY_FILLED", "CANCELED") {
		t.Fatal("valid order transition rejected")
	}
	if validTransition("FILLED", "OPEN") || validTransition("CANCELED", "FILLED") {
		t.Fatal("terminal order transition accepted")
	}
}

func TestFilledAndRemainingMustEqualOriginalQuantity(t *testing.T) {
	if !quantitiesBalance("2000000", 500000, 1500000) {
		t.Fatal("valid quantities rejected")
	}
	if quantitiesBalance("2000000", 500000, 1400000) {
		t.Fatal("invalid quantities accepted")
	}
}
