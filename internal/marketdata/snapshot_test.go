package marketdata

import (
	"reflect"
	"testing"
)

func TestMaximumDepthSnapshotWireRoundTrip(t *testing.T) {
	bids := make([]OrderBookLevel, MaxSnapshotDepth)
	asks := make([]OrderBookLevel, MaxSnapshotDepth)
	for i := 0; i < MaxSnapshotDepth; i++ {
		bids[i] = OrderBookLevel{Price: uint64(5000000000000 - i), Quantity: uint64(i + 1), OrderCount: 1}
		asks[i] = OrderBookLevel{Price: uint64(5000000000001 + i), Quantity: uint64(i + 1), OrderCount: 1}
	}
	want := OrderBookSnapshot{SequenceID: 99, TimestampNS: 1724880000000000000, Pair: "BTCUSDT", Bids: bids, Asks: asks}
	encoded, err := EncodeOrderBookSnapshot(want)
	if err != nil {
		t.Fatalf("encode snapshot: %v", err)
	}
	got, err := DecodeOrderBookSnapshot(encoded)
	if err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("snapshot changed during wire round trip")
	}
}

func TestOversizedSnapshotRejected(t *testing.T) {
	snapshot := OrderBookSnapshot{SequenceID: 1, Pair: "BTCUSDT", Bids: make([]OrderBookLevel, MaxSnapshotDepth+1)}
	if _, err := EncodeOrderBookSnapshot(snapshot); err == nil {
		t.Fatal("expected oversized snapshot to be rejected")
	}
}
