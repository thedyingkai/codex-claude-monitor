#include <UsbSense.h>
#include <unity.h>

#include <array>

using quota_monitor::UsbSenseFilter;
using quota_monitor::kUsbSenseSampleCount;
using quota_monitor::median_usb_sense_mv;

void setUp() {}
void tearDown() {}

void test_median_rejects_adc_outliers() {
  std::array<std::uint16_t, kUsbSenseSampleCount> samples{
      2248, 2251, 4095, 2249, 0, 2250, 2253, 2252, 2247};
  TEST_ASSERT_EQUAL_UINT16(2250, median_usb_sense_mv(samples));
}

void test_initial_decision_preserves_2250_mv_boundary() {
  UsbSenseFilter absent;
  TEST_ASSERT_FALSE(absent.update(2250));
  TEST_ASSERT_TRUE(absent.initialized());

  UsbSenseFilter present;
  TEST_ASSERT_TRUE(present.update(2251));
}

void test_hysteresis_prevents_boundary_chatter() {
  UsbSenseFilter filter;
  TEST_ASSERT_FALSE(filter.update(2100));
  TEST_ASSERT_FALSE(filter.update(2274));
  TEST_ASSERT_TRUE(filter.update(2275));
  TEST_ASSERT_TRUE(filter.update(2226));
  TEST_ASSERT_FALSE(filter.update(2225));
}

int main(int, char**) {
  UNITY_BEGIN();
  RUN_TEST(test_median_rejects_adc_outliers);
  RUN_TEST(test_initial_decision_preserves_2250_mv_boundary);
  RUN_TEST(test_hysteresis_prevents_boundary_chatter);
  return UNITY_END();
}
