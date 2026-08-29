#pragma once

#include <cstdint>

namespace quota_monitor {

// Debounces an externally pulled digital USB/VBUS-presence signal. Time
// arithmetic is uint32_t wrap-safe as long as debounce_ms is below 2^31.
class DigitalPresenceFilter {
 public:
  explicit DigitalPresenceFilter(std::uint32_t debounce_ms = 300U)
      : debounce_ms_(debounce_ms) {}

  bool update(std::uint32_t now_ms, bool raw_present);
  void reset();
  bool initialized() const { return initialized_; }
  bool present() const { return stable_present_; }

 private:
  std::uint32_t debounce_ms_;
  std::uint32_t candidate_since_ms_ = 0;
  bool initialized_ = false;
  bool stable_present_ = false;
  bool candidate_present_ = false;
};

}  // namespace quota_monitor
