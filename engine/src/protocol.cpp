#include "limiance/engine/protocol.hpp"

#include <stdexcept>

#include "flatbuffers/flatbuffers.h"
#include "market_data_generated.h"
#include "order_control_generated.h"
#include "trading_generated.h"

namespace limiance::engine {
namespace {
constexpr std::size_t max_wire_bytes = 8U << 20;

void verify_size(std::span<const std::uint8_t> payload) {
  if (payload.size() < sizeof(flatbuffers::uoffset_t) || payload.size() > max_wire_bytes) {
    throw std::invalid_argument("invalid wire message size");
  }
}

std::vector<std::uint8_t> finish(flatbuffers::FlatBufferBuilder& builder,
                                 flatbuffers::Offset<void> root) {
  builder.Finish(root);
  return {builder.GetBufferPointer(), builder.GetBufferPointer() + builder.GetSize()};
}
}  // namespace

DecodedOrder decode_order(std::span<const std::uint8_t> payload) {
  verify_size(payload);
  flatbuffers::Verifier verifier(payload.data(), payload.size());
  if (!trading::VerifyOrderIngressBuffer(verifier)) throw std::invalid_argument("malformed order command");
  const auto* wire = trading::GetOrderIngress(payload.data());
  if (wire->id() == nullptr || wire->user_id() == nullptr || wire->pair() == nullptr ||
      wire->side() > trading::OrderSide::SELL || wire->order_type() > trading::OrderType::STOP_LIMIT ||
      wire->time_in_force() > trading::TimeInForce::FOK) {
    throw std::invalid_argument("invalid order command");
  }
  if (wire->order_type() == trading::OrderType::STOP_LIMIT) {
    throw std::invalid_argument("conditional orders must be triggered by the gateway");
  }
  return DecodedOrder{
      wire->pair()->str(),
      OrderCommand{wire->id()->str(), wire->user_id()->str(),
                   wire->side() == trading::OrderSide::BUY ? Side::buy : Side::sell,
                   wire->order_type() == trading::OrderType::LIMIT ? OrderType::limit : OrderType::market,
                   static_cast<TimeInForce>(wire->time_in_force()), wire->price(), wire->quantity(),
                   wire->timestamp_ns(), wire->fee_tier(), wire->post_only()}};
}

ControlCommand decode_control(std::span<const std::uint8_t> payload) {
  verify_size(payload);
  flatbuffers::Verifier verifier(payload.data(), payload.size());
  if (!control::VerifyControlEnvelopeBuffer(verifier)) throw std::invalid_argument("malformed control command");
  const auto* envelope = control::GetControlEnvelope(payload.data());
  if (envelope->command_id() == nullptr || envelope->command_id()->str().empty()) {
    throw std::invalid_argument("control command id is required");
  }
  ControlCommand result{};
  result.command_id = envelope->command_id()->str();
  result.timestamp_ns = envelope->timestamp_ns();
  switch (envelope->payload_type()) {
    case control::ControlPayload_CancelOrderCommand: {
      const auto* command = envelope->payload_as_CancelOrderCommand();
      if (command == nullptr || command->pair() == nullptr || command->order_id() == nullptr) {
        throw std::invalid_argument("invalid cancel command");
      }
      result.kind = ControlCommand::Kind::cancel;
      result.pair = command->pair()->str();
      result.order_id = command->order_id()->str();
      break;
    }
    case control::ControlPayload_ReplayRequest: {
      const auto* command = envelope->payload_as_ReplayRequest();
      if (command == nullptr || command->pair() == nullptr || command->from_sequence_id() == 0) {
        throw std::invalid_argument("invalid replay command");
      }
      result.kind = ControlCommand::Kind::replay;
      result.pair = command->pair()->str();
      result.from_sequence = command->from_sequence_id();
      break;
    }
    case control::ControlPayload_SnapshotRequest: {
      const auto* command = envelope->payload_as_SnapshotRequest();
      if (command == nullptr || command->pair() == nullptr) throw std::invalid_argument("invalid snapshot command");
      result.kind = ControlCommand::Kind::snapshot;
      result.pair = command->pair()->str();
      break;
    }
    default:
      throw std::invalid_argument("unsupported control command");
  }
  if (result.pair.empty()) throw std::invalid_argument("pair is required");
  return result;
}

std::vector<std::uint8_t> encode_trade(const std::string& pair, const Trade& trade) {
  flatbuffers::FlatBufferBuilder builder(256);
  const auto root = trading::CreateTradeEventDirect(
      builder, trade.sequence_id, trade.timestamp_ns, pair.c_str(), trade.maker_order_id.c_str(),
      trade.taker_order_id.c_str(), trade.maker_user_id.c_str(), trade.taker_user_id.c_str(),
      trade.price, trade.quantity, trade.maker_fee_bps, trade.taker_fee_bps);
  return finish(builder, root.Union());
}

std::vector<std::uint8_t> encode_status(const OrderStatus& status) {
  flatbuffers::FlatBufferBuilder builder(128);
  const auto root = trading::CreateOrderStatusEventDirect(
      builder, status.sequence_id, status.order_id.c_str(), static_cast<trading::OrderStatus>(status.status),
      status.filled_quantity, status.remaining_quantity, status.average_price);
  return finish(builder, root.Union());
}

std::vector<std::uint8_t> encode_snapshot(const std::string& pair, std::uint64_t timestamp_ns,
                                          const Snapshot& snapshot) {
  flatbuffers::FlatBufferBuilder builder(1024);
  std::vector<flatbuffers::Offset<marketdata::OrderBookLevel>> bids;
  std::vector<flatbuffers::Offset<marketdata::OrderBookLevel>> asks;
  bids.reserve(snapshot.bids.size());
  asks.reserve(snapshot.asks.size());
  for (const auto& level : snapshot.bids) bids.push_back(marketdata::CreateOrderBookLevel(builder, level.price, level.quantity, level.order_count));
  for (const auto& level : snapshot.asks) asks.push_back(marketdata::CreateOrderBookLevel(builder, level.price, level.quantity, level.order_count));
  const auto pair_offset = builder.CreateString(pair);
  const auto root = marketdata::CreateOrderBookSnapshot(builder, snapshot.sequence_id, timestamp_ns,
                                                         pair_offset, builder.CreateVector(bids), builder.CreateVector(asks));
  marketdata::FinishOrderBookSnapshotBuffer(builder, root);
  return {builder.GetBufferPointer(), builder.GetBufferPointer() + builder.GetSize()};
}

std::vector<std::uint8_t> encode_control_ack(const std::string& command_id, std::uint64_t timestamp_ns,
                                             bool accepted, std::uint64_t sequence, const std::string& reason) {
  flatbuffers::FlatBufferBuilder builder(128);
  const auto reason_offset = builder.CreateString(reason);
  const auto ack = control::CreateControlAck(builder, accepted, sequence, reason_offset);
  const auto id = builder.CreateString(command_id);
  const auto envelope = control::CreateControlEnvelope(builder, id, timestamp_ns,
      control::ControlPayload_ControlAck, ack.Union());
  control::FinishControlEnvelopeBuffer(builder, envelope);
  return {builder.GetBufferPointer(), builder.GetBufferPointer() + builder.GetSize()};
}

}  // namespace limiance::engine
