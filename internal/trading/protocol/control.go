package protocol

import (
	"fmt"

	flatbuffers "github.com/google/flatbuffers/go"
	controlwire "github.com/limiance/backend/internal/trading/wire/controlwire"
)

type CancelOrderCommand struct {
	CommandID   string
	OrderID     string
	Pair        string
	TimestampNS uint64
}

func (command CancelOrderCommand) Validate() error {
	if !validControlIdentifier(command.CommandID) || !validControlIdentifier(command.OrderID) {
		return fmt.Errorf("cancel command identifiers are invalid")
	}
	if command.Pair == "" || len(command.Pair) > MaxPairBytes {
		return fmt.Errorf("cancel command pair is invalid")
	}
	return nil
}

type ReplayRequest struct {
	CommandID    string
	Pair         string
	FromSequence uint64
	TimestampNS  uint64
}

func (request ReplayRequest) Validate() error {
	if !validControlIdentifier(request.CommandID) || request.Pair == "" || len(request.Pair) > MaxPairBytes || request.FromSequence == 0 {
		return fmt.Errorf("replay request is invalid")
	}
	return nil
}

type SnapshotRequest struct {
	CommandID   string
	Pair        string
	TimestampNS uint64
}

func (request SnapshotRequest) Validate() error {
	if !validControlIdentifier(request.CommandID) || request.Pair == "" || len(request.Pair) > MaxPairBytes {
		return fmt.Errorf("snapshot request is invalid")
	}
	return nil
}

type ControlAck struct {
	CommandID   string
	Accepted    bool
	SequenceID  uint64
	Reason      string
	TimestampNS uint64
}

func (ack ControlAck) Validate() error {
	if !validControlIdentifier(ack.CommandID) || len(ack.Reason) > 1000 {
		return fmt.Errorf("control acknowledgement is invalid")
	}
	if ack.Accepted && ack.SequenceID == 0 {
		return fmt.Errorf("accepted control acknowledgement requires a sequence")
	}
	if !ack.Accepted && ack.Reason == "" {
		return fmt.Errorf("rejected control acknowledgement requires a reason")
	}
	return nil
}

func EncodeCancelOrderCommand(command CancelOrderCommand) ([]byte, error) {
	if err := command.Validate(); err != nil {
		return nil, err
	}
	builder := flatbuffers.NewBuilder(192)
	orderID := builder.CreateString(command.OrderID)
	pair := builder.CreateString(command.Pair)
	controlwire.CancelOrderCommandStart(builder)
	controlwire.CancelOrderCommandAddOrderId(builder, orderID)
	controlwire.CancelOrderCommandAddPair(builder, pair)
	payload := controlwire.CancelOrderCommandEnd(builder)
	return finishControlEnvelope(builder, command.CommandID, command.TimestampNS, controlwire.ControlPayloadCancelOrderCommand, payload), nil
}

func DecodeCancelOrderCommand(data []byte) (command CancelOrderCommand, err error) {
	err = decodeControlEnvelope(data, controlwire.ControlPayloadCancelOrderCommand, func(envelope *controlwire.ControlEnvelope, table flatbuffers.Table) error {
		payload := controlwire.CancelOrderCommand{}
		payload.Init(table.Bytes, table.Pos)
		command = CancelOrderCommand{
			CommandID: string(envelope.CommandId()), TimestampNS: envelope.TimestampNs(),
			OrderID: string(payload.OrderId()), Pair: string(payload.Pair()),
		}
		return command.Validate()
	})
	return command, err
}

func EncodeReplayRequest(request ReplayRequest) ([]byte, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	builder := flatbuffers.NewBuilder(160)
	pair := builder.CreateString(request.Pair)
	controlwire.ReplayRequestStart(builder)
	controlwire.ReplayRequestAddPair(builder, pair)
	controlwire.ReplayRequestAddFromSequenceId(builder, request.FromSequence)
	payload := controlwire.ReplayRequestEnd(builder)
	return finishControlEnvelope(builder, request.CommandID, request.TimestampNS, controlwire.ControlPayloadReplayRequest, payload), nil
}

func DecodeReplayRequest(data []byte) (request ReplayRequest, err error) {
	err = decodeControlEnvelope(data, controlwire.ControlPayloadReplayRequest, func(envelope *controlwire.ControlEnvelope, table flatbuffers.Table) error {
		payload := controlwire.ReplayRequest{}
		payload.Init(table.Bytes, table.Pos)
		request = ReplayRequest{CommandID: string(envelope.CommandId()), TimestampNS: envelope.TimestampNs(), Pair: string(payload.Pair()), FromSequence: payload.FromSequenceId()}
		return request.Validate()
	})
	return request, err
}

func EncodeSnapshotRequest(request SnapshotRequest) ([]byte, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	builder := flatbuffers.NewBuilder(128)
	pair := builder.CreateString(request.Pair)
	controlwire.SnapshotRequestStart(builder)
	controlwire.SnapshotRequestAddPair(builder, pair)
	payload := controlwire.SnapshotRequestEnd(builder)
	return finishControlEnvelope(builder, request.CommandID, request.TimestampNS, controlwire.ControlPayloadSnapshotRequest, payload), nil
}

func DecodeSnapshotRequest(data []byte) (request SnapshotRequest, err error) {
	err = decodeControlEnvelope(data, controlwire.ControlPayloadSnapshotRequest, func(envelope *controlwire.ControlEnvelope, table flatbuffers.Table) error {
		payload := controlwire.SnapshotRequest{}
		payload.Init(table.Bytes, table.Pos)
		request = SnapshotRequest{CommandID: string(envelope.CommandId()), TimestampNS: envelope.TimestampNs(), Pair: string(payload.Pair())}
		return request.Validate()
	})
	return request, err
}

func EncodeControlAck(ack ControlAck) ([]byte, error) {
	if err := ack.Validate(); err != nil {
		return nil, err
	}
	builder := flatbuffers.NewBuilder(160)
	reason := builder.CreateString(ack.Reason)
	controlwire.ControlAckStart(builder)
	controlwire.ControlAckAddAccepted(builder, ack.Accepted)
	controlwire.ControlAckAddSequenceId(builder, ack.SequenceID)
	controlwire.ControlAckAddReason(builder, reason)
	payload := controlwire.ControlAckEnd(builder)
	return finishControlEnvelope(builder, ack.CommandID, ack.TimestampNS, controlwire.ControlPayloadControlAck, payload), nil
}

func DecodeControlAck(data []byte) (ack ControlAck, err error) {
	err = decodeControlEnvelope(data, controlwire.ControlPayloadControlAck, func(envelope *controlwire.ControlEnvelope, table flatbuffers.Table) error {
		payload := controlwire.ControlAck{}
		payload.Init(table.Bytes, table.Pos)
		ack = ControlAck{CommandID: string(envelope.CommandId()), TimestampNS: envelope.TimestampNs(), Accepted: payload.Accepted(), SequenceID: payload.SequenceId(), Reason: string(payload.Reason())}
		return ack.Validate()
	})
	return ack, err
}

func finishControlEnvelope(builder *flatbuffers.Builder, commandID string, timestampNS uint64, payloadType controlwire.ControlPayload, payload flatbuffers.UOffsetT) []byte {
	id := builder.CreateString(commandID)
	controlwire.ControlEnvelopeStart(builder)
	controlwire.ControlEnvelopeAddCommandId(builder, id)
	controlwire.ControlEnvelopeAddTimestampNs(builder, timestampNS)
	controlwire.ControlEnvelopeAddPayloadType(builder, payloadType)
	controlwire.ControlEnvelopeAddPayload(builder, payload)
	envelope := controlwire.ControlEnvelopeEnd(builder)
	controlwire.FinishControlEnvelopeBuffer(builder, envelope)
	return append([]byte(nil), builder.FinishedBytes()...)
}

func decodeControlEnvelope(data []byte, expected controlwire.ControlPayload, decode func(*controlwire.ControlEnvelope, flatbuffers.Table) error) error {
	if len(data) < 8 || len(data) > MaxWireMessageBytes || !controlwire.ControlEnvelopeBufferHasIdentifier(data) {
		return fmt.Errorf("invalid order-control envelope")
	}
	return decodeSafely(data, func() error {
		envelope := controlwire.GetRootAsControlEnvelope(data, 0)
		if envelope.PayloadType() != expected {
			return fmt.Errorf("unexpected order-control payload %s", envelope.PayloadType())
		}
		var table flatbuffers.Table
		if !envelope.Payload(&table) {
			return fmt.Errorf("order-control payload is missing")
		}
		return decode(envelope, table)
	})
}

func validControlIdentifier(value string) bool {
	return value != "" && len(value) <= MaxIdentifierBytes
}
