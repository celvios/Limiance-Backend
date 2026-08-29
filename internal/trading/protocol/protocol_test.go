package protocol

import "testing"

func TestOrderIngressValidate(t *testing.T) {
	t.Run("accepts a minimal valid order", func(t *testing.T) {
		order := OrderIngress{
			ID:          "b8f7e3f2-4dd4-4b67-b0c5-9c8d6b2f4d1d",
			UserID:      "c7c6fae1-2e3b-4d8f-8d32-0c97d32b0c1d",
			Pair:        "BTCUSDT",
			Side:        OrderSideBuy,
			OrderType:   OrderTypeLimit,
			Price:       5000050000000,
			Quantity:    100000000,
			TimeInForce: TimeInForceGTC,
			TimestampNS: 1724880000000000000,
			FeeTier:     0,
		}
		if err := order.Validate(); err != nil {
			t.Fatalf("expected valid order, got error: %v", err)
		}
	})

	t.Run("rejects missing core fields", func(t *testing.T) {
		order := OrderIngress{}
		if err := order.Validate(); err == nil {
			t.Fatal("expected validation error")
		}
	})
}

func TestEnumValuesMatchSchema(t *testing.T) {
	if OrderSideBuy != 0 || OrderSideSell != 1 {
		t.Fatal("order side values do not match schema")
	}
	if OrderTypeLimit != 0 || OrderTypeStopLimit != 2 {
		t.Fatal("order type values do not match schema")
	}
	if TimeInForceGTC != 0 || TimeInForceFOK != 2 {
		t.Fatal("time-in-force values do not match schema")
	}
	if OrderStatusOpen != 0 || OrderStatusRejected != 4 {
		t.Fatal("order status values do not match schema")
	}
}
