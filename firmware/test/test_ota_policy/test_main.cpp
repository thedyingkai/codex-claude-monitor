#include <unity.h>

#include "OtaPolicy.h"

void setUp() {}
void tearDown() {}

void test_semver_is_strict() {
  quota_monitor::SemVersion version;
  TEST_ASSERT_TRUE(quota_monitor::parse_semver("0.3.0", version));
  TEST_ASSERT_FALSE(quota_monitor::parse_semver("v0.3.0", version));
  TEST_ASSERT_FALSE(quota_monitor::parse_semver("0.03.0", version));
  TEST_ASSERT_FALSE(quota_monitor::parse_semver("0.3", version));
  TEST_ASSERT_TRUE(quota_monitor::is_strictly_newer_version("0.3.1", "0.3.0"));
  TEST_ASSERT_FALSE(quota_monitor::is_strictly_newer_version("0.3.0", "0.3.0"));
}

void test_manifest_policy() {
  quota_monitor::FirmwareManifestPolicy policy{"e32r28t", "0.3.0", 1966080,
                                                131072};
  std::string error;
  const std::string hash(64, 'a');
  TEST_ASSERT_TRUE(quota_monitor::validate_firmware_manifest(
      1, "e32r28t", "0.3.1", 1500000, hash, policy, error));
  TEST_ASSERT_FALSE(quota_monitor::validate_firmware_manifest(
      1, "other", "0.3.1", 1500000, hash, policy, error));
  TEST_ASSERT_FALSE(quota_monitor::validate_firmware_manifest(
      1, "e32r28t", "0.3.1", 1900000, hash, policy, error));
  TEST_ASSERT_FALSE(quota_monitor::validate_firmware_manifest(
      1, "e32r28t", "0.3.1", 1500000, std::string(64, 'A'), policy, error));
}

int main(int, char**) {
  UNITY_BEGIN();
  RUN_TEST(test_semver_is_strict);
  RUN_TEST(test_manifest_policy);
  return UNITY_END();
}
