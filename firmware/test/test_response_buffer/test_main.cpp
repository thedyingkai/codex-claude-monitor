#include <ResponseBuffer.h>
#include <unity.h>

#include <cstdint>

using quota_monitor::BoundedResponseBuffer;

void setUp() {}
void tearDown() {}

void test_accepts_exact_limit() {
  BoundedResponseBuffer buffer(5);
  const std::uint8_t first[] = {'a', 'b', 'c'};
  const std::uint8_t second[] = {'d', 'e'};
  TEST_ASSERT_TRUE(buffer.append(nullptr, 0));
  TEST_ASSERT_TRUE(buffer.append(first, sizeof(first)));
  TEST_ASSERT_TRUE(buffer.append(second, sizeof(second)));
  TEST_ASSERT_FALSE(buffer.overflowed());
  TEST_ASSERT_EQUAL_STRING_LEN("abcde", buffer.value().data(), 5);
}

void test_rejects_chunk_before_partial_growth() {
  BoundedResponseBuffer buffer(5);
  const std::uint8_t first[] = {'a', 'b', 'c'};
  const std::uint8_t oversized[] = {'d', 'e', 'f'};
  TEST_ASSERT_TRUE(buffer.append(first, sizeof(first)));
  TEST_ASSERT_FALSE(buffer.append(oversized, sizeof(oversized)));
  TEST_ASSERT_TRUE(buffer.overflowed());
  TEST_ASSERT_EQUAL_UINT32(3, buffer.value().size());
  TEST_ASSERT_FALSE(buffer.append(static_cast<std::uint8_t>('z')));
  TEST_ASSERT_EQUAL_UINT32(3, buffer.value().size());
}

int main(int, char**) {
  UNITY_BEGIN();
  RUN_TEST(test_accepts_exact_limit);
  RUN_TEST(test_rejects_chunk_before_partial_growth);
  return UNITY_END();
}
