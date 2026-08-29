package protocol

import "fmt"

const (
	MaxWireMessageBytes = 8 << 20
	MaxIdentifierBytes  = 128
	MaxPairBytes        = 20
)

type OrderSide uint8

const (
	OrderSideBuy  OrderSide = 0
	OrderSideSell OrderSide = 1
)

type OrderType uint8

const (
	OrderTypeLimit     OrderType = 0
	OrderTypeMarket    OrderType = 1
	OrderTypeStopLimit OrderType = 2
)

type TimeInForce uint8

const (
	TimeInForceGTC TimeInForce = 0
	TimeInForceIOC TimeInForce = 1
	TimeInForceFOK TimeInForce = 2
)

type OrderStatus uint8

const (
	OrderStatusOpen     OrderStatus = 0
	OrderStatusPartial  OrderStatus = 1
	OrderStatusFilled   OrderStatus = 2
	OrderStatusCanceled OrderStatus = 3
	OrderStatusRejected OrderStatus = 4
)

type OrderIngress struct {
	ID          string
	UserID      string
	Pair        string
	Side        OrderSide
	OrderType   OrderType
	Price       uint64
	Quantity    uint64
	TimeInForce TimeInForce
	PostOnly    bool
	ReduceOnly  bool
	TimestampNS uint64
	FeeTier     uint8
}

func (o OrderIngress) Validate() error {
	if o.ID == "" {
		return fmt.Errorf("order ingress id is required")
	}
	if o.UserID == "" {
		return fmt.Errorf("order ingress user_id is required")
	}
	if o.Pair == "" {
		return fmt.Errorf("order ingress pair is required")
	}
	if len(o.ID) > MaxIdentifierBytes || len(o.UserID) > MaxIdentifierBytes {
		return fmt.Errorf("order ingress identifier exceeds %d bytes", MaxIdentifierBytes)
	}
	if len(o.Pair) > MaxPairBytes {
		return fmt.Errorf("order ingress pair exceeds %d bytes", MaxPairBytes)
	}
	if o.Quantity == 0 {
		return fmt.Errorf("order ingress quantity must be positive")
	}
	switch o.Side {
	case OrderSideBuy, OrderSideSell:
	default:
		return fmt.Errorf("order ingress side %d is invalid", o.Side)
	}
	switch o.OrderType {
	case OrderTypeLimit, OrderTypeMarket, OrderTypeStopLimit:
	default:
		return fmt.Errorf("order ingress order_type %d is invalid", o.OrderType)
	}
	if o.OrderType != OrderTypeMarket && o.Price == 0 {
		return fmt.Errorf("order ingress price must be positive for non-market orders")
	}
	switch o.TimeInForce {
	case TimeInForceGTC, TimeInForceIOC, TimeInForceFOK:
	default:
		return fmt.Errorf("order ingress time_in_force %d is invalid", o.TimeInForce)
	}
	return nil
}

type TradeEvent struct {
	SequenceID   uint64
	TimestampNS  uint64
	Pair         string
	MakerOrderID string
	TakerOrderID string
	MakerUserID  string
	TakerUserID  string
	Price        uint64
	Quantity     uint64
	MakerFeeBPS  int16
	TakerFeeBPS  int16
}

func (e TradeEvent) Validate() error {
	if e.SequenceID == 0 {
		return fmt.Errorf("trade event sequence_id must be positive")
	}
	if e.Pair == "" || len(e.Pair) > MaxPairBytes {
		return fmt.Errorf("trade event pair is invalid")
	}
	if e.MakerOrderID == "" || e.TakerOrderID == "" || e.MakerUserID == "" || e.TakerUserID == "" {
		return fmt.Errorf("trade event order and user identifiers are required")
	}
	if len(e.MakerOrderID) > MaxIdentifierBytes || len(e.TakerOrderID) > MaxIdentifierBytes || len(e.MakerUserID) > MaxIdentifierBytes || len(e.TakerUserID) > MaxIdentifierBytes {
		return fmt.Errorf("trade event identifier exceeds %d bytes", MaxIdentifierBytes)
	}
	if e.Price == 0 || e.Quantity == 0 {
		return fmt.Errorf("trade event price and quantity must be positive")
	}
	return nil
}

type OrderStatusEvent struct {
	SequenceID        uint64
	OrderID           string
	Status            OrderStatus
	FilledQuantity    uint64
	RemainingQuantity uint64
	AvgPrice          uint64
}

func (e OrderStatusEvent) Validate() error {
	if e.SequenceID == 0 {
		return fmt.Errorf("order status event sequence_id must be positive")
	}
	if e.OrderID == "" || len(e.OrderID) > MaxIdentifierBytes {
		return fmt.Errorf("order status event order_id is invalid")
	}
	switch e.Status {
	case OrderStatusOpen, OrderStatusPartial, OrderStatusFilled, OrderStatusCanceled, OrderStatusRejected:
	default:
		return fmt.Errorf("order status event status %d is invalid", e.Status)
	}
	return nil
}
