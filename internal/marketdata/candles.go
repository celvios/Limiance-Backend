package marketdata

import "time"

// FillMinuteGaps makes the canonical read model continuous between its first
// and last observations. Empty minutes carry the previous close and zero
// volume; no synthetic trade is introduced.
func FillMinuteGaps(candles []Candle) []Candle {
	if len(candles) < 2 {
		return append([]Candle(nil), candles...)
	}
	filled := make([]Candle, 0, len(candles))
	filled = append(filled, candles[0])
	for _, next := range candles[1:] {
		previous := filled[len(filled)-1]
		for open := previous.OpenTime.Add(time.Minute); open.Before(next.OpenTime); open = open.Add(time.Minute) {
			filled = append(filled, Candle{Pair: previous.Pair, Interval: "1m", OpenTime: open, CloseTime: open.Add(time.Minute), Open: previous.Close, High: previous.Close, Low: previous.Close, Close: previous.Close, Volume: "0", QuoteVolume: "0", SequenceID: previous.SequenceID})
			previous = filled[len(filled)-1]
		}
		filled = append(filled, next)
	}
	return filled
}
