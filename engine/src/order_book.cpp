#include "limiance/engine/order_book.hpp"

#include <algorithm>
#include <limits>

namespace limiance::engine {
namespace {
FeeRates default_fees(std::uint8_t tier) {
  static constexpr FeeRates rates[]{{10,10},{8,10},{6,9},{4,8},{2,7},{0,6}};
  return tier < std::size(rates) ? rates[tier] : rates[0];
}
}

OrderBook::OrderBook(std::size_t capacity, FeeResolver fees) : capacity_(capacity), storage_(capacity), fees_(std::move(fees)) {
  free_slots_.reserve(capacity_);
  for (std::size_t index = capacity_; index > 0; --index) free_slots_.push_back(index - 1);
  by_id_.reserve(capacity_);
  seen_ids_.reserve(capacity_);
  if (!fees_) fees_ = default_fees;
}

bool OrderBook::valid(const OrderCommand& command) const {
  return !command.id.empty() && !command.user_id.empty() && command.quantity > 0 &&
         (command.type == OrderType::market || command.price > 0) &&
         !(command.type == OrderType::market && command.time_in_force == TimeInForce::gtc) &&
         !(command.type == OrderType::market && command.post_only);
}

bool OrderBook::crosses(const OrderCommand& command) const {
  if (command.side == Side::buy) return !asks_.empty() && (command.type == OrderType::market || command.price >= asks_.begin()->first);
  return !bids_.empty() && (command.type == OrderType::market || command.price <= bids_.begin()->first);
}

std::uint64_t OrderBook::executable_quantity(const OrderCommand& command) const {
  std::uint64_t available{};
  auto add = [&](std::uint64_t quantity) {
    if (quantity > std::numeric_limits<std::uint64_t>::max() - available) available = std::numeric_limits<std::uint64_t>::max();
    else available += quantity;
  };
  if (command.side == Side::buy) {
    for (const auto& [price, orders] : asks_) {
      if (command.type == OrderType::limit && price > command.price) break;
      for (const auto* order : orders) add(order->remaining);
      if (available >= command.quantity) break;
    }
  } else {
    for (const auto& [price, orders] : bids_) {
      if (command.type == OrderType::limit && price < command.price) break;
      for (const auto* order : orders) add(order->remaining);
      if (available >= command.quantity) break;
    }
  }
  return available;
}

Result OrderBook::submit(const OrderCommand& command) {
  Result result;
  if (!command.id.empty() && seen_ids_.contains(command.id)) { result.duplicate = true; return result; }
  if (!command.id.empty()) seen_ids_.insert(command.id);
  if (!valid(command) || free_slots_.empty()) {
    Order rejected{command, command.quantity}; emit_status(rejected, Status::rejected, result); return result;
  }
  if (command.post_only && crosses(command)) {
    Order rejected{command, command.quantity}; emit_status(rejected, Status::rejected, result); return result;
  }
  if (command.time_in_force == TimeInForce::fok && executable_quantity(command) < command.quantity) {
    Order rejected{command, command.quantity}; emit_status(rejected, Status::rejected, result); return result;
  }
  Order& incoming = acquire(command);
  by_id_.emplace(command.id, &incoming);
  ++live_orders_;
  match(incoming, result);
  if (incoming.remaining == 0) { emit_status(incoming, Status::filled, result); --live_orders_; release(incoming); }
  else if (command.time_in_force == TimeInForce::gtc && rest(incoming)) {
    emit_status(incoming, incoming.remaining == command.quantity ? Status::open : Status::partial, result);
  } else {
    emit_status(incoming, incoming.remaining == command.quantity ? Status::rejected : Status::canceled, result);
    --live_orders_;
    release(incoming);
  }
  return result;
}

void OrderBook::match(Order& incoming, Result& result) {
  auto consume = [&](auto& levels) {
    while (incoming.remaining && !levels.empty()) {
      auto level = levels.begin();
      if (incoming.command.type == OrderType::limit) {
        if (incoming.command.side == Side::buy && level->first > incoming.command.price) break;
        if (incoming.command.side == Side::sell && level->first < incoming.command.price) break;
      }
      auto& queue = level->second;
      while (incoming.remaining && !queue.empty()) {
        Order& maker = *queue.front();
        const auto quantity = std::min(incoming.remaining, maker.remaining);
        maker.remaining -= quantity; incoming.remaining -= quantity;
        maker.filled_notional += static_cast<unsigned __int128>(level->first) * quantity;
        incoming.filled_notional += static_cast<unsigned __int128>(level->first) * quantity;
        emit_trade(maker, incoming, level->first, quantity, result);
        ++book_sequence_;
        if (!maker.remaining) { queue.pop_front(); emit_status(maker, Status::filled, result); --live_orders_; release(maker); }
        else emit_status(maker, Status::partial, result);
      }
      if (queue.empty()) levels.erase(level);
    }
  };
  if (incoming.command.side == Side::buy) consume(asks_); else consume(bids_);
}

bool OrderBook::rest(Order& order) {
  std::uint64_t total{};
  const auto accumulate = [&](const auto& levels) {
    const auto found = levels.find(order.command.price);
    if (found == levels.end()) return true;
    for (const auto* existing : found->second) {
      if (existing->remaining > std::numeric_limits<std::uint64_t>::max() - total) return false;
      total += existing->remaining;
    }
    return order.remaining <= std::numeric_limits<std::uint64_t>::max() - total;
  };
  if (order.command.side == Side::buy ? !accumulate(bids_) : !accumulate(asks_)) return false;
  if (order.command.side == Side::buy) bids_[order.command.price].push_back(&order);
  else asks_[order.command.price].push_back(&order);
  ++book_sequence_;
  return true;
}

OrderBook::Order& OrderBook::acquire(const OrderCommand& command) {
  const auto index = free_slots_.back();
  free_slots_.pop_back();
  storage_[index] = Order{command, command.quantity};
  return storage_[index];
}

void OrderBook::release(Order& order) {
  by_id_.erase(order.command.id);
  free_slots_.push_back(static_cast<std::size_t>(&order - storage_.data()));
}

void OrderBook::remove(Order& order) {
  auto erase = [&](auto& levels) {
    auto level = levels.find(order.command.price); if (level == levels.end()) return;
    auto& queue = level->second; queue.erase(std::remove(queue.begin(), queue.end(), &order), queue.end());
    if (queue.empty()) levels.erase(level);
  };
  if (order.command.side == Side::buy) erase(bids_); else erase(asks_);
}

Result OrderBook::cancel(const std::string& order_id, std::uint64_t) {
  Result result;
  auto found = by_id_.find(order_id);
  if (found == by_id_.end() || found->second->remaining == 0 || found->second->status == Status::canceled || found->second->status == Status::rejected) return result;
  Order& order = *found->second; remove(order); ++book_sequence_; --live_orders_; emit_status(order, Status::canceled, result); release(order); return result;
}

void OrderBook::emit_trade(Order& maker, Order& taker, std::uint64_t price, std::uint64_t quantity, Result& result) {
  const auto maker_fees = fees_(maker.command.fee_tier); const auto taker_fees = fees_(taker.command.fee_tier);
  result.trades.push_back(Trade{++trade_sequence_, taker.command.timestamp_ns, maker.command.id, taker.command.id, maker.command.user_id, taker.command.user_id, price, quantity, maker_fees.maker_bps, taker_fees.taker_bps});
}

std::uint64_t OrderBook::average_price(const Order& order) const {
  const auto filled = order.command.quantity - order.remaining; return filled ? static_cast<std::uint64_t>(order.filled_notional / filled) : 0;
}

void OrderBook::emit_status(Order& order, Status status, Result& result) {
  order.status = status;
  result.statuses.push_back(OrderStatus{++status_sequence_, order.command.id, status, order.command.quantity-order.remaining, order.remaining, average_price(order)});
}

Snapshot OrderBook::snapshot(std::size_t depth) const {
  Snapshot result; result.sequence_id = book_sequence_;
  auto collect = [depth](const auto& levels, auto& destination) {
    for (const auto& [price, orders] : levels) {
      if (destination.size() == depth) break;
      Level level{price,0,0}; for (const auto* order : orders) { level.quantity += order->remaining; ++level.order_count; }
      if (level.quantity) destination.push_back(level);
    }
  };
  collect(bids_,result.bids); collect(asks_,result.asks); return result;
}

}  // namespace limiance::engine
