#include <unity.h>

#include "BatteryEstimate.h"

void setUp() {}
void tearDown() {}

void test_voltage_is_clamped() {
  TEST_ASSERT_FLOAT_WITHIN(0.01F, 0.0F,
                           quota_monitor::lipo_percent_from_voltage(2.5F));
  TEST_ASSERT_FLOAT_WITHIN(0.01F, 100.0F,
                           quota_monitor::lipo_percent_from_voltage(4.3F));
}

void test_curve_points_and_interpolation() {
  TEST_ASSERT_FLOAT_WITHIN(0.01F, 50.0F,
                           quota_monitor::lipo_percent_from_voltage(3.79F));
  TEST_ASSERT_FLOAT_WITHIN(0.1F, 85.0F,
                           quota_monitor::lipo_percent_from_voltage(4.05F));
}

int main(int, char**) {
  UNITY_BEGIN();
  RUN_TEST(test_voltage_is_clamped);
  RUN_TEST(test_curve_points_and_interpolation);
  return UNITY_END();
}
