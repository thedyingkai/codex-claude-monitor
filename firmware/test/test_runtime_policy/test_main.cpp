#include <unity.h>

#include <initializer_list>

#include "RuntimePolicy.h"

using quota_monitor::DisplayState;

void setUp() {}
void tearDown() {}

void test_display_thresholds_and_disabled_values() {
  quota_monitor::DisplayStateMachine machine;
  machine.configure({60, 300});
  machine.begin(1000);
  TEST_ASSERT_EQUAL_INT(static_cast<int>(DisplayState::kAwake),
                        static_cast<int>(machine.update(60999)));
  TEST_ASSERT_EQUAL_INT(static_cast<int>(DisplayState::kDimmed),
                        static_cast<int>(machine.update(61000)));
  TEST_ASSERT_EQUAL_INT(static_cast<int>(DisplayState::kBacklightOff),
                        static_cast<int>(machine.update(301000)));
  machine.configure({0, 0});
  TEST_ASSERT_EQUAL_INT(static_cast<int>(DisplayState::kAwake),
                        static_cast<int>(machine.update(900000)));
}

void test_activity_and_forced_modes() {
  quota_monitor::DisplayStateMachine machine;
  machine.configure({1, 2});
  machine.begin(0);
  machine.update(2500);
  machine.note_activity(2500);
  TEST_ASSERT_EQUAL_INT(static_cast<int>(DisplayState::kAwake),
                        static_cast<int>(machine.state()));
  machine.enter_portal(3000);
  TEST_ASSERT_EQUAL_INT(static_cast<int>(DisplayState::kPortal),
                        static_cast<int>(machine.update(999999)));
  machine.enter_ota(1000000);
  TEST_ASSERT_EQUAL_INT(static_cast<int>(DisplayState::kOta),
                        static_cast<int>(machine.update(2000000)));
  machine.leave_forced_mode(2000000);
  TEST_ASSERT_EQUAL_INT(static_cast<int>(DisplayState::kAwake),
                        static_cast<int>(machine.state()));
}

void test_external_power_disables_dimming_and_unplug_restarts_idle_timer() {
  quota_monitor::DisplayStateMachine machine;
  machine.configure({60, 300});
  machine.begin(1000);

  TEST_ASSERT_EQUAL_INT(static_cast<int>(DisplayState::kAwake),
                        static_cast<int>(machine.update(61000, true)));
  TEST_ASSERT_TRUE(machine.external_power_present());
  TEST_ASSERT_EQUAL_INT(static_cast<int>(DisplayState::kAwake),
                        static_cast<int>(machine.update(900000, true)));

  // The falling edge resets inactivity: thresholds are measured from 900000,
  // not from begin() or from the time spent connected to external power.
  TEST_ASSERT_EQUAL_INT(static_cast<int>(DisplayState::kAwake),
                        static_cast<int>(machine.update(900000, false)));
  TEST_ASSERT_FALSE(machine.external_power_present());
  TEST_ASSERT_EQUAL_INT(static_cast<int>(DisplayState::kAwake),
                        static_cast<int>(machine.update(959999, false)));
  TEST_ASSERT_EQUAL_INT(static_cast<int>(DisplayState::kDimmed),
                        static_cast<int>(machine.update(960000, false)));
  TEST_ASSERT_EQUAL_INT(static_cast<int>(DisplayState::kBacklightOff),
                        static_cast<int>(machine.update(1200000, false)));
}

void test_external_power_wakes_and_each_edge_resets_idle_timer() {
  quota_monitor::DisplayStateMachine machine;
  machine.configure({1, 2});
  machine.begin(0);
  TEST_ASSERT_EQUAL_INT(static_cast<int>(DisplayState::kBacklightOff),
                        static_cast<int>(machine.update(2000, false)));

  TEST_ASSERT_EQUAL_INT(static_cast<int>(DisplayState::kAwake),
                        static_cast<int>(machine.update(2500, true)));
  TEST_ASSERT_EQUAL_INT(static_cast<int>(DisplayState::kAwake),
                        static_cast<int>(machine.update(100000, true)));

  TEST_ASSERT_EQUAL_INT(static_cast<int>(DisplayState::kAwake),
                        static_cast<int>(machine.update(100000, false)));
  TEST_ASSERT_EQUAL_INT(static_cast<int>(DisplayState::kAwake),
                        static_cast<int>(machine.update(100999, false)));
  TEST_ASSERT_EQUAL_INT(static_cast<int>(DisplayState::kDimmed),
                        static_cast<int>(machine.update(101000, false)));
}

void test_forced_modes_take_priority_over_external_power() {
  quota_monitor::DisplayStateMachine machine;
  machine.configure({1, 2});
  machine.begin(0);

  machine.enter_portal(100);
  TEST_ASSERT_EQUAL_INT(static_cast<int>(DisplayState::kPortal),
                        static_cast<int>(machine.update(100000, true)));
  machine.enter_ota(100001);
  TEST_ASSERT_EQUAL_INT(static_cast<int>(DisplayState::kOta),
                        static_cast<int>(machine.update(200000, true)));

  machine.leave_forced_mode(200000);
  TEST_ASSERT_EQUAL_INT(static_cast<int>(DisplayState::kAwake),
                        static_cast<int>(machine.update(999999, true)));
}

void test_external_power_edges_are_wrap_safe() {
  quota_monitor::DisplayStateMachine machine;
  machine.configure({1, 5});
  machine.begin(0xfffff000U);

  TEST_ASSERT_EQUAL_INT(static_cast<int>(DisplayState::kAwake),
                        static_cast<int>(machine.update(0xffffff00U, true)));
  TEST_ASSERT_EQUAL_INT(static_cast<int>(DisplayState::kAwake),
                        static_cast<int>(machine.update(0x00000100U, false)));
  TEST_ASSERT_EQUAL_INT(static_cast<int>(DisplayState::kAwake),
                        static_cast<int>(machine.update(0x000004e7U, false)));
  TEST_ASSERT_EQUAL_INT(static_cast<int>(DisplayState::kDimmed),
                        static_cast<int>(machine.update(0x000004e8U, false)));
}

void test_millis_wrap_and_refresh_coalescing() {
  quota_monitor::DisplayStateMachine machine;
  machine.configure({1, 5});
  machine.begin(0xfffffe00U);
  TEST_ASSERT_EQUAL_INT(static_cast<int>(DisplayState::kDimmed),
                        static_cast<int>(machine.update(0x000003e8U)));

  quota_monitor::RefreshGate gate;
  gate.request(100);
  TEST_ASSERT_TRUE(gate.take_if_ready(100, false));
  gate.request(200);
  TEST_ASSERT_FALSE(gate.take_if_ready(900, false));
  TEST_ASSERT_TRUE(gate.pending());
  TEST_ASSERT_FALSE(gate.take_if_ready(1100, true));
  TEST_ASSERT_TRUE(gate.take_if_ready(1100, false));
}

void test_screen_off_refresh_floor() {
  TEST_ASSERT_EQUAL_UINT32(60, quota_monitor::screen_off_refresh_seconds(15, 30));
  TEST_ASSERT_EQUAL_UINT32(90, quota_monitor::screen_off_refresh_seconds(90, 60));
  TEST_ASSERT_EQUAL_UINT32(120, quota_monitor::screen_off_refresh_seconds(15, 120));
}

void test_charging_forces_full_backlight_for_every_configured_brightness() {
  for (const std::uint8_t percent : {30U, 60U, 100U}) {
    TEST_ASSERT_EQUAL_UINT8(
        255U, quota_monitor::desired_backlight_pwm(
                  DisplayState::kAwake, percent, true));
    TEST_ASSERT_EQUAL_UINT8(
        255U, quota_monitor::desired_backlight_pwm(
                  DisplayState::kDimmed, percent, true));
    TEST_ASSERT_EQUAL_UINT8(
        255U, quota_monitor::desired_backlight_pwm(
                  DisplayState::kBacklightOff, percent, true));
  }
}

void test_backlight_pwm_follows_state_when_not_charging() {
  TEST_ASSERT_EQUAL_UINT8(
      77U, quota_monitor::desired_backlight_pwm(DisplayState::kAwake, 30U,
                                                false));
  TEST_ASSERT_EQUAL_UINT8(
      153U, quota_monitor::desired_backlight_pwm(DisplayState::kPortal, 60U,
                                                 false));
  TEST_ASSERT_EQUAL_UINT8(
      26U, quota_monitor::desired_backlight_pwm(DisplayState::kDimmed, 100U,
                                                false));
  TEST_ASSERT_EQUAL_UINT8(
      0U, quota_monitor::desired_backlight_pwm(DisplayState::kBacklightOff,
                                               100U, false));
}

int main(int, char**) {
  UNITY_BEGIN();
  RUN_TEST(test_display_thresholds_and_disabled_values);
  RUN_TEST(test_activity_and_forced_modes);
  RUN_TEST(test_external_power_disables_dimming_and_unplug_restarts_idle_timer);
  RUN_TEST(test_external_power_wakes_and_each_edge_resets_idle_timer);
  RUN_TEST(test_forced_modes_take_priority_over_external_power);
  RUN_TEST(test_external_power_edges_are_wrap_safe);
  RUN_TEST(test_millis_wrap_and_refresh_coalescing);
  RUN_TEST(test_screen_off_refresh_floor);
  RUN_TEST(test_charging_forces_full_backlight_for_every_configured_brightness);
  RUN_TEST(test_backlight_pwm_follows_state_when_not_charging);
  return UNITY_END();
}
