package protocol

import (
	"fmt"

	flatbuffers "github.com/google/flatbuffers/go"
	tradingwire "github.com/limiance/backend/internal/trading/wire/tradingwire"
)

func EncodeOrderIngress(order OrderIngress) ([]byte, error) {
	if err := order.Validate(); err != nil {
		return nil, err
	}
	builder := flatbuffers.NewBuilder(256)
	id := builder.CreateString(order.ID)
	userID := builder.CreateString(order.UserID)
	pair := builder.CreateString(order.Pair)
	tradingwire.OrderIngressStart(builder)
	tradingwire.OrderIngressAddId(builder, id)
	tradingwire.OrderIngressAddUserId(builder, userID)
	tradingwire.OrderIngressAddPair(builder, pair)
	tradingwire.OrderIngressAddSide(builder, tradingwire.OrderSide(order.Side))
	tradingwire.OrderIngressAddOrderType(builder, tradingwire.OrderType(order.OrderType))
	tradingwire.OrderIngressAddPrice(builder, order.Price)
	tradingwire.OrderIngressAddQuantity(builder, order.Quantity)
	tradingwire.OrderIngressAddTimeInForce(builder, tradingwire.TimeInForce(order.TimeInForce))
	tradingwire.OrderIngressAddPostOnly(builder, order.PostOnly)
	tradingwire.OrderIngressAddReduceOnly(builder, order.ReduceOnly)
	tradingwire.OrderIngressAddTimestampNs(builder, order.TimestampNS)
	tradingwire.OrderIngressAddFeeTier(builder, order.FeeTier)
	offset := tradingwire.OrderIngressEnd(builder)
	tradingwire.FinishOrderIngressBuffer(builder, offset)
	return append([]byte(nil), builder.FinishedBytes()...), nil
}

func DecodeOrderIngress(data []byte) (order OrderIngress, err error) {
	err = decodeSafely(data, func() error {
		message := tradingwire.GetRootAsOrderIngress(data, 0)
		order = OrderIngress{
			ID:          string(message.Id()),
			UserID:      string(message.UserId()),
			Pair:        string(message.Pair()),
			Side:        OrderSide(message.Side()),
			OrderType:   OrderType(message.OrderType()),
			Price:       message.Price(),
			Quantity:    message.Quantity(),
			TimeInForce: TimeInForce(message.TimeInForce()),
			PostOnly:    message.PostOnly(),
			ReduceOnly:  message.ReduceOnly(),
			TimestampNS: message.TimestampNs(),
			FeeTier:     message.FeeTier(),
		}
		return order.Validate()
	})
	return order, err
}

func EncodeTradeEvent(event TradeEvent) ([]byte, error) {
	if err := event.Validate(); err != nil {
		return nil, err
	}
	builder := flatbuffers.NewBuilder(256)
	pair := builder.CreateString(event.Pair)
	makerOrderID := builder.CreateString(event.MakerOrderID)
	takerOrderID := builder.CreateString(event.TakerOrderID)
	makerUserID := builder.CreateString(event.MakerUserID)
	takerUserID := builder.CreateString(event.TakerUserID)
	tradingwire.TradeEventStart(builder)
	tradingwire.TradeEventAddSequenceId(builder, event.SequenceID)
	tradingwire.TradeEventAddTimestampNs(builder, event.TimestampNS)
	tradingwire.TradeEventAddPair(builder, pair)
	tradingwire.TradeEventAddMakerOrderId(builder, makerOrderID)
	tradingwire.TradeEventAddTakerOrderId(builder, takerOrderID)
	tradingwire.TradeEventAddMakerUserId(builder, makerUserID)
	tradingwire.TradeEventAddTakerUserId(builder, takerUserID)
	tradingwire.TradeEventAddPrice(builder, event.Price)
	tradingwire.TradeEventAddQuantity(builder, event.Quantity)
	tradingwire.TradeEventAddMakerFeeBps(builder, event.MakerFeeBPS)
	tradingwire.TradeEventAddTakerFeeBps(builder, event.TakerFeeBPS)
	offset := tradingwire.TradeEventEnd(builder)
	tradingwire.FinishTradeEventBuffer(builder, offset)
	return append([]byte(nil), builder.FinishedBytes()...), nil
}

func DecodeTradeEvent(data []byte) (event TradeEvent, err error) {
	err = decodeSafely(data, func() error {
		message := tradingwire.GetRootAsTradeEvent(data, 0)
		event = TradeEvent{
			SequenceID:   message.SequenceId(),
			TimestampNS:  message.TimestampNs(),
			Pair:         string(message.Pair()),
			MakerOrderID: string(message.MakerOrderId()),
			TakerOrderID: string(message.TakerOrderId()),
			MakerUserID:  string(message.MakerUserId()),
			TakerUserID:  string(message.TakerUserId()),
			Price:        message.Price(),
			Quantity:     message.Quantity(),
			MakerFeeBPS:  message.MakerFeeBps(),
			TakerFeeBPS:  message.TakerFeeBps(),
		}
		return event.Validate()
	})
	return event, err
}

func EncodeOrderStatusEvent(event OrderStatusEvent) ([]byte, error) {
	if err := event.Validate(); err != nil {
		return nil, err
	}
	builder := flatbuffers.NewBuilder(128)
	orderID := builder.CreateString(event.OrderID)
	tradingwire.OrderStatusEventStart(builder)
	tradingwire.OrderStatusEventAddSequenceId(builder, event.SequenceID)
	tradingwire.OrderStatusEventAddOrderId(builder, orderID)
	tradingwire.OrderStatusEventAddStatus(builder, tradingwire.OrderStatus(event.Status))
	tradingwire.OrderStatusEventAddFilledQuantity(builder, event.FilledQuantity)
	tradingwire.OrderStatusEventAddRemainingQuantity(builder, event.RemainingQuantity)
	tradingwire.OrderStatusEventAddAvgPrice(builder, event.AvgPrice)
	offset := tradingwire.OrderStatusEventEnd(builder)
	tradingwire.FinishOrderStatusEventBuffer(builder, offset)
	return append([]byte(nil), builder.FinishedBytes()...), nil
}

func DecodeOrderStatusEvent(data []byte) (event OrderStatusEvent, err error) {
	err = decodeSafely(data, func() error {
		message := tradingwire.GetRootAsOrderStatusEvent(data, 0)
		event = OrderStatusEvent{
			SequenceID:        message.SequenceId(),
			OrderID:           string(message.OrderId()),
			Status:            OrderStatus(message.Status()),
			FilledQuantity:    message.FilledQuantity(),
			RemainingQuantity: message.RemainingQuantity(),
			AvgPrice:          message.AvgPrice(),
		}
		return event.Validate()
	})
	return event, err
}

func decodeSafely(data []byte, decode func() error) (err error) {
	if len(data) < flatbuffers.SizeUOffsetT {
		return fmt.Errorf("wire message is too short")
	}
	if len(data) > MaxWireMessageBytes {
		return fmt.Errorf("wire message exceeds %d bytes", MaxWireMessageBytes)
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("malformed wire message: %v", recovered)
		}
	}()
	return decode()
}
