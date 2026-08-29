#include "RuntimePolicy.h"

#include <algorithm>

namespace quota_monitor {

namespace {

std::uint32_t seconds_to_ms(std::uint32_t seconds) {
  return seconds * 1000U;
}

}  // namespace

void DisplayStateMachine::configure(DisplayPolicy policy) { policy_ = policy; }

void DisplayStateMachine::begin(std::uint32_t now_ms) {
  last_activity_ms_ = now_ms;
  state_ = DisplayState::kAwake;
}

void DisplayStateMachine::note_activity(std::uint32_t now_ms) {
  last_activity_ms_ = now_ms;
  if (state_ != DisplayState::kPortal && state_ != DisplayState::kOta)
    state_ = DisplayState::kAwake;
}

void DisplayStateMachine::enter_portal(std::uint32_t now_ms) {
  last_activity_ms_ = now_ms;
  state_ = DisplayState::kPortal;
}

void DisplayStateMachine::enter_ota(std::uint32_t now_ms) {
  last_activity_ms_ = now_ms;
  state_ = DisplayState::kOta;
}

void DisplayStateMachine::leave_forced_mode(std::uint32_t now_ms) {
  last_activity_ms_ = now_ms;
  state_ = DisplayState::kAwake;
}

DisplayState DisplayStateMachine::update(std::uint32_t now_ms) {
  return update(now_ms, external_power_present_);
}

DisplayState DisplayStateMachine::update(std::uint32_t now_ms,
                                         bool external_power_present) {
  if (external_power_present != external_power_present_) {
    external_power_present_ = external_power_present;
    // Inserting or removing external power starts a new inactivity period. In
    // particular, unplugging does not inherit the time spent on external
    // power, so dim/off thresholds start at zero on that edge.
    last_activity_ms_ = now_ms;
  }

  if (state_ == DisplayState::kPortal || state_ == DisplayState::kOta)
    return state_;

  // While externally powered there is no reason to apply battery-saving
  // states. Forced modes above retain priority and their distinct state.
  if (external_power_present_) {
    state_ = DisplayState::kAwake;
    return state_;
  }

  const std::uint32_t idle_ms = now_ms - last_activity_ms_;
  if (policy_.off_after_seconds != 0 &&
      idle_ms >= seconds_to_ms(policy_.off_after_seconds)) {
    state_ = DisplayState::kBacklightOff;
  } else if (policy_.dim_after_seconds != 0 &&
             idle_ms >= seconds_to_ms(policy_.dim_after_seconds)) {
    state_ = DisplayState::kDimmed;
  } else {
    state_ = DisplayState::kAwake;
  }
  return state_;
}

void RefreshGate::request(std::uint32_t) { pending_ = true; }

bool RefreshGate::take_if_ready(std::uint32_t now_ms, bool in_flight) {
  if (!pending_ || in_flight) return false;
  if (have_started_ && now_ms - last_started_ms_ < cooldown_ms_) return false;
  pending_ = false;
  have_started_ = true;
  last_started_ms_ = now_ms;
  return true;
}

std::uint32_t screen_off_refresh_seconds(std::uint32_t normal_seconds,
                                         std::uint32_t configured_seconds) {
  return std::max<std::uint32_t>(60U,
                                 std::max(normal_seconds, configured_seconds));
}

}  // namespace quota_monitor
