#pragma once

#include <cstdint>

namespace quota_monitor {

enum class DisplayState : std::uint8_t {
  kAwake,
  kDimmed,
  kBacklightOff,
  kPortal,
  kOta,
};

struct DisplayPolicy {
  std::uint32_t dim_after_seconds = 60;
  std::uint32_t off_after_seconds = 300;
};

// Wrap-safe runtime policy. All configured intervals are deliberately limited
// to less than 2^31 milliseconds by the configuration validator.
class DisplayStateMachine {
 public:
  void configure(DisplayPolicy policy);
  void begin(std::uint32_t now_ms);
  void note_activity(std::uint32_t now_ms);
  void enter_portal(std::uint32_t now_ms);
  void enter_ota(std::uint32_t now_ms);
  void leave_forced_mode(std::uint32_t now_ms);
  DisplayState update(std::uint32_t now_ms);

  DisplayState state() const { return state_; }
  std::uint32_t idle_milliseconds(std::uint32_t now_ms) const {
    return now_ms - last_activity_ms_;
  }

 private:
  DisplayPolicy policy_{};
  DisplayState state_ = DisplayState::kAwake;
  std::uint32_t last_activity_ms_ = 0;
};

// Coalesces repeated manual refreshes and imposes a one-second admission
// interval without losing a refresh requested while a request is in flight.
class RefreshGate {
 public:
  explicit RefreshGate(std::uint32_t cooldown_ms = 1000)
      : cooldown_ms_(cooldown_ms) {}

  void request(std::uint32_t now_ms);
  bool take_if_ready(std::uint32_t now_ms, bool in_flight);
  bool pending() const { return pending_; }

 private:
  std::uint32_t cooldown_ms_;
  std::uint32_t last_started_ms_ = 0;
  bool have_started_ = false;
  bool pending_ = false;
};

std::uint32_t screen_off_refresh_seconds(std::uint32_t normal_seconds,
                                         std::uint32_t configured_seconds);

}  // namespace quota_monitor
