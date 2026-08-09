#pragma once

#include <cstddef>
#include <cstdint>
#include <string>

namespace quota_monitor {

// A transport-independent response accumulator. append() is all-or-nothing:
// once the configured limit would be crossed, the buffer is marked overflowed
// and never grows again. The Arduino Stream adapter in main.cpp uses this to
// bound both Content-Length and chunked/connection-close HTTP responses before
// JSON parsing.
class BoundedResponseBuffer {
 public:
  explicit BoundedResponseBuffer(std::size_t limit) : limit_(limit) {}

  bool append(const std::uint8_t* data, std::size_t size) {
    if (overflowed_) return false;
    if (size == 0) return true;
    if (data == nullptr || value_.size() > limit_ ||
        size > limit_ - value_.size()) {
      overflowed_ = true;
      return false;
    }
    value_.append(reinterpret_cast<const char*>(data), size);
    return true;
  }

  bool append(std::uint8_t value) { return append(&value, 1); }

  bool overflowed() const { return overflowed_; }
  const std::string& value() const { return value_; }

 private:
  std::size_t limit_;
  std::string value_;
  bool overflowed_ = false;
};

}  // namespace quota_monitor
