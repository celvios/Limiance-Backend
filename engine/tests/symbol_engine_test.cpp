#include "limiance/engine/symbol_engine.hpp"

#include <chrono>
#include <cstdlib>
#include <filesystem>
#include <random>
#include <string>
#include <vector>

#include <gtest/gtest.h>

#include "flatbuffers/flatbuffers.h"
#include "market_data_generated.h"
#include "order_control_generated.h"
#include "trading_generated.h"

namespace limiance::engine {
namespace {
class TempJournal final {
 public:
  TempJournal() : path_((std::getenv("ENGINE_TEST_JOURNAL_DIR") != nullptr
      ? std::filesystem::path(std::getenv("ENGINE_TEST_JOURNAL_DIR"))
      : std::filesystem::temp_directory_path()) /
      ("limiance-engine-" + std::to_string(std::chrono::steady_clock::now().time_since_epoch().count()) + ".journal")) {}
  ~TempJournal() { std::error_code ignored; std::filesystem::remove(path_, ignored); }
  const std::filesystem::path& path() const { return path_; }
 private:
  std::filesystem::path path_;
};

std::vector<std::uint8_t> order_message(const std::string& id, trading::OrderSide side,
                                        std::uint64_t price, std::uint64_t quantity,
                                        trading::TimeInForce tif = trading::TimeInForce::GTC,
                                        trading::OrderType type = trading::OrderType::LIMIT) {
  flatbuffers::FlatBufferBuilder builder(256);
  const auto root = trading::CreateOrderIngressDirect(builder, id.c_str(), "user", "BTCUSDT", side,
      type, price, quantity, tif, false, false, 100, 0);
  trading::FinishOrderIngressBuffer(builder, root);
  return {builder.GetBufferPointer(), builder.GetBufferPointer() + builder.GetSize()};
}

std::vector<std::uint8_t> cancel_message(const std::string& command_id, const std::string& order_id) {
  flatbuffers::FlatBufferBuilder builder(128);
  const auto order = builder.CreateString(order_id);
  const auto pair = builder.CreateString("BTCUSDT");
  const auto cancel = control::CreateCancelOrderCommand(builder, order, pair);
  const auto id = builder.CreateString(command_id);
  const auto envelope = control::CreateControlEnvelope(builder, id, 200,
      control::ControlPayload_CancelOrderCommand, cancel.Union());
  control::FinishControlEnvelopeBuffer(builder, envelope);
  return {builder.GetBufferPointer(), builder.GetBufferPointer() + builder.GetSize()};
}

void expect_equal(const Snapshot& left, const Snapshot& right) {
  ASSERT_EQ(left.sequence_id, right.sequence_id);
  ASSERT_EQ(left.bids.size(), right.bids.size());
  ASSERT_EQ(left.asks.size(), right.asks.size());
  for (std::size_t index = 0; index < left.bids.size(); ++index) {
    EXPECT_EQ(left.bids[index].price, right.bids[index].price);
    EXPECT_EQ(left.bids[index].quantity, right.bids[index].quantity);
    EXPECT_EQ(left.bids[index].order_count, right.bids[index].order_count);
  }
  for (std::size_t index = 0; index < left.asks.size(); ++index) {
    EXPECT_EQ(left.asks[index].price, right.asks[index].price);
    EXPECT_EQ(left.asks[index].quantity, right.asks[index].quantity);
    EXPECT_EQ(left.asks[index].order_count, right.asks[index].order_count);
  }
}
}  // namespace

TEST(ProtocolTest, RejectsMalformedAndConditionalCommands) {
  EXPECT_THROW(decode_order(std::vector<std::uint8_t>{1, 2, 3, 4}), std::invalid_argument);
  const auto conditional = order_message("conditional", trading::OrderSide::BUY, 100, 1,
                                         trading::TimeInForce::GTC, trading::OrderType::STOP_LIMIT);
  EXPECT_THROW(decode_order(conditional), std::invalid_argument);
}

TEST(ProtocolTest, RejectsUnknownEnumValues) {
  const auto invalid = order_message("invalid-side", static_cast<trading::OrderSide>(99), 100, 1);
  EXPECT_THROW(decode_order(invalid), std::invalid_argument);
}

TEST(ProtocolTest, EncodesTradeAndStatusForTheGoConsumer) {
  const Trade trade{7, 100, "maker", "taker", "maker-user", "taker-user", 50000, 25, -2, 8};
  const auto trade_payload = encode_trade("BTCUSDT", trade);
  flatbuffers::Verifier trade_verifier(trade_payload.data(), trade_payload.size());
  ASSERT_TRUE(trade_verifier.VerifyBuffer<trading::TradeEvent>(nullptr));
  const auto* wire_trade = flatbuffers::GetRoot<trading::TradeEvent>(trade_payload.data());
  EXPECT_EQ(wire_trade->sequence_id(), 7U);
  EXPECT_EQ(wire_trade->maker_fee_bps(), -2);
  EXPECT_EQ(wire_trade->taker_fee_bps(), 8);

  const auto status_payload = encode_status({9, "order", Status::partial, 25, 75, 50000});
  flatbuffers::Verifier status_verifier(status_payload.data(), status_payload.size());
  ASSERT_TRUE(status_verifier.VerifyBuffer<trading::OrderStatusEvent>(nullptr));
  const auto* wire_status = flatbuffers::GetRoot<trading::OrderStatusEvent>(status_payload.data());
  EXPECT_EQ(wire_status->status(), trading::OrderStatus::PARTIAL);
  EXPECT_EQ(wire_status->filled_quantity(), 25U);
  EXPECT_EQ(wire_status->remaining_quantity(), 75U);
}

TEST(ProtocolTest, EncodesOneThousandOrderBookLevels) {
  Snapshot snapshot;
  snapshot.sequence_id = 42;
  for (std::uint64_t index = 0; index < 500; ++index) {
    snapshot.bids.push_back({50000 - index, index + 1, 1});
    snapshot.asks.push_back({50001 + index, index + 1, 1});
  }
  const auto payload = encode_snapshot("BTCUSDT", 100, snapshot);
  flatbuffers::Verifier verifier(payload.data(), payload.size());
  ASSERT_TRUE(marketdata::VerifyOrderBookSnapshotBuffer(verifier));
  const auto* decoded = marketdata::GetOrderBookSnapshot(payload.data());
  ASSERT_NE(decoded->bids(), nullptr);
  ASSERT_NE(decoded->asks(), nullptr);
  EXPECT_EQ(decoded->bids()->size() + decoded->asks()->size(), 1000U);
}

TEST(SymbolEngineTest, RestartRebuildsIdenticalBookAndDeduplicatesCommands) {
  TempJournal journal;
  Snapshot before;
  std::uint64_t journal_sequence{};
  const auto buy = order_message("buy-1", trading::OrderSide::BUY, 100, 10);
  const auto sell = order_message("sell-1", trading::OrderSide::SELL, 100, 4);
  {
    SymbolEngine engine("BTCUSDT", journal.path(), 100);
    const auto first_reply = engine.submit(buy);
    EXPECT_EQ(first_reply, engine.submit(buy));
    static_cast<void>(engine.submit(sell));
    before = engine.snapshot();
    journal_sequence = engine.journal_sequence();
  }
  SymbolEngine recovered("BTCUSDT", journal.path(), 100);
  expect_equal(before, recovered.snapshot());
  EXPECT_EQ(journal_sequence, recovered.journal_sequence());
  const auto sequence = recovered.journal_sequence();
  static_cast<void>(recovered.submit(buy));
  EXPECT_EQ(sequence, recovered.journal_sequence());
}

TEST(SymbolEngineTest, CancellationIsJournaledAndIdempotentAcrossRestart) {
  TempJournal journal;
  const auto buy = order_message("buy-1", trading::OrderSide::BUY, 100, 10);
  const auto cancel = cancel_message("cancel-1", "buy-1");
  std::vector<std::uint8_t> first_ack;
  {
    SymbolEngine engine("BTCUSDT", journal.path(), 100);
    static_cast<void>(engine.submit(buy));
    first_ack = engine.control(cancel);
    EXPECT_EQ(first_ack, engine.control(cancel));
    EXPECT_TRUE(engine.snapshot().bids.empty());
  }
  SymbolEngine recovered("BTCUSDT", journal.path(), 100);
  EXPECT_EQ(first_ack, recovered.control(cancel));
  EXPECT_TRUE(recovered.snapshot().bids.empty());
}

TEST(SymbolEngineTest, RandomizedJournalReplaysOneThousandCommandsExactly) {
  TempJournal journal;
  Snapshot before;
  std::uint64_t sequence{};
  {
    SymbolEngine engine("BTCUSDT", journal.path(), 2000);
    std::mt19937_64 random(0x4c494d49414e4345ULL);
    for (std::uint64_t index = 0; index < 1000; ++index) {
      const auto side = (random() & 1U) == 0 ? trading::OrderSide::BUY : trading::OrderSide::SELL;
      const auto price = 95 + random() % 11;
      const auto quantity = 1 + random() % 100;
      static_cast<void>(engine.submit(order_message("order-" + std::to_string(index), side, price, quantity)));
    }
    before = engine.snapshot();
    sequence = engine.journal_sequence();
  }
  SymbolEngine recovered("BTCUSDT", journal.path(), 2000);
  expect_equal(before, recovered.snapshot());
  EXPECT_EQ(sequence, recovered.journal_sequence());
}

TEST(SymbolEngineTest, ReplayPublishesOnlyRequestedTradeRange) {
  TempJournal journal;
  SymbolEngine engine("BTCUSDT", journal.path(), 100);
  static_cast<void>(engine.submit(order_message("maker-1", trading::OrderSide::SELL, 100, 1)));
  static_cast<void>(engine.submit(order_message("taker-1", trading::OrderSide::BUY, 100, 1)));
  static_cast<void>(engine.submit(order_message("maker-2", trading::OrderSide::SELL, 100, 1)));
  static_cast<void>(engine.submit(order_message("taker-2", trading::OrderSide::BUY, 100, 1)));
  std::vector<PublishedEvent> replayed;
  engine.replay(2, [&](const PublishedEvent& event) { replayed.push_back(event); });
  ASSERT_EQ(replayed.size(), 1U);
  EXPECT_EQ(replayed.front().topic, "trade");
}

TEST(SymbolEngineTest, RecoveryCompletesACommandWhoseOutputsWereNotYetWritten) {
  TempJournal journal;
  const auto order = order_message("recovered", trading::OrderSide::BUY, 100, 5);
  {
    Journal raw(journal.path());
    raw.append(JournalKind::order_command, 1, order);
  }
  SymbolEngine recovered("BTCUSDT", journal.path(), 100);
  ASSERT_EQ(recovered.snapshot().bids.size(), 1U);
  EXPECT_EQ(recovered.snapshot().bids.front().quantity, 5U);
  EXPECT_GT(recovered.journal_sequence(), 1U);
  const auto sequence = recovered.journal_sequence();
  static_cast<void>(recovered.submit(order));
  EXPECT_EQ(recovered.journal_sequence(), sequence);
}

}  // namespace limiance::engine
