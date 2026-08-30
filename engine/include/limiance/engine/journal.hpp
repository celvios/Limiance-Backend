#pragma once

#include <cstdint>
#include <filesystem>
#include <span>
#include <stdexcept>
#include <vector>

namespace limiance::engine {

enum class JournalKind : std::uint16_t {
  order_command=1, cancel_command=2, trade_event=3, status_event=4,
  book_snapshot=5, control_ack=6
};
struct JournalRecord { JournalKind kind{}; std::uint64_t sequence{}; std::vector<std::uint8_t> payload; };

class JournalError : public std::runtime_error { public: using std::runtime_error::runtime_error; };

class Journal final {
 public:
  explicit Journal(std::filesystem::path path);
  ~Journal();
  Journal(const Journal&)=delete;
  Journal& operator=(const Journal&)=delete;
  void append(JournalKind kind,std::uint64_t sequence,std::span<const std::uint8_t> payload);
  [[nodiscard]] std::vector<JournalRecord> recover() const;
 private:
  std::filesystem::path path_;
  int descriptor_{-1};
};

}  // namespace limiance::engine
