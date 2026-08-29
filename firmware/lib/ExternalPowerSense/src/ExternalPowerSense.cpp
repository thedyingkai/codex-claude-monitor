#include "ExternalPowerSense.h"

namespace quota_monitor {

bool DigitalPresenceFilter::update(std::uint32_t now_ms, bool raw_present) {
  if (!initialized_) {
    initialized_ = true;
    stable_present_ = raw_present;
    candidate_present_ = raw_present;
    candidate_since_ms_ = now_ms;
    return stable_present_;
  }

  if (raw_present != candidate_present_) {
    candidate_present_ = raw_present;
    candidate_since_ms_ = now_ms;
  }
  if (candidate_present_ != stable_present_ &&
      now_ms - candidate_since_ms_ >= debounce_ms_) {
    stable_present_ = candidate_present_;
  }
  return stable_present_;
}

void DigitalPresenceFilter::reset() {
  initialized_ = false;
  stable_present_ = false;
  candidate_present_ = false;
  candidate_since_ms_ = 0;
}

}  // namespace quota_monitor
