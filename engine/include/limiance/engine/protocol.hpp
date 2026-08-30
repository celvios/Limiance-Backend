#pragma once

#include <cstdint>
#include <span>
#include <string>
#include <vector>

#include "limiance/engine/order_book.hpp"

namespace limiance::engine {

struct DecodedOrder {
  std::string pair;
  OrderCommand command;
};

struct ControlCommand {
  enum class Kind : std::uint8_t { cancel, replay, snapshot };
  Kind kind{};
  std::string command_id;
  std::string pair;
  std::string order_id;
  std::uint64_t timestamp_ns{};
  std::uint64_t from_sequence{};
};

[[nodiscard]] DecodedOrder decode_order(std::span<const std::uint8_t> payload);
[[nodiscard]] ControlCommand decode_control(std::span<const std::uint8_t> payload);
[[nodiscard]] std::vector<std::uint8_t> encode_trade(const std::string& pair, const Trade& trade);
[[nodiscard]] std::vector<std::uint8_t> encode_status(const OrderStatus& status);
[[nodiscard]] std::vector<std::uint8_t> encode_snapshot(
    const std::string& pair, std::uint64_t timestamp_ns, const Snapshot& snapshot);
[[nodiscard]] std::vector<std::uint8_t> encode_control_ack(
    const std::string& command_id, std::uint64_t timestamp_ns, bool accepted,
    std::uint64_t sequence, const std::string& reason);

}  // namespace limiance::engine
