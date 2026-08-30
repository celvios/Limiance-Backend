#pragma once

#include <cstdint>
#include <filesystem>
#include <functional>
#include <span>
#include <string>
#include <unordered_map>
#include <vector>

#include "limiance/engine/journal.hpp"
#include "limiance/engine/order_book.hpp"
#include "limiance/engine/protocol.hpp"

namespace limiance::engine {

struct PublishedEvent {
  std::string topic;
  std::vector<std::uint8_t> payload;
};
using Publisher = std::function<void(const PublishedEvent&)>;

class SymbolEngine final {
 public:
  SymbolEngine(std::string pair, std::filesystem::path journal_path, std::size_t capacity);

  [[nodiscard]] std::vector<std::uint8_t> submit(
      std::span<const std::uint8_t> payload, const Publisher& publish = {});
  [[nodiscard]] std::vector<std::uint8_t> control(
      std::span<const std::uint8_t> payload, const Publisher& publish = {});
  void replay(std::uint64_t from_trade_sequence, const Publisher& publish) const;
  [[nodiscard]] Snapshot snapshot(std::size_t depth = 1000) const { return book_.snapshot(depth); }
  [[nodiscard]] std::uint64_t journal_sequence() const noexcept { return journal_sequence_; }

 private:
  struct EncodedRecord { JournalKind kind; std::vector<std::uint8_t> payload; std::string topic; };

  [[nodiscard]] std::vector<EncodedRecord> encode_result(
      const Result& result, std::uint64_t timestamp_ns, std::uint64_t prior_book_sequence) const;
  void persist_and_publish(const std::vector<EncodedRecord>& records, const Publisher& publish);
  void append(JournalKind kind, std::span<const std::uint8_t> payload);
  void restore();
  [[nodiscard]] std::vector<std::uint8_t> cancellation_ack(
      const ControlCommand& command, bool accepted, std::uint64_t sequence) const;

  std::string pair_;
  Journal journal_;
  OrderBook book_;
  std::vector<JournalRecord> records_;
  std::unordered_map<std::string, std::vector<std::uint8_t>> order_replies_;
  std::unordered_map<std::string, std::vector<std::uint8_t>> control_replies_;
  std::uint64_t journal_sequence_{};
};

}  // namespace limiance::engine
