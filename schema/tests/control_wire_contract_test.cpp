#include <cstdint>
#include <string>
#include <vector>

#include "../generated/cpp/order_control_generated.h"

namespace {

std::vector<std::uint8_t> DecodeHex(const std::string& value) {
  std::vector<std::uint8_t> bytes;
  bytes.reserve(value.size() / 2);
  for (std::size_t i = 0; i < value.size(); i += 2) {
    bytes.push_back(static_cast<std::uint8_t>(
        std::stoul(value.substr(i, 2), nullptr, 16)));
  }
  return bytes;
}

}  // namespace

int main() {
  // Produced by protocol.EncodeCancelOrderCommand in Go. This is deliberately
  // a golden byte contract so either generator or schema drift breaks both
  // language suites before it can reach the engine.
  const auto bytes = DecodeHex(
      "140000004c4f43540c0010000c0000000b0004000c0000002400000000000001"
      "0400000009000000636f6d6d616e642d3100000008000c000800040008000000"
      "0800000010000000070000004254435553445400070000006f726465722d3100");

  flatbuffers::Verifier verifier(bytes.data(), bytes.size());
  if (!limiance::control::VerifyControlEnvelopeBuffer(verifier)) return 1;
  if (!limiance::control::ControlEnvelopeBufferHasIdentifier(bytes.data())) return 2;

  const auto* envelope = limiance::control::GetControlEnvelope(bytes.data());
  if (envelope->command_id()->str() != "command-1") return 3;
  if (envelope->payload_type() !=
      limiance::control::ControlPayload_CancelOrderCommand) return 4;
  const auto* cancel = envelope->payload_as_CancelOrderCommand();
  if (cancel == nullptr) return 5;
  if (cancel->order_id()->str() != "order-1") return 6;
  if (cancel->pair()->str() != "BTCUSDT") return 7;
  return 0;
}
