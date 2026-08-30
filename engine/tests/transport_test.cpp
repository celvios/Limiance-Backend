#ifndef _WIN32

#include <chrono>
#include <csignal>
#include <cstdlib>
#include <filesystem>
#include <string>
#include <thread>
#include <vector>

#include <gtest/gtest.h>
#include <sys/types.h>
#include <sys/wait.h>
#include <unistd.h>
#include <zmq.h>

#include "flatbuffers/flatbuffers.h"
#include "trading_generated.h"

namespace {
using Bytes = std::vector<std::uint8_t>;

class EngineProcess final {
 public:
  EngineProcess(std::string order_endpoint, std::string control_endpoint,
                std::string event_endpoint, std::filesystem::path journal_dir)
      : order_(std::move(order_endpoint)), control_(std::move(control_endpoint)),
        event_(std::move(event_endpoint)), journal_(std::move(journal_dir)) { start(); }
  ~EngineProcess() { stop(); }
  void stop() {
    if (pid_ <= 0) return;
    kill(pid_, SIGTERM);
    waitpid(pid_, nullptr, 0);
    pid_ = -1;
  }
 private:
  void start() {
    pid_ = fork();
    if (pid_ < 0) throw std::runtime_error("fork matching engine failed");
    if (pid_ == 0) {
      setenv("ENGINE_ORDER_BIND", order_.c_str(), 1);
      setenv("ENGINE_CONTROL_BIND", control_.c_str(), 1);
      setenv("ENGINE_EVENT_BIND", event_.c_str(), 1);
      setenv("ENGINE_JOURNAL_DIR", journal_.c_str(), 1);
      execl("./matching_engine", "matching_engine", static_cast<char*>(nullptr));
      _exit(127);
    }
    std::this_thread::sleep_for(std::chrono::milliseconds(250));
  }
  std::string order_, control_, event_;
  std::filesystem::path journal_;
  pid_t pid_{-1};
};

Bytes order_message(const std::string& id) {
  flatbuffers::FlatBufferBuilder builder(256);
  const auto root = limiance::trading::CreateOrderIngressDirect(
      builder, id.c_str(), "user", "BTCUSDT", limiance::trading::OrderSide::BUY,
      limiance::trading::OrderType::LIMIT, 50000, 1, limiance::trading::TimeInForce::GTC,
      false, false, 100, 0);
  limiance::trading::FinishOrderIngressBuffer(builder, root);
  return {builder.GetBufferPointer(), builder.GetBufferPointer() + builder.GetSize()};
}

Bytes request(void* socket, const Bytes& payload) {
  if (zmq_send(socket, payload.data(), payload.size(), 0) < 0) throw std::runtime_error(zmq_strerror(zmq_errno()));
  zmq_msg_t reply;
  zmq_msg_init(&reply);
  const auto size = zmq_msg_recv(&reply, socket, 0);
  if (size < 0) { zmq_msg_close(&reply); throw std::runtime_error(zmq_strerror(zmq_errno())); }
  const auto* begin = static_cast<const std::uint8_t*>(zmq_msg_data(&reply));
  Bytes result(begin, begin + static_cast<std::size_t>(size));
  zmq_msg_close(&reply);
  return result;
}
}  // namespace

TEST(TransportTest, RequestSocketReconnectsAfterEngineRestart) {
  const auto suffix = std::to_string(getpid());
  const auto order_endpoint = "ipc:///dev/shm/limiance-order-" + suffix;
  const auto control_endpoint = "ipc:///dev/shm/limiance-control-" + suffix;
  const auto event_endpoint = "ipc:///dev/shm/limiance-event-" + suffix;
  const auto journal_dir = std::filesystem::path("/dev/shm") / ("limiance-journal-" + suffix);
  std::filesystem::create_directories(journal_dir);

  void* context = zmq_ctx_new();
  ASSERT_NE(context, nullptr);
  void* socket = zmq_socket(context, ZMQ_REQ);
  ASSERT_NE(socket, nullptr);
  int timeout = 5000;
  ASSERT_EQ(zmq_setsockopt(socket, ZMQ_RCVTIMEO, &timeout, sizeof(timeout)), 0);
  ASSERT_EQ(zmq_connect(socket, order_endpoint.c_str()), 0);

  {
    EngineProcess first(order_endpoint, control_endpoint, event_endpoint, journal_dir);
    const auto reply = request(socket, order_message("order-1"));
    flatbuffers::Verifier verifier(reply.data(), reply.size());
    ASSERT_TRUE(verifier.VerifyBuffer<limiance::trading::OrderStatusEvent>(nullptr));
    EXPECT_EQ(flatbuffers::GetRoot<limiance::trading::OrderStatusEvent>(reply.data())->status(),
              limiance::trading::OrderStatus::OPEN);
  }
  std::this_thread::sleep_for(std::chrono::milliseconds(250));
  {
    EngineProcess second(order_endpoint, control_endpoint, event_endpoint, journal_dir);
    const auto duplicate_reply = request(socket, order_message("order-1"));
    flatbuffers::Verifier verifier(duplicate_reply.data(), duplicate_reply.size());
    ASSERT_TRUE(verifier.VerifyBuffer<limiance::trading::OrderStatusEvent>(nullptr));
    EXPECT_STREQ(flatbuffers::GetRoot<limiance::trading::OrderStatusEvent>(duplicate_reply.data())->order_id()->c_str(), "order-1");
  }

  zmq_close(socket);
  zmq_ctx_term(context);
  std::error_code ignored;
  std::filesystem::remove_all(journal_dir, ignored);
}

#endif
