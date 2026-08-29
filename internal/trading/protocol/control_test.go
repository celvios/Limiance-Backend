package protocol

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func TestOrderControlMessagesRoundTrip(t *testing.T) {
	tests := []struct {
		name   string
		encode func() ([]byte, error)
		decode func([]byte) error
	}{
		{
			name: "cancel",
			encode: func() ([]byte, error) {
				return EncodeCancelOrderCommand(CancelOrderCommand{CommandID: "command-1", OrderID: "order-1", Pair: "BTCUSDT", TimestampNS: 1724880000000000000})
			},
			decode: func(data []byte) error {
				got, err := DecodeCancelOrderCommand(data)
				if err == nil && (got.OrderID != "order-1" || got.Pair != "BTCUSDT") {
					t.Fatalf("unexpected cancel command %#v", got)
				}
				return err
			},
		},
		{
			name: "replay",
			encode: func() ([]byte, error) {
				return EncodeReplayRequest(ReplayRequest{CommandID: "command-2", Pair: "ETHUSDT", FromSequence: 42, TimestampNS: 2})
			},
			decode: func(data []byte) error {
				got, err := DecodeReplayRequest(data)
				if err == nil && got.FromSequence != 42 {
					t.Fatalf("unexpected replay request %#v", got)
				}
				return err
			},
		},
		{
			name: "snapshot",
			encode: func() ([]byte, error) {
				return EncodeSnapshotRequest(SnapshotRequest{CommandID: "command-3", Pair: "SOLUSDT", TimestampNS: 3})
			},
			decode: func(data []byte) error {
				_, err := DecodeSnapshotRequest(data)
				return err
			},
		},
		{
			name: "ack",
			encode: func() ([]byte, error) {
				return EncodeControlAck(ControlAck{CommandID: "command-1", Accepted: true, SequenceID: 99, TimestampNS: 4})
			},
			decode: func(data []byte) error {
				got, err := DecodeControlAck(data)
				if err == nil && (!got.Accepted || got.SequenceID != 99) {
					t.Fatalf("unexpected control acknowledgement %#v", got)
				}
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := test.encode()
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if err := test.decode(encoded); err != nil {
				t.Fatalf("decode: %v", err)
			}
		})
	}
}

func TestOrderControlRejectsWrongIdentifierAndPayload(t *testing.T) {
	encoded, err := EncodeCancelOrderCommand(CancelOrderCommand{CommandID: "command-1", OrderID: "order-1", Pair: "BTCUSDT"})
	if err != nil {
		t.Fatalf("encode cancel: %v", err)
	}
	const golden = "140000004c4f43540c0010000c0000000b0004000c00000024000000000000010400000009000000636f6d6d616e642d3100000008000c0008000400080000000800000010000000070000004254435553445400070000006f726465722d3100"
	if got := hex.EncodeToString(encoded); got != golden {
		t.Fatalf("control wire golden changed:\nwant %s\n got %s", golden, got)
	}
	wrongIdentifier := bytes.Clone(encoded)
	copy(wrongIdentifier[4:8], []byte("NOPE"))
	if _, err := DecodeCancelOrderCommand(wrongIdentifier); err == nil {
		t.Fatal("expected wrong file identifier to fail")
	}
	if _, err := DecodeReplayRequest(encoded); err == nil {
		t.Fatal("expected wrong union payload to fail")
	}
}

func TestOrderControlRejectsMalformedAndInvalidCommands(t *testing.T) {
	for _, data := range [][]byte{nil, {1, 2, 3}, make([]byte, MaxWireMessageBytes+1)} {
		if _, err := DecodeCancelOrderCommand(data); err == nil {
			t.Fatalf("expected malformed message of length %d to fail", len(data))
		}
	}
	if _, err := EncodeCancelOrderCommand(CancelOrderCommand{CommandID: "command-1", Pair: "BTCUSDT"}); err == nil {
		t.Fatal("expected missing order id to fail")
	}
	if _, err := EncodeReplayRequest(ReplayRequest{CommandID: "command-1", Pair: "BTCUSDT"}); err == nil {
		t.Fatal("expected zero replay sequence to fail")
	}
	if _, err := EncodeControlAck(ControlAck{CommandID: "command-1", Accepted: false}); err == nil {
		t.Fatal("expected rejected acknowledgement without reason to fail")
	}
}
