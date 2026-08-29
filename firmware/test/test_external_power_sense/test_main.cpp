#include <unity.h>

#include "ExternalPowerSense.h"

using quota_monitor::DigitalPresenceFilter;

void setUp() {}
void tearDown() {}

void test_initial_sample_is_applied_immediately() {
  DigitalPresenceFilter filter;
  TEST_ASSERT_TRUE(filter.update(100, true));
  TEST_ASSERT_TRUE(filter.initialized());
}

void test_edges_require_stable_debounce_period() {
  DigitalPresenceFilter filter(300);
  TEST_ASSERT_FALSE(filter.update(1000, false));
  TEST_ASSERT_FALSE(filter.update(1100, true));
  TEST_ASSERT_FALSE(filter.update(1399, true));
  TEST_ASSERT_TRUE(filter.update(1400, true));
  TEST_ASSERT_TRUE(filter.update(1500, false));
  TEST_ASSERT_TRUE(filter.update(1799, false));
  TEST_ASSERT_FALSE(filter.update(1800, false));
}

void test_bounce_restarts_candidate_timer() {
  DigitalPresenceFilter filter(300);
  TEST_ASSERT_FALSE(filter.update(0, false));
  TEST_ASSERT_FALSE(filter.update(100, true));
  TEST_ASSERT_FALSE(filter.update(200, false));
  TEST_ASSERT_FALSE(filter.update(300, true));
  TEST_ASSERT_FALSE(filter.update(599, true));
  TEST_ASSERT_TRUE(filter.update(600, true));
}

void test_millis_wrap_is_safe() {
  DigitalPresenceFilter filter(300);
  TEST_ASSERT_FALSE(filter.update(0xffffff00U, false));
  TEST_ASSERT_FALSE(filter.update(0xfffffff0U, true));
  TEST_ASSERT_FALSE(filter.update(0x000000f0U, true));
  TEST_ASSERT_TRUE(filter.update(0x00000120U, true));
}

int main(int, char**) {
  UNITY_BEGIN();
  RUN_TEST(test_initial_sample_is_applied_immediately);
  RUN_TEST(test_edges_require_stable_debounce_period);
  RUN_TEST(test_bounce_restarts_candidate_timer);
  RUN_TEST(test_millis_wrap_is_safe);
  return UNITY_END();
}
