package marketdata

import (
	"testing"
	"time"
)

func TestTickerChangesUseSignedIntegerBasisPoints(t *testing.T) {
	tests := []struct {
		name, last, opening, change, basisPoints string
	}{
		{name: "gain", last: "110", opening: "90", change: "20", basisPoints: "2222"},
		{name: "loss", last: "90", opening: "110", change: "-20", basisPoints: "-1818"},
		{name: "no trades", last: "0", opening: "0", change: "0", basisPoints: "0"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			change, basisPoints := tickerChanges(test.last, test.opening)
			if change != test.change || basisPoints != test.basisPoints {
				t.Fatalf("changes=(%s,%s), want (%s,%s)", change, basisPoints, test.change, test.basisPoints)
			}
		})
	}
}

func TestMinuteCandleUsesIntegerAtomicValues(t *testing.T) {
	start := time.Date(2026, 8, 29, 10, 0, 5, 0, time.UTC)
	candle, err := NewMinuteCandle(Trade{Pair: "BTCUSDT", SequenceID: 1, Timestamp: start, PriceAtomic: "50000", QuantityAtomic: "1", QuoteAtomic: "50000", Side: "BUY"})
	if err != nil {
		t.Fatal(err)
	}
	for sequence, price := range []string{"51000", "49000", "50500"} {
		candle, err = candle.Add(Trade{Pair: "BTCUSDT", SequenceID: uint64(sequence + 2), Timestamp: start.Add(time.Duration(sequence+1) * time.Second), PriceAtomic: price, QuantityAtomic: "2", QuoteAtomic: "100000", Side: "SELL"})
		if err != nil {
			t.Fatal(err)
		}
	}
	if candle.Open != "50000" || candle.High != "51000" || candle.Low != "49000" || candle.Close != "50500" {
		t.Fatalf("unexpected OHLC: %#v", candle)
	}
	if candle.Volume != "7" || candle.QuoteVolume != "350000" || candle.TradeCount != 4 {
		t.Fatalf("unexpected aggregation: %#v", candle)
	}
	if !candle.OpenTime.Equal(time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("unexpected bucket: %s", candle.OpenTime)
	}
}

func TestAggregateCandlesDerivesLargerIntervals(t *testing.T) {
	base := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	minutes := []Candle{
		{Pair: "BTCUSDT", Interval: "1m", OpenTime: base, CloseTime: base.Add(time.Minute), Open: "100", High: "110", Low: "90", Close: "105", Volume: "2", QuoteVolume: "200", TradeCount: 2, SequenceID: 2},
		{Pair: "BTCUSDT", Interval: "1m", OpenTime: base.Add(time.Minute), CloseTime: base.Add(2 * time.Minute), Open: "105", High: "120", Low: "95", Close: "115", Volume: "3", QuoteVolume: "330", TradeCount: 3, SequenceID: 5},
	}
	candles, err := AggregateCandles(minutes, "1h")
	if err != nil {
		t.Fatal(err)
	}
	if len(candles) != 1 || candles[0].Open != "100" || candles[0].High != "120" || candles[0].Low != "90" || candles[0].Close != "115" || candles[0].Volume != "5" || candles[0].TradeCount != 5 {
		t.Fatalf("unexpected aggregate: %#v", candles)
	}
}

func TestAllDocumentedIntervalsAreAccepted(t *testing.T) {
	for _, interval := range []string{"1m", "5m", "15m", "1h", "4h", "1d", "1w", "1M"} {
		if _, ok := IntervalDuration(interval); !ok {
			t.Fatalf("interval %s rejected", interval)
		}
	}
}

func TestFillMinuteGapsCarriesCloseWithoutSyntheticVolume(t *testing.T) {
	start := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	candles := FillMinuteGaps([]Candle{
		{Pair: "BTCUSDT", Interval: "1m", OpenTime: start, CloseTime: start.Add(time.Minute), Open: "100", High: "100", Low: "100", Close: "100", Volume: "2", QuoteVolume: "200", TradeCount: 1, SequenceID: 1},
		{Pair: "BTCUSDT", Interval: "1m", OpenTime: start.Add(3 * time.Minute), CloseTime: start.Add(4 * time.Minute), Open: "110", High: "110", Low: "110", Close: "110", Volume: "1", QuoteVolume: "110", TradeCount: 1, SequenceID: 2},
	})
	if len(candles) != 4 {
		t.Fatalf("got %d candles, want 4", len(candles))
	}
	for _, candle := range candles[1:3] {
		if candle.Open != "100" || candle.Close != "100" || candle.Volume != "0" || candle.TradeCount != 0 {
			t.Fatalf("unexpected gap candle: %+v", candle)
		}
	}
}
