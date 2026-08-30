#include "limiance/engine/order_book.hpp"

#include <gtest/gtest.h>

using namespace limiance::engine;

namespace {
OrderCommand order(std::string id, Side side, std::uint64_t price, std::uint64_t quantity, TimeInForce tif=TimeInForce::gtc, OrderType type=OrderType::limit) {
  return OrderCommand{std::move(id), "user", side, type, tif, price, quantity, 100, 0, false};
}
}

TEST(OrderBookTest, BuyRestsAsBid) {
  OrderBook book(100); auto result=book.submit(order("buy",Side::buy,50000,1)); auto snapshot=book.snapshot(20);
  ASSERT_EQ(result.statuses.back().status,Status::open); ASSERT_EQ(snapshot.bids.size(),1); EXPECT_EQ(snapshot.bids[0].price,50000); EXPECT_EQ(snapshot.bids[0].quantity,1);
}

TEST(OrderBookTest, CrossingSellTradesAtMakerPrice) {
  OrderBook book(100); book.submit(order("buy",Side::buy,50000,1)); auto result=book.submit(order("sell",Side::sell,49999,1));
  ASSERT_EQ(result.trades.size(),1); EXPECT_EQ(result.trades[0].price,50000); EXPECT_EQ(result.trades[0].quantity,1); EXPECT_EQ(result.trades[0].maker_order_id,"buy");
}

TEST(OrderBookTest, HigherSellDoesNotCross) {
  OrderBook book(100); book.submit(order("buy",Side::buy,50000,1)); book.submit(order("sell",Side::sell,50001,1)); auto snapshot=book.snapshot(20);
  ASSERT_EQ(snapshot.bids.size(),1); ASSERT_EQ(snapshot.asks.size(),1); EXPECT_EQ(snapshot.asks[0].price,50001);
}

TEST(OrderBookTest, FIFOAtOnePrice) {
  OrderBook book(100); book.submit(order("A",Side::buy,50000,1)); book.submit(order("B",Side::buy,50000,1)); auto result=book.submit(order("sell",Side::sell,50000,1));
  ASSERT_EQ(result.trades.size(),1); EXPECT_EQ(result.trades[0].maker_order_id,"A"); auto snapshot=book.snapshot(20); ASSERT_EQ(snapshot.bids.size(),1); EXPECT_EQ(snapshot.bids[0].order_count,1);
}

TEST(OrderBookTest, PartialFillLeavesRemainder) {
  OrderBook book(100); book.submit(order("buy",Side::buy,50000,2)); auto result=book.submit(order("sell",Side::sell,50000,1)); auto snapshot=book.snapshot(20);
  ASSERT_EQ(result.trades.size(),1); ASSERT_EQ(snapshot.bids.size(),1); EXPECT_EQ(snapshot.bids[0].quantity,1); EXPECT_EQ(result.statuses.front().status,Status::partial);
}

TEST(OrderBookTest, CancelRemovesOrder) {
  OrderBook book(100); book.submit(order("buy",Side::buy,50000,1)); auto result=book.cancel("buy",200); EXPECT_EQ(result.statuses.back().status,Status::canceled); EXPECT_TRUE(book.snapshot(20).bids.empty());
}

TEST(OrderBookTest, CancelPartiallyFilledOrderPreservesTrade) {
  OrderBook book(100); book.submit(order("buy",Side::buy,50000,2)); book.submit(order("sell",Side::sell,50000,1)); auto result=book.cancel("buy",200);
  ASSERT_EQ(result.statuses.size(),1); EXPECT_EQ(result.statuses[0].filled_quantity,1); EXPECT_EQ(result.statuses[0].remaining_quantity,1); EXPECT_TRUE(book.snapshot(20).bids.empty());
}

TEST(OrderBookTest, MarketOrderWalksPriceLevels) {
  OrderBook book(100); book.submit(order("b1",Side::buy,50000,1)); book.submit(order("b2",Side::buy,49999,1)); book.submit(order("b3",Side::buy,49998,1)); auto result=book.submit(order("sell",Side::sell,0,3,TimeInForce::ioc,OrderType::market));
  ASSERT_EQ(result.trades.size(),3); EXPECT_EQ(result.trades[0].price,50000); EXPECT_EQ(result.trades[1].price,49999); EXPECT_EQ(result.trades[2].price,49998); EXPECT_TRUE(book.snapshot(20).bids.empty());
}

TEST(OrderBookTest, TradeSequencesAreStrictlyMonotonic) {
  OrderBook book(300); for(int index=0;index<100;++index)book.submit(order("b"+std::to_string(index),Side::buy,50000,1)); auto result=book.submit(order("sell",Side::sell,50000,100));
  ASSERT_EQ(result.trades.size(),100); for(std::size_t index=0;index<result.trades.size();++index)EXPECT_EQ(result.trades[index].sequence_id,index+1);
}

TEST(OrderBookTest, FOKAndIOCDoNotLeaveUnexpectedRemainders) {
  OrderBook book(100); book.submit(order("ask",Side::sell,50000,1)); auto fok=book.submit(order("fok",Side::buy,50000,2,TimeInForce::fok)); EXPECT_TRUE(fok.trades.empty()); EXPECT_EQ(fok.statuses.back().status,Status::rejected);
  auto ioc=book.submit(order("ioc",Side::buy,50000,2,TimeInForce::ioc)); ASSERT_EQ(ioc.trades.size(),1); EXPECT_EQ(ioc.statuses.back().status,Status::canceled); EXPECT_EQ(ioc.statuses.back().remaining_quantity,1); EXPECT_EQ(book.live_orders(),0);
}

TEST(OrderBookTest, CompletedSlotsAreReusedWithoutLosingIdempotency) {
  OrderBook book(2);
  for (std::uint64_t index = 0; index < 100; ++index) {
    const auto maker_id = "maker-" + std::to_string(index);
    const auto taker_id = "taker-" + std::to_string(index);
    EXPECT_FALSE(book.submit(order(maker_id, Side::sell, 100, 1)).statuses.empty());
    EXPECT_FALSE(book.submit(order(taker_id, Side::buy, 100, 1)).statuses.empty());
  }
  EXPECT_EQ(book.live_orders(), 0U);
  const auto duplicate = book.submit(order("maker-0", Side::sell, 100, 1));
  EXPECT_TRUE(duplicate.duplicate);
  EXPECT_TRUE(duplicate.statuses.empty());
}
