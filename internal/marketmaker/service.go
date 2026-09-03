package marketmaker

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/limiance/backend/internal/marketdata"
	"github.com/limiance/backend/internal/trading"
)

type Service struct {
	store     Store
	gateway   Gateway
	providers []NamedProvider
	now       func() time.Time
}

func NewService(store Store, gateway Gateway, providers []NamedProvider) *Service {
	return &Service{store: store, gateway: gateway, providers: providers, now: time.Now}
}

func (service *Service) Cycle(ctx context.Context) error {
	control, err := service.store.LoadControl(ctx)
	if err != nil {
		return err
	}
	if !control.Enabled {
		return ErrDisabled
	}
	if control.KillSwitch {
		return service.stopExisting(ctx, control)
	}
	if control.UserID == "" || control.AccountID == "" {
		return fmt.Errorf("%w: internal account is not configured", ErrRiskLimit)
	}
	configs, err := service.store.ListConfigs(ctx)
	if err != nil {
		return err
	}
	var cycleErr error
	for _, config := range configs {
		if !config.Enabled {
			continue
		}
		if err = service.quotePair(ctx, control, config); err != nil {
			cycleErr = errors.Join(cycleErr, fmt.Errorf("%s: %w", config.Pair, err))
		}
	}
	return cycleErr
}

func (service *Service) stopExisting(ctx context.Context, control Control) error {
	if control.UserID == "" || control.AccountID == "" {
		return ErrKillSwitch
	}
	configs, err := service.store.ListConfigs(ctx)
	if err != nil {
		return errors.Join(ErrKillSwitch, err)
	}
	var stopErr error
	now := service.now().UTC()
	for _, config := range configs {
		if config.Enabled {
			stopErr = errors.Join(stopErr, service.cancelExisting(ctx, control, config.Pair, now))
		}
	}
	return errors.Join(ErrKillSwitch, stopErr)
}

func (service *Service) quotePair(ctx context.Context, control Control, config Config) error {
	now := service.now().UTC()
	if !control.DryRun && config.PairStatus != "active" {
		err := fmt.Errorf("%w: pair is not active", ErrRiskLimit)
		_ = service.store.RecordDecision(ctx, Decision{Pair: config.Pair, Status: "halted", Reason: err.Error(), At: now})
		return err
	}
	quote, err := service.Reference(ctx, config, now)
	if err != nil {
		_ = service.store.RecordDecision(ctx, Decision{Pair: config.Pair, Status: "halted", Reason: err.Error(), At: now})
		return err
	}
	if !control.DryRun {
		risk, riskErr := service.store.Risk(ctx, control.AccountID, config.Pair)
		if riskErr != nil {
			return riskErr
		}
		if err = checkRisk(config, risk, quote); err != nil {
			_ = service.store.RecordDecision(ctx, Decision{Pair: config.Pair, Status: "halted", Reason: err.Error(), ReferencePriceAtomic: quote.ReferencePriceAtomic, At: now})
			return err
		}
	}
	commands := []struct{ key, side, price string }{
		{fmt.Sprintf("market-maker:%s:%d:buy", config.Pair, now.Truncate(5*time.Second).Unix()), "BUY", quote.BidPriceAtomic},
		{fmt.Sprintf("market-maker:%s:%d:sell", config.Pair, now.Truncate(5*time.Second).Unix()), "SELL", quote.AskPriceAtomic},
	}
	claimedAll := true
	for _, command := range commands {
		claimed, claimErr := service.store.ClaimCommand(ctx, command.key, config.Pair, command.side, command.price, quote.QuantityAtomic, control.DryRun)
		if claimErr != nil {
			return claimErr
		}
		claimedAll = claimedAll && claimed
	}
	if !claimedAll {
		return service.store.RecordDecision(ctx, Decision{Pair: config.Pair, Status: "duplicate_cycle", ReferencePriceAtomic: quote.ReferencePriceAtomic, At: now})
	}
	if !control.DryRun {
		if err = service.cancelExisting(ctx, control, config.Pair, now); err != nil {
			return err
		}
	}
	for _, command := range commands {
		if control.DryRun {
			continue
		}
		order, placeErr := service.gateway.PlaceOrder(ctx, trading.PlaceOrderInput{UserID: control.UserID, AccountID: control.AccountID, AccountKind: "uta", IdempotencyKey: command.key, Pair: config.Pair, Side: command.side, Type: "LIMIT", Price: command.price, Quantity: quote.QuantityAtomic, TimeInForce: "GTC", PostOnly: true})
		if placeErr != nil {
			return placeErr
		}
		if attachErr := service.store.AttachOrder(ctx, command.key, order.ID); attachErr != nil {
			return attachErr
		}
	}
	return service.store.RecordDecision(ctx, Decision{Pair: config.Pair, Status: map[bool]string{true: "dry_run", false: "quoting"}[control.DryRun], ReferencePriceAtomic: quote.ReferencePriceAtomic, At: now})
}

func (service *Service) cancelExisting(ctx context.Context, control Control, pair string, now time.Time) error {
	for _, status := range []string{"PENDING", "OPEN", "PARTIALLY_FILLED"} {
		orders, err := service.gateway.ListOrders(ctx, control.UserID, control.AccountID, trading.OrderFilter{Pair: pair, Status: status, Limit: 200})
		if err != nil {
			return err
		}
		for _, order := range orders {
			_, err = service.gateway.CancelOrder(ctx, trading.CancelOrderInput{UserID: control.UserID, AccountID: control.AccountID, OrderID: order.ID, IdempotencyKey: fmt.Sprintf("market-maker-cancel:%s:%d", order.ID, now.Unix()/5)})
			if err != nil && !errors.Is(err, trading.ErrOrderNotCancelable) && !errors.Is(err, trading.ErrOrderAlreadyFilled) {
				return err
			}
		}
	}
	return nil
}

func (service *Service) Reference(ctx context.Context, config Config, now time.Time) (Quote, error) {
	type result struct {
		name   string
		ticker marketdata.SpotTicker
		err    error
	}
	results := make(chan result, len(service.providers))
	var wait sync.WaitGroup
	for _, source := range service.providers {
		if source.Provider == nil {
			continue
		}
		wait.Add(1)
		go func(name string, provider marketdata.SpotProvider) {
			defer wait.Done()
			ticker, err := provider.SpotTicker(ctx, config.Pair)
			results <- result{name, ticker, err}
		}(source.Name, source.Provider)
	}
	wait.Wait()
	close(results)
	values := make([]*big.Int, 0, len(service.providers))
	seenProviders := make(map[string]struct{}, len(service.providers))
	latest := time.Time{}
	for result := range results {
		name := strings.ToLower(strings.TrimSpace(result.name))
		if name == "" {
			continue
		}
		if _, duplicate := seenProviders[name]; duplicate {
			continue
		}
		if result.err != nil || result.ticker.ObservedAt.IsZero() || now.Sub(result.ticker.ObservedAt) > config.StaleAfter || result.ticker.ObservedAt.After(now.Add(time.Second)) {
			continue
		}
		bid, bidErr := decimalToAtomic(result.ticker.Bid, config.PriceScale)
		ask, askErr := decimalToAtomic(result.ticker.Ask, config.PriceScale)
		if bidErr != nil || askErr != nil || bid.Sign() <= 0 || ask.Cmp(bid) < 0 {
			continue
		}
		seenProviders[name] = struct{}{}
		values = append(values, new(big.Int).Quo(new(big.Int).Add(bid, ask), big.NewInt(2)))
		if result.ticker.ObservedAt.After(latest) {
			latest = result.ticker.ObservedAt
		}
	}
	if len(values) < 2 {
		return Quote{}, ErrReferenceStale
	}
	sort.Slice(values, func(i, j int) bool { return values[i].Cmp(values[j]) < 0 })
	median := new(big.Int)
	if len(values)%2 == 1 {
		median.Set(values[len(values)/2])
	} else {
		median.Quo(new(big.Int).Add(values[len(values)/2-1], values[len(values)/2]), big.NewInt(2))
	}
	divergence := new(big.Int).Quo(new(big.Int).Mul(new(big.Int).Sub(values[len(values)-1], values[0]), big.NewInt(10000)), median)
	if divergence.Cmp(big.NewInt(config.MaxDivergenceBPS)) > 0 {
		return Quote{}, ErrReferenceDiverged
	}
	half := config.SpreadBPS / 2
	bid := new(big.Int).Quo(new(big.Int).Mul(new(big.Int).Set(median), big.NewInt(10000-half)), big.NewInt(10000))
	askNumerator := new(big.Int).Mul(new(big.Int).Set(median), big.NewInt(10000+half))
	ask := ceilQuo(askNumerator, big.NewInt(10000))
	tick, ok := new(big.Int).SetString(config.PriceTickAtomic, 10)
	if !ok || tick.Sign() <= 0 {
		return Quote{}, ErrRiskLimit
	}
	bid.Quo(bid, tick).Mul(bid, tick)
	ask = ceilQuo(ask, tick)
	ask.Mul(ask, tick)
	if bid.Sign() <= 0 || ask.Cmp(bid) <= 0 {
		return Quote{}, ErrRiskLimit
	}
	return Quote{Pair: config.Pair, ReferencePriceAtomic: median.String(), BidPriceAtomic: bid.String(), AskPriceAtomic: ask.String(), QuantityAtomic: config.QuantityAtomic, ObservedAt: latest}, nil
}

func checkRisk(config Config, risk RiskSnapshot, quote Quote) error {
	inventory, inventoryOK := new(big.Int).SetString(risk.BaseInventoryAtomic, 10)
	quantity, quantityOK := new(big.Int).SetString(config.QuantityAtomic, 10)
	maxInventory, maxInventoryOK := new(big.Int).SetString(config.MaxBaseInventoryAtomic, 10)
	pnl, pnlOK := new(big.Int).SetString(risk.DailyPnLQuoteAtomic, 10)
	maxLoss, maxLossOK := new(big.Int).SetString(config.MaxDailyLossAtomic, 10)
	price, priceOK := new(big.Int).SetString(quote.AskPriceAtomic, 10)
	maxNotional, maxNotionalOK := new(big.Int).SetString(config.MaxQuoteNotionalAtomic, 10)
	if !inventoryOK || !quantityOK || !maxInventoryOK || !pnlOK || !maxLossOK || !priceOK || !maxNotionalOK {
		return ErrRiskLimit
	}
	if new(big.Int).Add(new(big.Int).Set(inventory), quantity).Cmp(maxInventory) > 0 {
		return ErrRiskLimit
	}
	if inventory.Cmp(quantity) < 0 {
		return ErrRiskLimit
	}
	if pnl.Sign() < 0 && new(big.Int).Abs(new(big.Int).Set(pnl)).Cmp(maxLoss) >= 0 {
		return ErrRiskLimit
	}
	numerator := new(big.Int).Mul(price, quantity)
	numerator.Mul(numerator, pow10(config.QuoteScale))
	divisor := pow10(config.PriceScale + config.QuantityScale)
	if divisor.Sign() <= 0 {
		return ErrRiskLimit
	}
	notional := ceilQuo(numerator, divisor)
	if notional.Cmp(maxNotional) > 0 {
		return ErrRiskLimit
	}
	return nil
}

func pow10(scale int) *big.Int {
	if scale < 0 || scale > 36 {
		return new(big.Int)
	}
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil)
}

func decimalToAtomic(value string, scale int) (*big.Int, error) {
	if scale < 0 || scale > 18 {
		return nil, ErrReferenceStale
	}
	parts := strings.Split(value, ".")
	if len(parts) > 2 || len(parts[0]) == 0 || (len(parts) == 2 && len(parts[1]) > scale) {
		return nil, ErrReferenceStale
	}
	for _, part := range parts {
		for _, char := range part {
			if char < '0' || char > '9' {
				return nil, ErrReferenceStale
			}
		}
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
	}
	fraction += strings.Repeat("0", scale-len(fraction))
	number, ok := new(big.Int).SetString(parts[0]+fraction, 10)
	if !ok {
		return nil, ErrReferenceStale
	}
	return number, nil
}

func ceilQuo(numerator, denominator *big.Int) *big.Int {
	return new(big.Int).Quo(new(big.Int).Add(numerator, new(big.Int).Sub(denominator, big.NewInt(1))), denominator)
}
