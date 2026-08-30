#include <atomic>
#include <condition_variable>
#include <csignal>
#include <cctype>
#include <cstdlib>
#include <deque>
#include <filesystem>
#include <future>
#include <iostream>
#include <memory>
#include <mutex>
#include <stdexcept>
#include <string>
#include <thread>
#include <unordered_map>
#include <utility>
#include <vector>

#include <zmq.h>

#include "limiance/engine/protocol.hpp"
#include "limiance/engine/symbol_engine.hpp"

namespace {
using Bytes = std::vector<std::uint8_t>;
using limiance::engine::PublishedEvent;

std::atomic_bool running{true};
void stop(int) { running.store(false); }

std::string setting(const char* name, const char* fallback) {
  const char* value = std::getenv(name);
  return value != nullptr && *value != '\0' ? value : fallback;
}

std::size_t capacity_setting() {
  const auto value = setting("ENGINE_ORDER_CAPACITY", "1000000");
  const auto parsed = std::stoull(value);
  if (parsed == 0) throw std::invalid_argument("ENGINE_ORDER_CAPACITY must be positive");
  return static_cast<std::size_t>(parsed);
}

std::string safe_pair(const std::string& pair) {
  if (pair.empty()) throw std::invalid_argument("pair is required");
  for (const unsigned char character : pair) {
    if (!(std::isalnum(character) || character == '-' || character == '_')) {
      throw std::invalid_argument("pair contains unsafe journal characters");
    }
  }
  return pair;
}

void check(int result, const char* action) {
  if (result < 0) throw std::runtime_error(std::string(action) + ": " + zmq_strerror(zmq_errno()));
}

class EventPublisher final {
 public:
  EventPublisher(void* context, const std::string& endpoint) : context_(context), endpoint_(endpoint), thread_([this] { run(); }) {
    ready_.get_future().get();
  }
  ~EventPublisher() {
    { std::lock_guard lock(mutex_); stopping_ = true; }
    condition_.notify_one();
  }
  EventPublisher(const EventPublisher&) = delete;
  EventPublisher& operator=(const EventPublisher&) = delete;

  void enqueue(const PublishedEvent& event) {
    { std::lock_guard lock(mutex_); queue_.push_back(event); }
    condition_.notify_one();
  }

 private:
  void run() {
    void* socket = zmq_socket(context_, ZMQ_PUB);
    if (socket == nullptr) {
      ready_.set_exception(std::make_exception_ptr(std::runtime_error("create publisher socket failed")));
      return;
    }
    if (zmq_bind(socket, endpoint_.c_str()) < 0) {
      ready_.set_exception(std::make_exception_ptr(
          std::runtime_error(std::string("bind publisher: ") + zmq_strerror(zmq_errno()))));
      zmq_close(socket);
      return;
    }
    ready_.set_value();
    while (true) {
      PublishedEvent event;
      {
        std::unique_lock lock(mutex_);
        condition_.wait(lock, [this] { return stopping_ || !queue_.empty(); });
        if (stopping_ && queue_.empty()) break;
        event = std::move(queue_.front());
        queue_.pop_front();
      }
      if (zmq_send(socket, event.topic.data(), event.topic.size(), ZMQ_SNDMORE) < 0 ||
          zmq_send(socket, event.payload.data(), event.payload.size(), 0) < 0) {
        std::cerr << "publish event: " << zmq_strerror(zmq_errno()) << '\n';
      }
    }
    zmq_close(socket);
  }

  void* context_;
  std::string endpoint_;
  std::mutex mutex_;
  std::condition_variable condition_;
  std::deque<PublishedEvent> queue_;
  bool stopping_{};
  std::promise<void> ready_;
  std::jthread thread_;
};

class PartitionWorker final {
 public:
  PartitionWorker(std::string pair, const std::filesystem::path& journal_dir, std::size_t capacity,
                  EventPublisher& publisher)
      : engine_(pair, journal_dir / (safe_pair(pair) + ".journal"), capacity), publisher_(publisher),
        thread_([this] { run(); }) {}
  ~PartitionWorker() {
    { std::lock_guard lock(mutex_); stopping_ = true; }
    condition_.notify_one();
  }
  PartitionWorker(const PartitionWorker&) = delete;
  PartitionWorker& operator=(const PartitionWorker&) = delete;

  Bytes submit(Bytes payload, bool control) {
    Job job{std::move(payload), control, {}};
    auto future = job.reply.get_future();
    { std::lock_guard lock(mutex_); queue_.push_back(std::move(job)); }
    condition_.notify_one();
    return future.get();
  }

 private:
  struct Job { Bytes payload; bool control{}; std::promise<Bytes> reply; };
  void run() {
    while (true) {
      Job job;
      {
        std::unique_lock lock(mutex_);
        condition_.wait(lock, [this] { return stopping_ || !queue_.empty(); });
        if (stopping_ && queue_.empty()) break;
        job = std::move(queue_.front());
        queue_.pop_front();
      }
      try {
        const auto publish = [this](const PublishedEvent& event) { publisher_.enqueue(event); };
        job.reply.set_value(job.control ? engine_.control(job.payload, publish) : engine_.submit(job.payload, publish));
      } catch (...) { job.reply.set_exception(std::current_exception()); }
    }
  }

  limiance::engine::SymbolEngine engine_;
  EventPublisher& publisher_;
  std::mutex mutex_;
  std::condition_variable condition_;
  std::deque<Job> queue_;
  bool stopping_{};
  std::jthread thread_;
};

class PartitionRouter final {
 public:
  PartitionRouter(std::filesystem::path journal_dir, std::size_t capacity, EventPublisher& publisher)
      : journal_dir_(std::move(journal_dir)), capacity_(capacity), publisher_(publisher) {
    std::filesystem::create_directories(journal_dir_);
  }

  Bytes order(Bytes payload) {
    const auto decoded = limiance::engine::decode_order(payload);
    return worker(decoded.pair).submit(std::move(payload), false);
  }
  Bytes control(Bytes payload) {
    const auto decoded = limiance::engine::decode_control(payload);
    return worker(decoded.pair).submit(std::move(payload), true);
  }

 private:
  PartitionWorker& worker(const std::string& pair) {
    const auto safe = safe_pair(pair);
    auto found = workers_.find(safe);
    if (found == workers_.end()) {
      found = workers_.emplace(safe, std::make_unique<PartitionWorker>(safe, journal_dir_, capacity_, publisher_)).first;
    }
    return *found->second;
  }

  std::filesystem::path journal_dir_;
  std::size_t capacity_;
  EventPublisher& publisher_;
  std::unordered_map<std::string, std::unique_ptr<PartitionWorker>> workers_;
};

Bytes receive(void* socket) {
  zmq_msg_t message;
  check(zmq_msg_init(&message), "initialize message");
  const auto size = zmq_msg_recv(&message, socket, 0);
  if (size < 0) { zmq_msg_close(&message); check(size, "receive request"); }
  const auto* begin = static_cast<const std::uint8_t*>(zmq_msg_data(&message));
  Bytes result(begin, begin + static_cast<std::size_t>(size));
  zmq_msg_close(&message);
  return result;
}

void send(void* socket, const Bytes& payload) {
  check(zmq_send(socket, payload.data(), payload.size(), 0), "send reply");
}

void bind(void* socket, const std::string& endpoint) { check(zmq_bind(socket, endpoint.c_str()), "bind socket"); }
}  // namespace

int main() {
  try {
    std::signal(SIGINT, stop);
    std::signal(SIGTERM, stop);
    void* context = zmq_ctx_new();
    if (context == nullptr) throw std::runtime_error("create ZeroMQ context failed");
    void* orders = zmq_socket(context, ZMQ_REP);
    void* controls = zmq_socket(context, ZMQ_REP);
    if (orders == nullptr || controls == nullptr) throw std::runtime_error("create ZeroMQ socket failed");
    const auto order_endpoint = setting("ENGINE_ORDER_BIND", "tcp://*:5555");
    const auto control_endpoint = setting("ENGINE_CONTROL_BIND", "tcp://*:5556");
    bind(orders, order_endpoint);
    bind(controls, control_endpoint);
    {
      EventPublisher publisher(context, setting("ENGINE_EVENT_BIND", "tcp://*:5557"));
      PartitionRouter router(setting("ENGINE_JOURNAL_DIR", "/var/lib/limiance-engine"), capacity_setting(), publisher);
      std::cout << "matching engine ready on " << order_endpoint << " and " << control_endpoint << '\n';
      while (running.load()) {
        zmq_pollitem_t items[]{{orders, 0, ZMQ_POLLIN, 0}, {controls, 0, ZMQ_POLLIN, 0}};
        const auto ready = zmq_poll(items, 2, 100);
        if (ready < 0) { if (zmq_errno() == EINTR) continue; check(ready, "poll sockets"); }
        if ((items[0].revents & ZMQ_POLLIN) != 0) {
          try { send(orders, router.order(receive(orders))); }
          catch (const std::exception& error) {
            send(orders, limiance::engine::encode_status({1, "invalid", limiance::engine::Status::rejected, 0, 0, 0}));
            std::cerr << "reject order: " << error.what() << '\n';
          }
        }
        if ((items[1].revents & ZMQ_POLLIN) != 0) {
          try { send(controls, router.control(receive(controls))); }
          catch (const std::exception& error) {
            send(controls, limiance::engine::encode_control_ack("invalid", 0, false, 0, error.what()));
            std::cerr << "reject control: " << error.what() << '\n';
          }
        }
      }
    }
    zmq_close(controls);
    zmq_close(orders);
    zmq_ctx_shutdown(context);
    zmq_ctx_term(context);
    return 0;
  } catch (const std::exception& error) {
    std::cerr << "matching engine fatal: " << error.what() << '\n';
    return 1;
  }
}
