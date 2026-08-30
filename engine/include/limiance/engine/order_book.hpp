#pragma once

#include <cstdint>
#include <deque>
#include <functional>
#include <map>
#include <optional>
#include <string>
#include <unordered_map>
#include <unordered_set>
#include <vector>

namespace limiance::engine {

enum class Side : std::uint8_t { buy, sell };
enum class OrderType : std::uint8_t { limit, market };
enum class TimeInForce : std::uint8_t { gtc, ioc, fok };
enum class Status : std::uint8_t { open, partial, filled, canceled, rejected };

struct OrderCommand {
  std::string id;
  std::string user_id;
  Side side{};
  OrderType type{};
  TimeInForce time_in_force{};
  std::uint64_t price{};
  std::uint64_t quantity{};
  std::uint64_t timestamp_ns{};
  std::uint8_t fee_tier{};
  bool post_only{};
};

struct Trade {
  std::uint64_t sequence_id{};
  std::uint64_t timestamp_ns{};
  std::string maker_order_id;
  std::string taker_order_id;
  std::string maker_user_id;
  std::string taker_user_id;
  std::uint64_t price{};
  std::uint64_t quantity{};
  std::int16_t maker_fee_bps{};
  std::int16_t taker_fee_bps{};
};

struct OrderStatus {
  std::uint64_t sequence_id{};
  std::string order_id;
  Status status{};
  std::uint64_t filled_quantity{};
  std::uint64_t remaining_quantity{};
  std::uint64_t average_price{};
};

struct Result {
  std::vector<Trade> trades;
  std::vector<OrderStatus> statuses;
  bool duplicate{};
};

struct Level { std::uint64_t price{}, quantity{}; std::uint32_t order_count{}; };
struct Snapshot { std::uint64_t sequence_id{}; std::vector<Level> bids, asks; };

struct FeeRates { std::int16_t maker_bps{}, taker_bps{}; };
using FeeResolver = std::function<FeeRates(std::uint8_t)>;

class OrderBook final {
 public:
  explicit OrderBook(std::size_t capacity, FeeResolver fees = {});
  Result submit(const OrderCommand& command);
  Result cancel(const std::string& order_id, std::uint64_t timestamp_ns);
  [[nodiscard]] Snapshot snapshot(std::size_t depth) const;
  [[nodiscard]] std::size_t live_orders() const noexcept { return live_orders_; }
  [[nodiscard]] std::uint64_t trade_sequence() const noexcept { return trade_sequence_; }
  [[nodiscard]] std::uint64_t status_sequence() const noexcept { return status_sequence_; }
  [[nodiscard]] std::uint64_t book_sequence() const noexcept { return book_sequence_; }

 private:
  struct Order {
    OrderCommand command;
    std::uint64_t remaining{};
    unsigned __int128 filled_notional{};
    Status status{Status::open};
  };
  using BidLevels = std::map<std::uint64_t, std::deque<Order*>, std::greater<>>;
  using AskLevels = std::map<std::uint64_t, std::deque<Order*>>;

  [[nodiscard]] bool valid(const OrderCommand&) const;
  [[nodiscard]] bool crosses(const OrderCommand&) const;
  [[nodiscard]] std::uint64_t executable_quantity(const OrderCommand&) const;
  void match(Order&, Result&);
  [[nodiscard]] bool rest(Order&);
  void remove(Order&);
  [[nodiscard]] Order& acquire(const OrderCommand&);
  void release(Order&);
  void emit_status(Order&, Status, Result&);
  void emit_trade(Order& maker, Order& taker, std::uint64_t price, std::uint64_t quantity, Result&);
  [[nodiscard]] std::uint64_t average_price(const Order&) const;

  std::size_t capacity_{};
  std::vector<Order> storage_;
  std::vector<std::size_t> free_slots_;
  std::unordered_map<std::string, Order*> by_id_;
  std::unordered_set<std::string> seen_ids_;
  BidLevels bids_;
  AskLevels asks_;
  FeeResolver fees_;
  std::size_t live_orders_{};
  std::uint64_t trade_sequence_{}, status_sequence_{}, book_sequence_{};
};

}  // namespace limiance::engine
