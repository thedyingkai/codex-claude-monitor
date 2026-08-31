#include <unity.h>

#include <cstdint>

#include "ChargeTrendDetector.h"
#include "RuntimePolicy.h"

using quota_monitor::ChargeTrendDetector;
using quota_monitor::ChargeTrendState;

void setUp() {}
void tearDown() {}

bool push(ChargeTrendDetector& detector, std::uint32_t& now_ms,
          std::uint16_t millivolts, std::uint8_t pwm = 153U,
          bool valid = true) {
  const bool charging = detector.update(now_ms, millivolts, valid, pwm);
  now_ms += 5000U;
  return charging;
}

void settle_at(ChargeTrendDetector& detector, std::uint32_t& now_ms,
               std::uint16_t millivolts, std::uint8_t pwm = 153U,
               int samples = 10) {
  detector.note_load_transition(now_ms, pwm);
  for (int i = 0; i < samples; ++i)
    TEST_ASSERT_FALSE(push(detector, now_ms, millivolts, pwm));
}

void drive_to_charging(ChargeTrendDetector& detector, std::uint32_t& now_ms,
                       std::uint16_t& millivolts,
                       std::uint8_t pwm = 153U) {
  settle_at(detector, now_ms, millivolts, pwm);
  bool charging = false;
  for (int i = 0; i < 16 && !charging; ++i) {
    millivolts = static_cast<std::uint16_t>(millivolts + 8U);
    charging = push(detector, now_ms, millivolts, pwm);
  }
  TEST_ASSERT_TRUE(charging);
  TEST_ASSERT_EQUAL_INT(static_cast<int>(ChargeTrendState::kCharging),
                        static_cast<int>(detector.state()));
}

void test_flat_noisy_battery_never_looks_like_charging() {
  ChargeTrendDetector detector;
  std::uint32_t now_ms = 0;
  detector.note_load_transition(now_ms, 153U);
  now_ms += 45000U;
  constexpr int noise[] = {-12, 5, 11, -4, 8, -9, 2, 12, -7, 0};
  for (int i = 0; i < 50; ++i) {
    const std::uint16_t mv =
        static_cast<std::uint16_t>(3800 + noise[i % 10]);
    TEST_ASSERT_FALSE(push(detector, now_ms, mv));
  }
  TEST_ASSERT_EQUAL_INT(static_cast<int>(ChargeTrendState::kBattery),
                        static_cast<int>(detector.state()));
}

void test_transient_voltage_spike_is_rejected() {
  ChargeTrendDetector detector;
  std::uint32_t now_ms = 0;
  settle_at(detector, now_ms, 3700U);
  TEST_ASSERT_FALSE(push(detector, now_ms, 3780U));
  for (int i = 0; i < 12; ++i)
    TEST_ASSERT_FALSE(push(detector, now_ms, 3700U));
}

void test_backlight_rebound_is_discarded_and_does_not_oscillate() {
  ChargeTrendDetector detector;
  std::uint32_t now_ms = 0;
  settle_at(detector, now_ms, 3700U, 153U, 12);

  // Dimming removes load and causes a large, persistent voltage rebound. The
  // PWM edge must reset the old baseline before that sample is considered.
  TEST_ASSERT_FALSE(push(detector, now_ms, 3770U, 26U));
  for (int i = 0; i < 24; ++i)
    TEST_ASSERT_FALSE(push(detector, now_ms, 3770U, 26U));
}

void test_fast_charge_trend_is_confirmed_conservatively() {
  ChargeTrendDetector detector;
  std::uint32_t now_ms = 0;
  std::uint16_t millivolts = 3650U;
  drive_to_charging(detector, now_ms, millivolts);
  TEST_ASSERT_TRUE(detector.charging());
}

void test_charge_is_latched_before_default_dim_threshold() {
  ChargeTrendDetector detector;
  quota_monitor::DisplayStateMachine display;
  display.configure({60U, 300U});
  display.begin(0U);

  std::uint32_t now_ms = 0U;
  bool charging = false;
  for (; now_ms <= 55000U; now_ms += 5000U) {
    const std::uint16_t mv = now_ms < 10000U ? 3650U : 3700U;
    charging = detector.update(now_ms, mv, true, 153U);
    TEST_ASSERT_EQUAL_INT(
        static_cast<int>(quota_monitor::DisplayState::kAwake),
        static_cast<int>(display.update(now_ms, charging)));
  }
  TEST_ASSERT_TRUE(charging);
  TEST_ASSERT_EQUAL_UINT8(
      255U, quota_monitor::desired_backlight_pwm(
                display.state(), 60U, charging));
}

void test_slow_consistent_charge_trend_uses_long_window() {
  ChargeTrendDetector detector;
  std::uint32_t now_ms = 0;
  settle_at(detector, now_ms, 3650U, 153U, 6);
  bool charging = false;
  std::uint16_t millivolts = 3650U;
  for (int i = 0; i < 40 && !charging; ++i) {
    millivolts = static_cast<std::uint16_t>(millivolts + 2U);
    charging = push(detector, now_ms, millivolts);
  }
  TEST_ASSERT_TRUE(charging);
}

void test_forced_full_brightness_does_not_cancel_charging() {
  ChargeTrendDetector detector;
  std::uint32_t now_ms = 0;
  std::uint16_t millivolts = 3650U;
  drive_to_charging(detector, now_ms, millivolts);

  // The policy reacts to charging by changing PWM to 255. That load edge must
  // retain the positive decision throughout the settle guard.
  for (int i = 0; i < 9; ++i) {
    millivolts = static_cast<std::uint16_t>(millivolts - 2U);
    TEST_ASSERT_TRUE(push(detector, now_ms, millivolts, 255U));
  }
  TEST_ASSERT_TRUE(detector.charging());
}

void test_sustained_unplug_drop_releases_charging() {
  ChargeTrendDetector detector;
  std::uint32_t now_ms = 0;
  std::uint16_t millivolts = 3650U;
  drive_to_charging(detector, now_ms, millivolts);

  // Simulate the policy switching to full brightness and wait through its
  // load-settle guard before establishing a charging peak.
  TEST_ASSERT_TRUE(push(detector, now_ms, millivolts, 255U));
  for (int i = 0; i < 10; ++i)
    TEST_ASSERT_TRUE(push(detector, now_ms, millivolts, 255U));

  const std::uint16_t unplugged_mv =
      static_cast<std::uint16_t>(millivolts - 60U);
  bool charging = true;
  for (int i = 0; i < 8 && charging; ++i)
    charging = push(detector, now_ms, unplugged_mv, 255U);
  TEST_ASSERT_FALSE(charging);
  TEST_ASSERT_EQUAL_INT(static_cast<int>(ChargeTrendState::kBattery),
                        static_cast<int>(detector.state()));
}

void test_brief_drop_during_network_activity_does_not_unplug() {
  ChargeTrendDetector detector;
  std::uint32_t now_ms = 0;
  std::uint16_t millivolts = 3650U;
  drive_to_charging(detector, now_ms, millivolts);
  TEST_ASSERT_TRUE(push(detector, now_ms, millivolts, 255U));
  for (int i = 0; i < 10; ++i)
    TEST_ASSERT_TRUE(push(detector, now_ms, millivolts, 255U));

  TEST_ASSERT_TRUE(
      push(detector, now_ms, static_cast<std::uint16_t>(millivolts - 60U),
           255U));
  for (int i = 0; i < 6; ++i)
    TEST_ASSERT_TRUE(push(detector, now_ms, millivolts, 255U));
}

void test_invalid_adc_fails_safe_after_one_minute() {
  ChargeTrendDetector detector;
  std::uint32_t now_ms = 0;
  std::uint16_t millivolts = 3650U;
  drive_to_charging(detector, now_ms, millivolts);
  for (int i = 0; i < 12; ++i)
    TEST_ASSERT_TRUE(push(detector, now_ms, 0U, 153U, false));
  TEST_ASSERT_FALSE(push(detector, now_ms, 0U, 153U, false));
}

void test_load_guard_is_millis_wrap_safe() {
  ChargeTrendDetector detector;
  std::uint32_t now_ms = 0xffff8000U;
  detector.note_load_transition(now_ms, 153U);
  // A real PWM edge (not the initial observation) starts the 15-second guard.
  detector.note_load_transition(now_ms, 26U);
  now_ms += 14999U;
  TEST_ASSERT_FALSE(push(detector, now_ms, 3700U, 26U));
  TEST_ASSERT_EQUAL_UINT32(0U, detector.sample_count());
  // The preceding helper advanced five seconds across uint32 wrap, so the
  // guard has now expired without relying on signed absolute timestamps.
  TEST_ASSERT_FALSE(push(detector, now_ms, 3700U, 26U));
  TEST_ASSERT_EQUAL_UINT32(1U, detector.sample_count());
}

int main(int, char**) {
  UNITY_BEGIN();
  RUN_TEST(test_flat_noisy_battery_never_looks_like_charging);
  RUN_TEST(test_transient_voltage_spike_is_rejected);
  RUN_TEST(test_backlight_rebound_is_discarded_and_does_not_oscillate);
  RUN_TEST(test_fast_charge_trend_is_confirmed_conservatively);
  RUN_TEST(test_charge_is_latched_before_default_dim_threshold);
  RUN_TEST(test_slow_consistent_charge_trend_uses_long_window);
  RUN_TEST(test_forced_full_brightness_does_not_cancel_charging);
  RUN_TEST(test_sustained_unplug_drop_releases_charging);
  RUN_TEST(test_brief_drop_during_network_activity_does_not_unplug);
  RUN_TEST(test_invalid_adc_fails_safe_after_one_minute);
  RUN_TEST(test_load_guard_is_millis_wrap_safe);
  return UNITY_END();
}
