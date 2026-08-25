// Package conversions creates Limiance quotes from read-only market data. It
// does not execute an external exchange trade; confirmation will settle from
// Limiance treasury through the immutable internal ledger.
package conversions

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"time"

	"github.com/limiance/backend/internal/datamanager"
	"github.com/limiance/backend/internal/marketdata"
)

var ErrInvalidInput = errors.New("invalid conversion input")
var ErrQuoteUnavailable = errors.New("conversion quote unavailable")

type QuoteInput struct {
	SourceAccountID string `json:"source_account_id"`
	FromAssetSymbol string `json:"from_asset_symbol"`
	FromNetwork     string `json:"from_network"`
	ToAssetSymbol   string `json:"to_asset_symbol"`
	ToNetwork       string `json:"to_network"`
	AmountAtomic    int64  `json:"amount_atomic"`
}

type ConfirmInput struct {
	QuoteID        string `json:"quote_id"`
	IdempotencyKey string
}

type Service struct {
	data   *datamanager.Manager
	market marketdata.SpotProvider
	ttl    time.Duration
}

func NewService(data *datamanager.Manager, market marketdata.SpotProvider) *Service {
	return &Service{data: data, market: market, ttl: 15 * time.Second}
}

func buildMarketSymbol(fromSymbol, toSymbol string) string {
	fromSymbol = strings.ToUpper(strings.TrimSpace(fromSymbol))
	toSymbol = strings.ToUpper(strings.TrimSpace(toSymbol))
	if fromSymbol == "" || toSymbol == "" {
		return ""
	}
	if fromSymbol == "USDC" || fromSymbol == "USDT" || fromSymbol == "USD" {
		return toSymbol + fromSymbol
	}
	if toSymbol == "USDC" || toSymbol == "USDT" || toSymbol == "USD" {
		return fromSymbol + toSymbol
	}
	return fromSymbol + toSymbol
}

func (s *Service) Quote(ctx context.Context, userID string, input QuoteInput) (datamanager.ConversionQuote, error) {
	input.SourceAccountID = strings.TrimSpace(input.SourceAccountID)
	input.FromAssetSymbol = strings.ToUpper(strings.TrimSpace(input.FromAssetSymbol))
	input.ToAssetSymbol = strings.ToUpper(strings.TrimSpace(input.ToAssetSymbol))
	input.FromNetwork = strings.TrimSpace(input.FromNetwork)
	input.ToNetwork = strings.TrimSpace(input.ToNetwork)
	if userID == "" || input.SourceAccountID == "" || input.FromAssetSymbol == "" || input.ToAssetSymbol == "" || input.FromNetwork == "" || input.ToNetwork == "" || input.AmountAtomic <= 0 || input.FromAssetSymbol == input.ToAssetSymbol && input.FromNetwork == input.ToNetwork || s.market == nil {
		return datamanager.ConversionQuote{}, ErrInvalidInput
	}
	pair, err := s.data.ConversionPair(ctx, input.FromAssetSymbol, input.FromNetwork, input.ToAssetSymbol, input.ToNetwork)
	if err != nil {
		return datamanager.ConversionQuote{}, ErrQuoteUnavailable
	}
	ticker, err := s.market.SpotTicker(ctx, pair.MarketSymbol)
	if err != nil {
		return datamanager.ConversionQuote{}, ErrQuoteUnavailable
	}
	// The market symbol is intentionally operator-configured. A pair whose first
	// asset is the base uses bid (customer sells base); reverse uses ask.
	sellBase := strings.HasPrefix(pair.MarketSymbol, pair.FromSymbol)
	price := ticker.Ask
	if sellBase {
		price = ticker.Bid
	}
	out, fee, err := quotedAmounts(input.AmountAtomic, price, pair.FromDecimals, pair.ToDecimals, sellBase, pair.SpreadBPS, pair.FeeBPS)
	if err != nil {
		return datamanager.ConversionQuote{}, ErrQuoteUnavailable
	}
	return s.data.CreateConversionQuote(ctx, datamanager.ConversionQuoteInput{UserID: userID, SourceAccountID: input.SourceAccountID, FromSymbol: pair.FromSymbol, FromNetwork: pair.FromNetwork, ToSymbol: pair.ToSymbol, ToNetwork: pair.ToNetwork, InputAmountAtomic: input.AmountAtomic, OutputAmountAtomic: out, FeeAmountAtomic: fee, Price: price, Provider: "bybit_public_spot", ExpiresAt: time.Now().UTC().Add(s.ttl)})
}

func (s *Service) Confirm(ctx context.Context, userID string, input ConfirmInput) (datamanager.ConversionResult, error) {
	input.QuoteID = strings.TrimSpace(input.QuoteID)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if userID == "" || input.QuoteID == "" || len(input.IdempotencyKey) < 16 || len(input.IdempotencyKey) > 255 {
		return datamanager.ConversionResult{}, ErrInvalidInput
	}
	return s.data.ConfirmConversion(ctx, userID, input.QuoteID, input.IdempotencyKey)
}

func (s *Service) History(ctx context.Context, userID string, limit int) ([]datamanager.ConversionHistoryItem, error) {
	if userID == "" {
		return nil, ErrInvalidInput
	}
	return s.data.ConversionHistory(ctx, userID, limit)
}

// quotedAmounts uses exact rationals and floors in the customer's favour only
// after applying operator-approved spread and fee. No floating point money is
// used anywhere in this calculation.
func quotedAmounts(input int64, price string, fromDecimals, toDecimals int16, sellBase bool, spreadBPS, feeBPS int) (int64, int64, error) {
	if input <= 0 || fromDecimals < 0 || toDecimals < 0 || spreadBPS < 0 || feeBPS < 0 {
		return 0, 0, ErrInvalidInput
	}
	p, ok := new(big.Rat).SetString(price)
	if !ok || p.Sign() <= 0 {
		return 0, 0, ErrInvalidInput
	}
	ten := big.NewInt(10)
	fd := new(big.Int).Exp(ten, big.NewInt(int64(fromDecimals)), nil)
	td := new(big.Int).Exp(ten, big.NewInt(int64(toDecimals)), nil)
	r := new(big.Rat).SetInt64(input)
	r.Quo(r, new(big.Rat).SetInt(fd))
	if sellBase {
		r.Mul(r, p)
		r.Mul(r, new(big.Rat).SetFrac(big.NewInt(int64(10000-spreadBPS)), big.NewInt(10000)))
	} else {
		r.Quo(r, p)
		r.Quo(r, new(big.Rat).SetFrac(big.NewInt(int64(10000+spreadBPS)), big.NewInt(10000)))
	}
	r.Mul(r, new(big.Rat).SetInt(td))
	gross := new(big.Int).Quo(r.Num(), r.Denom())
	fee := new(big.Int).Quo(new(big.Int).Mul(gross, big.NewInt(int64(feeBPS))), big.NewInt(10000))
	net := new(big.Int).Sub(gross, fee)
	if !net.IsInt64() || !fee.IsInt64() || net.Sign() <= 0 {
		return 0, 0, ErrInvalidInput
	}
	return net.Int64(), fee.Int64(), nil
}
