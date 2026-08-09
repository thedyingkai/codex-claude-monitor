#include <unity.h>

#include "DisplayText.h"

void setUp() {}
void tearDown() {}

void test_reset_line_labels_timestamp() {
  const std::string text =
      quota_monitor::format_reset_line("08/09 12:34");
  TEST_ASSERT_EQUAL_STRING(u8"重置 08/09 12:34", text.c_str());
}

void test_reset_line_labels_missing_timestamp() {
  const std::string text = quota_monitor::format_reset_line("");
  TEST_ASSERT_EQUAL_STRING(u8"重置 --", text.c_str());
}

int main(int, char**) {
  UNITY_BEGIN();
  RUN_TEST(test_reset_line_labels_timestamp);
  RUN_TEST(test_reset_line_labels_missing_timestamp);
  return UNITY_END();
}
