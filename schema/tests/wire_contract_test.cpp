#include <cstdint>
#include <string>
#include <vector>

#include "../generated/cpp/trading_generated.h"

namespace {

std::vector<std::uint8_t> DecodeHex(const std::string& value) {
  std::vector<std::uint8_t> bytes;
  bytes.reserve(value.size() / 2);
  for (std::size_t i = 0; i < value.size(); i += 2) {
    bytes.push_back(static_cast<std::uint8_t>(std::stoul(value.substr(i, 2), nullptr, 16)));
  }
  return bytes;
}

}  // namespace

int main() {
  const auto bytes = DecodeHex(
      "200000001c003c003800340030002f0000002000180017001600000008000700"
      "1c0000000000000300002da03801f017000000000000010140420f0000000000"
      "8040342a8c04000000000000000000010c000000140000001c00000007000000"
      "425443555344540006000000757365722d310000070000006f726465722d3100");

  flatbuffers::Verifier verifier(bytes.data(), bytes.size());
  if (!limiance::trading::VerifyOrderIngressBuffer(verifier)) return 1;

  const auto* order = limiance::trading::GetOrderIngress(bytes.data());
  if (order->id()->str() != "order-1") return 2;
  if (order->user_id()->str() != "user-1") return 3;
  if (order->pair()->str() != "BTCUSDT") return 4;
  if (order->side() != limiance::trading::OrderSide::SELL) return 5;
  if (order->order_type() != limiance::trading::OrderType::LIMIT) return 6;
  if (order->price() != 5000050000000ULL) return 7;
  if (order->quantity() != 1000000ULL) return 8;
  if (order->time_in_force() != limiance::trading::TimeInForce::IOC) return 9;
  if (!order->post_only()) return 10;
  if (order->reduce_only()) return 11;
  if (order->timestamp_ns() != 1724880000000000000ULL) return 12;
  if (order->fee_tier() != 3) return 13;
  return 0;
}
