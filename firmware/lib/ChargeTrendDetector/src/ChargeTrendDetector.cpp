#include "ChargeTrendDetector.h"

#include <algorithm>

namespace quota_monitor {
namespace {

std::uint16_t median3(std::uint16_t a, std::uint16_t b, std::uint16_t c) {
  if (a > b) std::swap(a, b);
  if (b > c) std::swap(b, c);
  if (a > b) std::swap(a, b);
  return b;
}

}  // namespace

void ChargeTrendDetector::clear_samples() {
  sample_count_ = 0;
  charge_evidence_ = 0;
  unplug_evidence_ = 0;
  charge_peak_mv_ = 0;
  charge_candidate_level_mv_ = 0;
}

void ChargeTrendDetector::note_load_transition(
    std::uint32_t now_ms, std::uint8_t backlight_pwm) {
  if (have_backlight_pwm_ && last_backlight_pwm_ == backlight_pwm) return;
  if (!have_backlight_pwm_) {
    // The first observation establishes the load; it is not itself evidence
    // of a runtime load change. Starting a 45-second guard here would collide
    // with the default 60-second dim threshold before a trend can be formed.
    have_backlight_pwm_ = true;
    last_backlight_pwm_ = backlight_pwm;
    clear_samples();
    return;
  }
  have_backlight_pwm_ = true;
  last_backlight_pwm_ = backlight_pwm;
  have_load_transition_ = true;
  last_load_change_ms_ = now_ms;
  load_settle_ms_ =
      charging_ ? kChargingLoadSettleMs : kNormalLoadSettleMs;
  clear_samples();
  // Entering full brightness is an expected consequence of a positive charge
  // decision. Keep that decision latched while the battery voltage settles.
  state_ = charging_ ? ChargeTrendState::kCharging
                     : ChargeTrendState::kUnknown;
}

void ChargeTrendDetector::append_sample(std::uint32_t now_ms,
                                        std::uint16_t battery_mv) {
  if (sample_count_ == samples_.size()) {
    std::move(samples_.begin() + 1, samples_.end(), samples_.begin());
    --sample_count_;
  }
  samples_[sample_count_++] = {now_ms, battery_mv};
}

std::uint16_t ChargeTrendDetector::recent_median() const {
  if (sample_count_ < 3) return 0;
  return median3(samples_[sample_count_ - 3].millivolts,
                 samples_[sample_count_ - 2].millivolts,
                 samples_[sample_count_ - 1].millivolts);
}

bool ChargeTrendDetector::delta_over(std::uint32_t now_ms,
                                     std::uint32_t window_ms,
                                     std::int32_t& delta_mv) const {
  if (sample_count_ < 6) return false;

  // Find the newest three-sample group whose newest member is at least the
  // requested age. Reject very old anchors so a long sampling outage cannot
  // manufacture a trend on the first sample after recovery.
  std::size_t anchor = sample_count_;
  for (std::size_t i = sample_count_ - 3; i >= 2; --i) {
    const std::uint32_t age_ms = now_ms - samples_[i].at_ms;
    if (age_ms >= window_ms) {
      if (age_ms <= window_ms + 30000U) anchor = i;
      break;
    }
    if (i == 2) break;
  }
  if (anchor == sample_count_) return false;

  const std::uint16_t old_median =
      median3(samples_[anchor - 2].millivolts,
              samples_[anchor - 1].millivolts,
              samples_[anchor].millivolts);
  delta_mv = static_cast<std::int32_t>(recent_median()) -
             static_cast<std::int32_t>(old_median);
  return true;
}

bool ChargeTrendDetector::rising_is_consistent(std::uint32_t now_ms,
                                                std::uint32_t window_ms) const {
  std::size_t first = 0;
  while (first < sample_count_ &&
         now_ms - samples_[first].at_ms > window_ms)
    ++first;
  if (sample_count_ - first < 12) return false;

  std::size_t intervals = 0;
  std::size_t material_falls = 0;
  for (std::size_t i = first + 1; i < sample_count_; ++i) {
    ++intervals;
    if (static_cast<std::int32_t>(samples_[i].millivolts) + 8 <
        static_cast<std::int32_t>(samples_[i - 1].millivolts))
      ++material_falls;
  }
  // At least 70% of the steps must avoid a material fall.
  return intervals != 0 && material_falls * 10U <= intervals * 3U;
}

bool ChargeTrendDetector::update(std::uint32_t now_ms,
                                 std::uint16_t battery_mv,
                                 bool sample_valid,
                                 std::uint8_t backlight_pwm) {
  if (!have_backlight_pwm_ || backlight_pwm != last_backlight_pwm_)
    note_load_transition(now_ms, backlight_pwm);

  const bool valid = sample_valid && battery_mv >= kMinimumValidMv &&
                     battery_mv <= kMaximumValidMv;
  if (!valid) {
    if (!invalid_active_) {
      invalid_active_ = true;
      invalid_since_ms_ = now_ms;
    } else if (now_ms - invalid_since_ms_ >= kInvalidFailSafeMs) {
      charging_ = false;
      state_ = ChargeTrendState::kUnknown;
      clear_samples();
    }
    return charging_;
  }
  invalid_active_ = false;

  if (have_load_transition_ &&
      now_ms - last_load_change_ms_ < load_settle_ms_)
    return charging_;

  append_sample(now_ms, battery_mv);
  if (sample_count_ < 3) {
    state_ = charging_ ? ChargeTrendState::kCharging
                       : ChargeTrendState::kUnknown;
    return charging_;
  }

  const std::uint16_t recent = recent_median();
  if (charging_) {
    charge_peak_mv_ = std::max(charge_peak_mv_, recent);
    std::int32_t delta_30s = 0;
    const bool have_30s = delta_over(now_ms, 30000U, delta_30s);
    const bool falling =
        (have_30s && delta_30s <= -35) ||
        (charge_peak_mv_ >= static_cast<std::uint16_t>(recent + 45U));
    if (falling) {
      unplug_evidence_ =
          std::min<std::uint8_t>(3U, static_cast<std::uint8_t>(unplug_evidence_ + 1U));
      state_ = ChargeTrendState::kUnplugCandidate;
    } else {
      unplug_evidence_ = 0;
      state_ = ChargeTrendState::kCharging;
    }
    if (unplug_evidence_ >= 3U) {
      charging_ = false;
      state_ = ChargeTrendState::kBattery;
      clear_samples();
    }
    return charging_;
  }

  std::int32_t delta_30s = 0;
  std::int32_t delta_90s = 0;
  const bool fast_rise =
      delta_over(now_ms, 30000U, delta_30s) && delta_30s >= 35;
  const bool slow_rise = delta_over(now_ms, 90000U, delta_90s) &&
                         delta_90s >= 25 &&
                         rising_is_consistent(now_ms, 90000U);
  if (fast_rise || slow_rise) {
    if (charge_evidence_ == 0U) charge_candidate_level_mv_ = recent;
    charge_evidence_ =
        std::min<std::uint8_t>(3U, static_cast<std::uint8_t>(charge_evidence_ + 1U));
    state_ = ChargeTrendState::kChargeCandidate;
  } else if (charge_evidence_ != 0U &&
             static_cast<std::uint32_t>(recent) + 8U >=
                 charge_candidate_level_mv_) {
    // A charger insertion is often a durable voltage step rather than a
    // continuously steep slope. Once a conservative trend window has opened
    // the candidate, require the elevated level to persist for two more
    // samples instead of requiring the old baseline to remain in every
    // overlapping window.
    charge_evidence_ =
        std::min<std::uint8_t>(3U, static_cast<std::uint8_t>(charge_evidence_ + 1U));
    state_ = ChargeTrendState::kChargeCandidate;
  } else {
    charge_evidence_ = 0;
    charge_candidate_level_mv_ = 0;
    state_ = ChargeTrendState::kBattery;
  }

  if (charge_evidence_ >= 3U) {
    charging_ = true;
    state_ = ChargeTrendState::kCharging;
    charge_peak_mv_ = recent;
    unplug_evidence_ = 0;
  }
  return charging_;
}

}  // namespace quota_monitor
