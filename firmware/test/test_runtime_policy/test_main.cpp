#include <unity.h>

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

int main(int, char**) {
  UNITY_BEGIN();
  RUN_TEST(test_display_thresholds_and_disabled_values);
  RUN_TEST(test_activity_and_forced_modes);
  RUN_TEST(test_millis_wrap_and_refresh_coalescing);
  RUN_TEST(test_screen_off_refresh_floor);
  return UNITY_END();
}
