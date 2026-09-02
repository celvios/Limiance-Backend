package trading

import (
	"context"
	"errors"
	"time"

	"github.com/limiance/backend/internal/trading/protocol"
)

var (
	ErrInvalidOrder           = errors.New("invalid order")
	ErrIdempotencyRequired    = errors.New("idempotency key is required")
	ErrIdempotencyConflict    = errors.New("idempotency key was already used for a different request")
	ErrInsufficientBalance    = errors.New("insufficient balance")
	ErrTradingAccountRequired = errors.New("a unified trading or subaccount context is required")
	ErrPairUnavailable        = errors.New("trading pair is unavailable")
	ErrEngineUnavailable      = errors.New("matching engine is unavailable")
	ErrEngineResponse         = errors.New("matching engine returned an invalid response")
	ErrOrderNotFound          = errors.New("order not found")
	ErrOrderAlreadyFilled     = errors.New("order is already filled")
	ErrOrderNotCancelable     = errors.New("order cannot be canceled")
)

type PlaceOrderInput struct {
	UserID         string
	AccountID      string
	AccountKind    string
	IdempotencyKey string
	Pair           string
	Side           string
	Type           string
	Price          string
	TriggerPrice   string
	Quantity       string
	TimeInForce    string
	PostOnly       bool
	ReduceOnly     bool
}

type FeeTier struct {
	Level       int
	MakerFeeBPS int
	TakerFeeBPS int
}

type Order struct {
	ID                string    `json:"order_id"`
	UserID            string    `json:"-"`
	AccountID         string    `json:"account_id"`
	Pair              string    `json:"pair"`
	Side              string    `json:"side"`
	Type              string    `json:"type"`
	Price             string    `json:"price"`
	TriggerPrice      string    `json:"trigger_price,omitempty"`
	Quantity          string    `json:"quantity"`
	FilledQuantity    string    `json:"filled_quantity"`
	RemainingQuantity string    `json:"remaining_quantity"`
	AvgPrice          string    `json:"avg_price"`
	TimeInForce       string    `json:"time_in_force"`
	Status            string    `json:"status"`
	PostOnly          bool      `json:"post_only"`
	ReduceOnly        bool      `json:"reduce_only"`
	FeeTier           int       `json:"fee_tier"`
	Duplicate         bool      `json:"-"`
	EnginePayload     []byte    `json:"-"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type CreateOrderCommand struct {
	OrderID          string
	UserID           string
	AccountID        string
	Pair             string
	Side             string
	Type             string
	Price            uint64
	Quantity         uint64
	TimeInForce      string
	PostOnly         bool
	ReduceOnly       bool
	FeeTier          int
	IdempotencyKey   string
	RequestHash      [32]byte
	HoldAmount       string
	HoldAllAvailable bool
	Conditional      bool
	TriggerPrice     uint64
	TriggerDirection string
	EnginePayload    []byte
}

type OrderFilter struct {
	Status  string
	Pair    string
	History bool
	Limit   int
	Offset  int
}

type PairRules struct {
	PriceScale    int
	QuantityScale int
	QuoteScale    int
}

type CancelOrderInput struct {
	UserID         string
	AccountID      string
	OrderID        string
	IdempotencyKey string
}

type CancelOrderCommand struct {
	UserID         string
	AccountID      string
	OrderID        string
	CommandID      string
	IdempotencyKey string
	Payload        []byte
}

type Store interface {
	PairRules(context.Context, string) (PairRules, error)
	CreateOrder(context.Context, CreateOrderCommand) (Order, error)
	ListOrders(context.Context, string, string, OrderFilter) ([]Order, error)
	GetOrder(context.Context, string, string, string) (Order, error)
	PrepareCancel(context.Context, CancelOrderCommand) (Order, error)
	ApplyCancelAck(context.Context, string, string, protocol.ControlAck) (Order, error)
	ActivateConditionalOrders(context.Context, string, uint64, int) ([]Order, error)
	ApplyEngineStatus(context.Context, string, protocol.OrderStatusEvent) (Order, error)
	RecordDispatchFailure(context.Context, string, string) error
}

type FeeResolver interface {
	Resolve(context.Context, string, string) (FeeTier, error)
}

type Engine interface {
	Request([]byte) ([]byte, error)
}
