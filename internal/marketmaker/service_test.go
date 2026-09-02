package marketmaker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/limiance/backend/internal/marketdata"
	"github.com/limiance/backend/internal/trading"
)

type providerStub struct {
	ticker marketdata.SpotTicker
	err    error
}

func (provider providerStub) SpotTicker(context.Context, string) (marketdata.SpotTicker, error) {
	return provider.ticker, provider.err
}

type storeStub struct {
	control   Control
	configs   []Config
	risk      RiskSnapshot
	claims    map[string]bool
	decisions []Decision
}

func (store *storeStub) LoadControl(context.Context) (Control, error)  { return store.control, nil }
func (store *storeStub) ListConfigs(context.Context) ([]Config, error) { return store.configs, nil }
func (store *storeStub) Risk(context.Context, string, string) (RiskSnapshot, error) {
	return store.risk, nil
}
func (store *storeStub) ClaimCommand(_ context.Context, key, _, _, _, _ string, _ bool) (bool, error) {
	if store.claims == nil {
		store.claims = map[string]bool{}
	}
	if store.claims[key] {
		return false, nil
	}
	store.claims[key] = true
	return true, nil
}
func (*storeStub) AttachOrder(context.Context, string, string) error { return nil }
func (store *storeStub) RecordDecision(_ context.Context, decision Decision) error {
	store.decisions = append(store.decisions, decision)
	return nil
}

type gatewayStub struct {
	placed   []trading.PlaceOrderInput
	canceled int
	orders   []trading.Order
}

func (gateway *gatewayStub) PlaceOrder(_ context.Context, input trading.PlaceOrderInput) (trading.Order, error) {
	gateway.placed = append(gateway.placed, input)
	return trading.Order{ID: input.IdempotencyKey}, nil
}
func (gateway *gatewayStub) ListOrders(context.Context, string, string, trading.OrderFilter) ([]trading.Order, error) {
	return gateway.orders, nil
}
func (gateway *gatewayStub) CancelOrder(context.Context, trading.CancelOrderInput) (trading.Order, error) {
	gateway.canceled++
	return trading.Order{}, nil
}

func baseConfig() Config {
	return Config{Pair: "BTCUSDT", Enabled: true, PriceScale: 2, QuantityScale: 6, QuoteScale: 6, PriceTickAtomic: "1", SpreadBPS: 20, QuantityAtomic: "1000000", MaxBaseInventoryAtomic: "10000000", MaxQuoteNotionalAtomic: "200000000", MaxDailyLossAtomic: "1000000", MaxDivergenceBPS: 100, StaleAfter: 5 * time.Second}
}

func TestRiskNotionalUsesPairAndAssetScales(t *testing.T) {
	config := baseConfig()
	quote := Quote{AskPriceAtomic: "10000", QuantityAtomic: config.QuantityAtomic}
	if err := checkRisk(config, RiskSnapshot{BaseInventoryAtomic: "2000000", DailyPnLQuoteAtomic: "0"}, quote); err != nil {
		t.Fatal(err)
	}
	config.MaxQuoteNotionalAtomic = "99999999"
	if err := checkRisk(config, RiskSnapshot{BaseInventoryAtomic: "2000000", DailyPnLQuoteAtomic: "0"}, quote); !errors.Is(err, ErrRiskLimit) {
		t.Fatalf("expected scaled notional limit, got %v", err)
	}
}
func providers(at time.Time, prices ...string) []NamedProvider {
	result := make([]NamedProvider, 0, len(prices))
	for index, price := range prices {
		result = append(result, NamedProvider{Name: string(rune('a' + index)), Provider: providerStub{ticker: marketdata.SpotTicker{Symbol: "BTCUSDT", Bid: price, Ask: price, ObservedAt: at}}})
	}
	return result
}

func TestReferenceUsesMedianAndIntegerSpread(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	service := NewService(&storeStub{}, &gatewayStub{}, providers(now, "99.75", "100.00", "100.25"))
	quote, err := service.Reference(context.Background(), baseConfig(), now)
	if err != nil {
		t.Fatal(err)
	}
	if quote.ReferencePriceAtomic != "10000" || quote.BidPriceAtomic != "9990" || quote.AskPriceAtomic != "10010" {
		t.Fatalf("unexpected quote %+v", quote)
	}
}

func TestReferenceFailsClosedForStaleOrDivergentFeeds(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	service := NewService(&storeStub{}, &gatewayStub{}, providers(now.Add(-time.Minute), "100.00", "100.00", "100.00"))
	if _, err := service.Reference(context.Background(), baseConfig(), now); !errors.Is(err, ErrReferenceStale) {
		t.Fatalf("stale feeds: %v", err)
	}
	service = NewService(&storeStub{}, &gatewayStub{}, providers(now, "90.00", "100.00", "110.00"))
	if _, err := service.Reference(context.Background(), baseConfig(), now); !errors.Is(err, ErrReferenceDiverged) {
		t.Fatalf("divergent feeds: %v", err)
	}
}

func TestDryRunClaimsDeterministicCommandsWithoutGatewayOrders(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 2, 0, time.UTC)
	store := &storeStub{control: Control{Enabled: true, DryRun: true, UserID: "user", AccountID: "account"}, configs: []Config{baseConfig()}, risk: RiskSnapshot{BaseInventoryAtomic: "2000000", DailyPnLQuoteAtomic: "0"}}
	gateway := &gatewayStub{}
	service := NewService(store, gateway, providers(now, "100.00", "100.00", "100.00"))
	service.now = func() time.Time { return now }
	if err := service.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := service.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.claims) != 2 || len(gateway.placed) != 0 || store.decisions[len(store.decisions)-1].Status != "duplicate_cycle" {
		t.Fatalf("claims=%d orders=%d decisions=%+v", len(store.claims), len(gateway.placed), store.decisions)
	}
}

func TestLiveQuotesUseNormalGatewayAndInventoryLimitHalts(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 2, 0, time.UTC)
	store := &storeStub{control: Control{Enabled: true, DryRun: false, UserID: "user", AccountID: "account"}, configs: []Config{baseConfig()}, risk: RiskSnapshot{BaseInventoryAtomic: "2000000", DailyPnLQuoteAtomic: "0"}}
	gateway := &gatewayStub{}
	service := NewService(store, gateway, providers(now, "100.00", "100.00", "100.00"))
	service.now = func() time.Time { return now }
	if err := service.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(gateway.placed) != 2 || gateway.placed[0].AccountID != "account" || !gateway.placed[0].PostOnly {
		t.Fatalf("gateway orders %+v", gateway.placed)
	}
	store.risk.BaseInventoryAtomic = "9500001"
	if err := service.Cycle(context.Background()); !errors.Is(err, ErrRiskLimit) {
		t.Fatalf("inventory limit: %v", err)
	}
}

func TestRestartDuplicateDoesNotCancelExistingQuotes(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 2, 0, time.UTC)
	store := &storeStub{control: Control{Enabled: true, UserID: "user", AccountID: "account"}, configs: []Config{baseConfig()}, risk: RiskSnapshot{BaseInventoryAtomic: "2000000", DailyPnLQuoteAtomic: "0"}}
	gateway := &gatewayStub{orders: []trading.Order{{ID: "existing"}}}
	service := NewService(store, gateway, providers(now, "100.00", "100.00", "100.00"))
	service.now = func() time.Time { return now }
	if err := service.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	firstCancelCount := gateway.canceled
	if err := service.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gateway.canceled != firstCancelCount || store.decisions[len(store.decisions)-1].Status != "duplicate_cycle" {
		t.Fatalf("duplicate canceled quotes: before=%d after=%d decisions=%+v", firstCancelCount, gateway.canceled, store.decisions)
	}
}

func TestKillSwitchAndLossLimitHaltBeforeOrderPlacement(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	store := &storeStub{control: Control{Enabled: true, KillSwitch: true, UserID: "user", AccountID: "account"}, configs: []Config{baseConfig()}, risk: RiskSnapshot{BaseInventoryAtomic: "2000000", DailyPnLQuoteAtomic: "-1000000"}}
	gateway := &gatewayStub{}
	service := NewService(store, gateway, providers(now, "100.00", "100.00"))
	service.now = func() time.Time { return now }
	if err := service.Cycle(context.Background()); !errors.Is(err, ErrKillSwitch) {
		t.Fatalf("kill switch: %v", err)
	}
	if gateway.canceled != 0 {
		t.Fatal("kill switch should have no orders to cancel")
	}
	store.control.KillSwitch = false
	if err := service.Cycle(context.Background()); !errors.Is(err, ErrRiskLimit) {
		t.Fatalf("loss limit: %v", err)
	}
	if len(gateway.placed) != 0 {
		t.Fatal("risk halt placed orders")
	}
}

func TestKillSwitchCancelsOutstandingQuotes(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	store := &storeStub{control: Control{Enabled: true, KillSwitch: true, UserID: "user", AccountID: "account"}, configs: []Config{baseConfig()}}
	gateway := &gatewayStub{orders: []trading.Order{{ID: "open-quote"}}}
	service := NewService(store, gateway, nil)
	service.now = func() time.Time { return now }
	if err := service.Cycle(context.Background()); !errors.Is(err, ErrKillSwitch) {
		t.Fatalf("kill switch: %v", err)
	}
	if gateway.canceled != 3 {
		t.Fatalf("expected cancellation through each active-status query, got %d", gateway.canceled)
	}
}

func TestReferenceRequiresIndependentProviderNames(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	feed := providerStub{ticker: marketdata.SpotTicker{Symbol: "BTCUSDT", Bid: "100.00", Ask: "100.00", ObservedAt: now}}
	service := NewService(&storeStub{}, &gatewayStub{}, []NamedProvider{{Name: "venue", Provider: feed}, {Name: "VENUE", Provider: feed}})
	if _, err := service.Reference(context.Background(), baseConfig(), now); !errors.Is(err, ErrReferenceStale) {
		t.Fatalf("duplicate venue counted independently: %v", err)
	}
}
