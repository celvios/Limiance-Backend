package marketdata

import (
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"
)

var (
	ErrInvalidMarketEvent     = errors.New("invalid market event")
	ErrMarketSequenceGap      = errors.New("market data sequence gap")
	ErrMarketSequenceConflict = errors.New("market data sequence conflict")
)

type Trade struct {
	Pair           string    `json:"pair"`
	SequenceID     uint64    `json:"sequence_id"`
	Timestamp      time.Time `json:"timestamp"`
	PriceAtomic    string    `json:"price"`
	QuantityAtomic string    `json:"quantity"`
	QuoteAtomic    string    `json:"quote_volume"`
	Side           string    `json:"side"`
	PayloadHash    [32]byte  `json:"-"`
}

type Ticker struct {
	Pair                string     `json:"pair"`
	SequenceID          uint64     `json:"sequence_id"`
	LastPrice           string     `json:"last_price"`
	High24H             string     `json:"high_24h"`
	Low24H              string     `json:"low_24h"`
	Volume24H           string     `json:"volume_24h"`
	QuoteVolume24H      string     `json:"quote_volume_24h"`
	Change24H           string     `json:"change_24h"`
	ChangeBPS24H        string     `json:"change_bps_24h"`
	ReferencePrice      string     `json:"reference_price"`
	ReferenceObservedAt *time.Time `json:"reference_observed_at,omitempty"`
	ReferenceStatus     string     `json:"reference_status"`
}

func tickerChanges(lastPrice, openingPrice string) (string, string) {
	last, lastOK := new(big.Int).SetString(lastPrice, 10)
	opening, openingOK := new(big.Int).SetString(openingPrice, 10)
	if !lastOK || !openingOK || opening.Sign() <= 0 {
		return "0", "0"
	}
	change := new(big.Int).Sub(last, opening)
	basisPoints := new(big.Int).Quo(new(big.Int).Mul(new(big.Int).Set(change), big.NewInt(10000)), opening)
	return change.String(), basisPoints.String()
}

type Candle struct {
	Pair        string    `json:"pair"`
	Interval    string    `json:"interval"`
	OpenTime    time.Time `json:"open_time"`
	CloseTime   time.Time `json:"close_time"`
	Open        string    `json:"open"`
	High        string    `json:"high"`
	Low         string    `json:"low"`
	Close       string    `json:"close"`
	Volume      string    `json:"volume"`
	QuoteVolume string    `json:"quote_volume"`
	TradeCount  uint64    `json:"trade_count"`
	SequenceID  uint64    `json:"sequence_id"`
}

var intervalDurations = map[string]time.Duration{
	"1m":  time.Minute,
	"5m":  5 * time.Minute,
	"15m": 15 * time.Minute,
	"1h":  time.Hour,
	"4h":  4 * time.Hour,
	"1d":  24 * time.Hour,
	"1w":  7 * 24 * time.Hour,
}

func ValidatePair(pair string) error {
	pair = strings.TrimSpace(pair)
	if pair == "" || len(pair) > 20 || pair != strings.ToUpper(pair) {
		return fmt.Errorf("%w: invalid pair", ErrInvalidMarketEvent)
	}
	for _, character := range pair {
		if (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
			return fmt.Errorf("%w: invalid pair", ErrInvalidMarketEvent)
		}
	}
	return nil
}

func IntervalDuration(interval string) (time.Duration, bool) {
	if interval == "1M" {
		return 0, true
	}
	duration, ok := intervalDurations[interval]
	return duration, ok
}

func BucketStart(at time.Time, interval string) (time.Time, error) {
	at = at.UTC()
	if interval == "1M" {
		return time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, time.UTC), nil
	}
	duration, ok := intervalDurations[interval]
	if !ok {
		return time.Time{}, fmt.Errorf("%w: unsupported candle interval", ErrInvalidMarketEvent)
	}
	if interval == "1w" {
		dayStart := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC)
		mondayOffset := (int(dayStart.Weekday()) + 6) % 7
		return dayStart.AddDate(0, 0, -mondayOffset), nil
	}
	return at.Truncate(duration), nil
}

func BucketEnd(start time.Time, interval string) (time.Time, error) {
	if interval == "1M" {
		return start.AddDate(0, 1, 0), nil
	}
	duration, ok := intervalDurations[interval]
	if !ok {
		return time.Time{}, fmt.Errorf("%w: unsupported candle interval", ErrInvalidMarketEvent)
	}
	return start.Add(duration), nil
}

func NewMinuteCandle(trade Trade) (Candle, error) {
	if err := validateTrade(trade); err != nil {
		return Candle{}, err
	}
	start, _ := BucketStart(trade.Timestamp, "1m")
	return Candle{Pair: trade.Pair, Interval: "1m", OpenTime: start, CloseTime: start.Add(time.Minute), Open: trade.PriceAtomic, High: trade.PriceAtomic, Low: trade.PriceAtomic, Close: trade.PriceAtomic, Volume: trade.QuantityAtomic, QuoteVolume: trade.QuoteAtomic, TradeCount: 1, SequenceID: trade.SequenceID}, nil
}

func (candle Candle) Add(trade Trade) (Candle, error) {
	if err := validateTrade(trade); err != nil {
		return Candle{}, err
	}
	if candle.Pair != trade.Pair || !trade.Timestamp.Before(candle.CloseTime) || trade.Timestamp.Before(candle.OpenTime) || trade.SequenceID <= candle.SequenceID {
		return Candle{}, fmt.Errorf("%w: trade does not continue candle", ErrInvalidMarketEvent)
	}
	if compareAtomic(trade.PriceAtomic, candle.High) > 0 {
		candle.High = trade.PriceAtomic
	}
	if compareAtomic(trade.PriceAtomic, candle.Low) < 0 {
		candle.Low = trade.PriceAtomic
	}
	candle.Close = trade.PriceAtomic
	candle.Volume = addAtomic(candle.Volume, trade.QuantityAtomic)
	candle.QuoteVolume = addAtomic(candle.QuoteVolume, trade.QuoteAtomic)
	candle.TradeCount++
	candle.SequenceID = trade.SequenceID
	return candle, nil
}

func AggregateCandles(minutes []Candle, interval string) ([]Candle, error) {
	if _, ok := IntervalDuration(interval); !ok {
		return nil, fmt.Errorf("%w: unsupported candle interval", ErrInvalidMarketEvent)
	}
	if interval == "1m" {
		return append([]Candle(nil), minutes...), nil
	}
	sort.Slice(minutes, func(i, j int) bool { return minutes[i].OpenTime.Before(minutes[j].OpenTime) })
	result := make([]Candle, 0)
	for _, minute := range minutes {
		start, err := BucketStart(minute.OpenTime, interval)
		if err != nil {
			return nil, err
		}
		end, _ := BucketEnd(start, interval)
		if len(result) == 0 || !result[len(result)-1].OpenTime.Equal(start) {
			minute.Interval, minute.OpenTime, minute.CloseTime = interval, start, end
			result = append(result, minute)
			continue
		}
		current := &result[len(result)-1]
		if compareAtomic(minute.High, current.High) > 0 {
			current.High = minute.High
		}
		if compareAtomic(minute.Low, current.Low) < 0 {
			current.Low = minute.Low
		}
		current.Close = minute.Close
		current.Volume = addAtomic(current.Volume, minute.Volume)
		current.QuoteVolume = addAtomic(current.QuoteVolume, minute.QuoteVolume)
		current.TradeCount += minute.TradeCount
		if minute.SequenceID > current.SequenceID {
			current.SequenceID = minute.SequenceID
		}
	}
	return result, nil
}

func validateTrade(trade Trade) error {
	if err := ValidatePair(trade.Pair); err != nil || trade.SequenceID == 0 || trade.Timestamp.IsZero() || !positiveAtomic(trade.PriceAtomic) || !positiveAtomic(trade.QuantityAtomic) || !positiveAtomic(trade.QuoteAtomic) {
		return fmt.Errorf("%w: invalid trade", ErrInvalidMarketEvent)
	}
	if trade.Side != "BUY" && trade.Side != "SELL" {
		return fmt.Errorf("%w: invalid trade side", ErrInvalidMarketEvent)
	}
	return nil
}

func positiveAtomic(value string) bool {
	number, ok := new(big.Int).SetString(value, 10)
	return ok && number.Sign() > 0
}
func compareAtomic(left, right string) int {
	a, _ := new(big.Int).SetString(left, 10)
	b, _ := new(big.Int).SetString(right, 10)
	return a.Cmp(b)
}
func addAtomic(left, right string) string {
	a, _ := new(big.Int).SetString(left, 10)
	b, _ := new(big.Int).SetString(right, 10)
	return a.Add(a, b).String()
}
