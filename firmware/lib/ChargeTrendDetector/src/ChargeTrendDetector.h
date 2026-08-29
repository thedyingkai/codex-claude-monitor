#pragma once

#include <array>
#include <cstddef>
#include <cstdint>

namespace quota_monitor {

enum class ChargeTrendState : std::uint8_t {
  kUnknown,
  kBattery,
  kChargeCandidate,
  kCharging,
  kUnplugCandidate,
};

// Best-effort charger inference for boards that expose battery voltage but no
// USB/VBUS signal. This deliberately favours false negatives over false
// positives: a stable voltage alone never proves that a charger is attached.
//
// Feed one already-filtered battery sample about every five seconds. The
// detector rejects samples around backlight load changes so the normal LiPo
// rebound after dimming or switching the backlight off cannot look like a
// charger insertion.
class ChargeTrendDetector {
 public:
  bool update(std::uint32_t now_ms, std::uint16_t battery_mv,
              bool sample_valid, std::uint8_t backlight_pwm);
  void note_load_transition(std::uint32_t now_ms,
                            std::uint8_t backlight_pwm);

  bool charging() const { return charging_; }
  ChargeTrendState state() const { return state_; }
  std::size_t sample_count() const { return sample_count_; }

 private:
  struct Sample {
    std::uint32_t at_ms = 0;
    std::uint16_t millivolts = 0;
  };

  static constexpr std::size_t kSampleCapacity = 25;
  static constexpr std::uint32_t kNormalLoadSettleMs = 15000U;
  static constexpr std::uint32_t kChargingLoadSettleMs = 45000U;
  static constexpr std::uint32_t kInvalidFailSafeMs = 60000U;
  static constexpr std::uint16_t kMinimumValidMv = 2800U;
  static constexpr std::uint16_t kMaximumValidMv = 4350U;

  void clear_samples();
  void append_sample(std::uint32_t now_ms, std::uint16_t battery_mv);
  std::uint16_t recent_median() const;
  bool delta_over(std::uint32_t now_ms, std::uint32_t window_ms,
                  std::int32_t& delta_mv) const;
  bool rising_is_consistent(std::uint32_t now_ms,
                            std::uint32_t window_ms) const;

  std::array<Sample, kSampleCapacity> samples_{};
  std::size_t sample_count_ = 0;
  std::uint32_t last_load_change_ms_ = 0;
  std::uint32_t load_settle_ms_ = 0;
  std::uint32_t invalid_since_ms_ = 0;
  std::uint16_t charge_peak_mv_ = 0;
  std::uint16_t charge_candidate_level_mv_ = 0;
  std::uint8_t last_backlight_pwm_ = 0;
  std::uint8_t charge_evidence_ = 0;
  std::uint8_t unplug_evidence_ = 0;
  bool have_backlight_pwm_ = false;
  bool have_load_transition_ = false;
  bool invalid_active_ = false;
  bool charging_ = false;
  ChargeTrendState state_ = ChargeTrendState::kUnknown;
};

}  // namespace quota_monitor
