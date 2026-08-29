package marketdata

import (
	"fmt"

	flatbuffers "github.com/google/flatbuffers/go"
	marketdatawire "github.com/limiance/backend/internal/marketdata/wire/marketdatawire"
)

const (
	MaxSnapshotDepth       = 1000
	maxSnapshotMessageSize = 8 << 20
)

type OrderBookLevel struct {
	Price      uint64
	Quantity   uint64
	OrderCount uint32
}

type OrderBookSnapshot struct {
	SequenceID  uint64
	TimestampNS uint64
	Pair        string
	Bids        []OrderBookLevel
	Asks        []OrderBookLevel
}

func (snapshot OrderBookSnapshot) Validate() error {
	if snapshot.SequenceID == 0 {
		return fmt.Errorf("order book sequence_id must be positive")
	}
	if snapshot.Pair == "" || len(snapshot.Pair) > 20 {
		return fmt.Errorf("order book pair is invalid")
	}
	if len(snapshot.Bids) > MaxSnapshotDepth || len(snapshot.Asks) > MaxSnapshotDepth {
		return fmt.Errorf("order book exceeds maximum depth %d", MaxSnapshotDepth)
	}
	for _, side := range [][]OrderBookLevel{snapshot.Bids, snapshot.Asks} {
		for _, level := range side {
			if level.Price == 0 || level.Quantity == 0 || level.OrderCount == 0 {
				return fmt.Errorf("order book levels require positive price, quantity, and order count")
			}
		}
	}
	return nil
}

func EncodeOrderBookSnapshot(snapshot OrderBookSnapshot) ([]byte, error) {
	if err := snapshot.Validate(); err != nil {
		return nil, err
	}
	builder := flatbuffers.NewBuilder(64 + (len(snapshot.Bids)+len(snapshot.Asks))*32)
	pair := builder.CreateString(snapshot.Pair)
	bids := buildLevels(builder, snapshot.Bids, marketdatawire.OrderBookSnapshotStartBidsVector)
	asks := buildLevels(builder, snapshot.Asks, marketdatawire.OrderBookSnapshotStartAsksVector)
	marketdatawire.OrderBookSnapshotStart(builder)
	marketdatawire.OrderBookSnapshotAddSequenceId(builder, snapshot.SequenceID)
	marketdatawire.OrderBookSnapshotAddTimestampNs(builder, snapshot.TimestampNS)
	marketdatawire.OrderBookSnapshotAddPair(builder, pair)
	marketdatawire.OrderBookSnapshotAddBids(builder, bids)
	marketdatawire.OrderBookSnapshotAddAsks(builder, asks)
	offset := marketdatawire.OrderBookSnapshotEnd(builder)
	marketdatawire.FinishOrderBookSnapshotBuffer(builder, offset)
	return append([]byte(nil), builder.FinishedBytes()...), nil
}

func DecodeOrderBookSnapshot(data []byte) (snapshot OrderBookSnapshot, err error) {
	if len(data) < flatbuffers.SizeUOffsetT {
		return snapshot, fmt.Errorf("order book message is too short")
	}
	if len(data) > maxSnapshotMessageSize {
		return snapshot, fmt.Errorf("order book message exceeds %d bytes", maxSnapshotMessageSize)
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("malformed order book message: %v", recovered)
		}
	}()
	message := marketdatawire.GetRootAsOrderBookSnapshot(data, 0)
	snapshot = OrderBookSnapshot{
		SequenceID:  message.SequenceId(),
		TimestampNS: message.TimestampNs(),
		Pair:        string(message.Pair()),
		Bids:        readLevels(message.BidsLength(), message.Bids),
		Asks:        readLevels(message.AsksLength(), message.Asks),
	}
	if err := snapshot.Validate(); err != nil {
		return OrderBookSnapshot{}, err
	}
	return snapshot, nil
}

func buildLevels(builder *flatbuffers.Builder, levels []OrderBookLevel, startVector func(*flatbuffers.Builder, int) flatbuffers.UOffsetT) flatbuffers.UOffsetT {
	offsets := make([]flatbuffers.UOffsetT, len(levels))
	for i, level := range levels {
		marketdatawire.OrderBookLevelStart(builder)
		marketdatawire.OrderBookLevelAddPrice(builder, level.Price)
		marketdatawire.OrderBookLevelAddQuantity(builder, level.Quantity)
		marketdatawire.OrderBookLevelAddOrderCount(builder, level.OrderCount)
		offsets[i] = marketdatawire.OrderBookLevelEnd(builder)
	}
	startVector(builder, len(offsets))
	for i := len(offsets) - 1; i >= 0; i-- {
		builder.PrependUOffsetT(offsets[i])
	}
	return builder.EndVector(len(offsets))
}

func readLevels(length int, read func(*marketdatawire.OrderBookLevel, int) bool) []OrderBookLevel {
	levels := make([]OrderBookLevel, 0, length)
	for i := 0; i < length; i++ {
		var message marketdatawire.OrderBookLevel
		if !read(&message, i) {
			continue
		}
		levels = append(levels, OrderBookLevel{Price: message.Price(), Quantity: message.Quantity(), OrderCount: message.OrderCount()})
	}
	return levels
}
