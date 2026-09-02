package marketmaker

import (
	"context"
	"errors"
	"time"

	"github.com/limiance/backend/internal/marketdata"
	"github.com/limiance/backend/internal/trading"
)

var (
	ErrDisabled          = errors.New("market maker disabled")
	ErrKillSwitch        = errors.New("market maker kill switch enabled")
	ErrReferenceStale    = errors.New("market maker reference feeds are stale")
	ErrReferenceDiverged = errors.New("market maker reference feeds diverged")
	ErrRiskLimit         = errors.New("market maker risk limit reached")
)

type Control struct {
	Enabled, DryRun, KillSwitch bool
	UserID, AccountID           string
}

type Config struct {
	Pair                   string
	Enabled                bool
	PriceScale             int
	QuantityScale          int
	QuoteScale             int
	PriceTickAtomic        string
	SpreadBPS              int64
	QuantityAtomic         string
	MaxBaseInventoryAtomic string
	MaxQuoteNotionalAtomic string
	MaxDailyLossAtomic     string
	MaxDivergenceBPS       int64
	StaleAfter             time.Duration
}

type RiskSnapshot struct {
	BaseInventoryAtomic string
	DailyPnLQuoteAtomic string
}

type NamedProvider struct {
	Name     string
	Provider marketdata.SpotProvider
}

type Quote struct {
	Pair, ReferencePriceAtomic, BidPriceAtomic, AskPriceAtomic, QuantityAtomic string
	ObservedAt                                                                 time.Time
}

type Decision struct {
	Pair, Status, Reason, ReferencePriceAtomic string
	At                                         time.Time
}

type Store interface {
	LoadControl(context.Context) (Control, error)
	ListConfigs(context.Context) ([]Config, error)
	Risk(context.Context, string, string) (RiskSnapshot, error)
	ClaimCommand(context.Context, string, string, string, string, string, bool) (bool, error)
	AttachOrder(context.Context, string, string) error
	RecordDecision(context.Context, Decision) error
}

type Gateway interface {
	PlaceOrder(context.Context, trading.PlaceOrderInput) (trading.Order, error)
	ListOrders(context.Context, string, string, trading.OrderFilter) ([]trading.Order, error)
	CancelOrder(context.Context, trading.CancelOrderInput) (trading.Order, error)
}
