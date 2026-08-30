#include "limiance/engine/symbol_engine.hpp"

#include <algorithm>
#include <stdexcept>

#include "flatbuffers/flatbuffers.h"
#include "trading_generated.h"

namespace limiance::engine {
namespace {
constexpr const char* trade_topic = "trade";
constexpr const char* status_topic = "order_status";
constexpr const char* book_topic = "order_book";

std::uint64_t trade_sequence(std::span<const std::uint8_t> payload) {
  flatbuffers::Verifier verifier(payload.data(), payload.size());
  if (!verifier.VerifyBuffer<trading::TradeEvent>(nullptr)) throw JournalError("invalid trade event in journal");
  return flatbuffers::GetRoot<trading::TradeEvent>(payload.data())->sequence_id();
}
}  // namespace

SymbolEngine::SymbolEngine(std::string pair, std::filesystem::path journal_path, std::size_t capacity)
    : pair_(std::move(pair)), journal_(std::move(journal_path)), book_(capacity) {
  if (pair_.empty()) throw std::invalid_argument("symbol pair is required");
  restore();
}

void SymbolEngine::append(JournalKind kind, std::span<const std::uint8_t> payload) {
  journal_.append(kind, ++journal_sequence_, payload);
  records_.push_back(JournalRecord{kind, journal_sequence_, {payload.begin(), payload.end()}});
}

std::vector<SymbolEngine::EncodedRecord> SymbolEngine::encode_result(
    const Result& result, std::uint64_t timestamp_ns, std::uint64_t prior_book_sequence) const {
  std::vector<EncodedRecord> records;
  records.reserve(result.trades.size() + result.statuses.size() + 1);
  for (const auto& trade : result.trades) records.push_back({JournalKind::trade_event, encode_trade(pair_, trade), trade_topic});
  for (const auto& status : result.statuses) records.push_back({JournalKind::status_event, encode_status(status), status_topic});
  if (book_.book_sequence() != prior_book_sequence) {
    records.push_back({JournalKind::book_snapshot, encode_snapshot(pair_, timestamp_ns, book_.snapshot(1000)), book_topic});
  }
  return records;
}

void SymbolEngine::persist_and_publish(const std::vector<EncodedRecord>& records, const Publisher& publish) {
  for (const auto& record : records) {
    append(record.kind, record.payload);
    if (publish) publish(PublishedEvent{record.topic, record.payload});
  }
}

std::vector<std::uint8_t> SymbolEngine::submit(std::span<const std::uint8_t> payload, const Publisher& publish) {
  const auto decoded = decode_order(payload);
  if (decoded.pair != pair_) throw std::invalid_argument("order routed to wrong symbol partition");
  if (const auto found = order_replies_.find(decoded.command.id); found != order_replies_.end()) return found->second;
  append(JournalKind::order_command, payload);
  const auto prior = book_.book_sequence();
  const auto result = book_.submit(decoded.command);
  const auto events = encode_result(result, decoded.command.timestamp_ns, prior);
  persist_and_publish(events, publish);
  if (result.statuses.empty()) throw std::logic_error("new order produced no status");
  auto reply = encode_status(result.statuses.back());
  order_replies_.emplace(decoded.command.id, reply);
  return reply;
}

std::vector<std::uint8_t> SymbolEngine::cancellation_ack(
    const ControlCommand& command, bool accepted, std::uint64_t sequence) const {
  return encode_control_ack(command.command_id, command.timestamp_ns, accepted, sequence,
                            accepted ? "" : "order is not open");
}

std::vector<std::uint8_t> SymbolEngine::control(std::span<const std::uint8_t> payload, const Publisher& publish) {
  const auto command = decode_control(payload);
  if (command.pair != pair_) throw std::invalid_argument("control command routed to wrong symbol partition");
  if (const auto found = control_replies_.find(command.command_id); found != control_replies_.end()) return found->second;
  if (command.kind == ControlCommand::Kind::replay) {
    replay(command.from_sequence, publish);
    auto reply = encode_control_ack(command.command_id, command.timestamp_ns, true,
                                    std::max<std::uint64_t>(1, book_.trade_sequence()), "");
    control_replies_.emplace(command.command_id, reply);
    return reply;
  }
  if (command.kind == ControlCommand::Kind::snapshot) {
    const auto encoded = encode_snapshot(pair_, command.timestamp_ns, book_.snapshot(1000));
    if (publish) publish(PublishedEvent{book_topic, encoded});
    auto reply = encode_control_ack(command.command_id, command.timestamp_ns, true,
                                    std::max<std::uint64_t>(1, book_.book_sequence()), "");
    control_replies_.emplace(command.command_id, reply);
    return reply;
  }

  append(JournalKind::cancel_command, payload);
  const auto prior = book_.book_sequence();
  const auto result = book_.cancel(command.order_id, command.timestamp_ns);
  const auto events = encode_result(result, command.timestamp_ns, prior);
  persist_and_publish(events, publish);
  const bool accepted = !result.statuses.empty();
  const auto sequence = accepted ? result.statuses.back().sequence_id : book_.status_sequence();
  auto reply = cancellation_ack(command, accepted, sequence);
  append(JournalKind::control_ack, reply);
  control_replies_.emplace(command.command_id, reply);
  return reply;
}

void SymbolEngine::replay(std::uint64_t from_trade_sequence, const Publisher& publish) const {
  if (!publish) return;
  for (const auto& record : records_) {
    if (record.kind == JournalKind::trade_event && trade_sequence(record.payload) >= from_trade_sequence) {
      publish(PublishedEvent{trade_topic, record.payload});
    }
  }
}

void SymbolEngine::restore() {
  records_ = journal_.recover();
  if (!records_.empty()) journal_sequence_ = records_.back().sequence;
  std::size_t index{};
  while (index < records_.size()) {
    const auto command_record = records_[index++];
    if (command_record.kind != JournalKind::order_command && command_record.kind != JournalKind::cancel_command) {
      throw JournalError("journal command boundary is invalid");
    }
    const auto prior = book_.book_sequence();
    Result result;
    std::uint64_t timestamp{};
    std::string command_id;
    std::string order_id;
    ControlCommand cancel_command;
    if (command_record.kind == JournalKind::order_command) {
      const auto decoded = decode_order(command_record.payload);
      if (decoded.pair != pair_) throw JournalError("journal contains another symbol");
      timestamp = decoded.command.timestamp_ns;
      order_id = decoded.command.id;
      result = book_.submit(decoded.command);
    } else {
      const auto decoded = decode_control(command_record.payload);
      if (decoded.kind != ControlCommand::Kind::cancel || decoded.pair != pair_) throw JournalError("invalid journal cancel command");
      cancel_command = decoded;
      timestamp = decoded.timestamp_ns;
      command_id = decoded.command_id;
      result = book_.cancel(decoded.order_id, decoded.timestamp_ns);
    }
    const auto expected = encode_result(result, timestamp, prior);
    for (const auto& event : expected) {
      if (index >= records_.size()) {
        append(event.kind, event.payload);
      } else if (records_[index].kind != event.kind || records_[index].payload != event.payload) {
        throw JournalError("journal replay diverged from deterministic output");
      }
      ++index;
    }
    if (command_record.kind == JournalKind::order_command) {
      if (result.statuses.empty()) throw JournalError("journal order has no response");
      order_replies_[order_id] = encode_status(result.statuses.back());
    } else {
      const bool accepted = !result.statuses.empty();
      const auto sequence = accepted ? result.statuses.back().sequence_id : book_.status_sequence();
      const auto expected_ack = cancellation_ack(cancel_command, accepted, sequence);
      if (index >= records_.size()) {
        append(JournalKind::control_ack, expected_ack);
      } else if (records_[index].kind != JournalKind::control_ack || records_[index].payload != expected_ack) {
        throw JournalError("journal cancel acknowledgement diverged");
      }
      control_replies_[command_id] = records_[index++].payload;
    }
  }
}

}  // namespace limiance::engine
