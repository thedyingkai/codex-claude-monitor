#include <unity.h>

#include <array>
#include <cstdint>

#include "WifiFailoverPolicy.h"

using quota_monitor::WifiFailoverAction;
using quota_monitor::WifiProfile;

void setUp() {}
void tearDown() {}

void assert_decision(const quota_monitor::WifiFailoverDecision& decision,
                     WifiFailoverAction action, WifiProfile profile,
                     std::uint32_t retry_after_ms = 0) {
  TEST_ASSERT_EQUAL_INT(static_cast<int>(action),
                        static_cast<int>(decision.action));
  TEST_ASSERT_EQUAL_INT(static_cast<int>(profile),
                        static_cast<int>(decision.profile));
  TEST_ASSERT_EQUAL_UINT32(retry_after_ms, decision.retry_after_ms);
}

void test_primary_then_backup_and_profile_timeout() {
  quota_monitor::WifiFailoverPolicy policy;
  policy.configure(true, true);
  policy.begin(100);

  assert_decision(policy.update(100, false),
                  WifiFailoverAction::kStartProfile, WifiProfile::kPrimary);
  assert_decision(policy.update(12099, false), WifiFailoverAction::kNone,
                  WifiProfile::kNone);
  assert_decision(policy.update(12100, false),
                  WifiFailoverAction::kStartProfile, WifiProfile::kBackup);
  assert_decision(policy.update(24100, false),
                  WifiFailoverAction::kRoundFailed, WifiProfile::kNone, 1000);
  assert_decision(policy.update(25099, false), WifiFailoverAction::kNone,
                  WifiProfile::kNone);
  assert_decision(policy.update(25100, false),
                  WifiFailoverAction::kStartProfile, WifiProfile::kPrimary);
}

void test_last_good_profile_is_tried_first_after_disconnect() {
  quota_monitor::WifiFailoverPolicy policy;
  policy.configure(true, true);
  policy.begin(0);

  assert_decision(policy.update(0, false), WifiFailoverAction::kStartProfile,
                  WifiProfile::kPrimary);
  assert_decision(policy.update(12000, false),
                  WifiFailoverAction::kStartProfile, WifiProfile::kBackup);
  policy.update(12500, true);
  TEST_ASSERT_EQUAL_INT(static_cast<int>(WifiProfile::kBackup),
                        static_cast<int>(policy.last_good_profile()));

  assert_decision(policy.update(13000, false),
                  WifiFailoverAction::kStartProfile, WifiProfile::kBackup);
  assert_decision(policy.update(25000, false),
                  WifiFailoverAction::kStartProfile, WifiProfile::kPrimary);
}

void test_note_connected_supports_an_externally_identified_profile() {
  quota_monitor::WifiFailoverPolicy policy;
  policy.configure(true, true);
  policy.begin(0);
  policy.note_connected(WifiProfile::kBackup);

  TEST_ASSERT_EQUAL_INT(static_cast<int>(WifiProfile::kBackup),
                        static_cast<int>(policy.last_good_profile()));
  assert_decision(policy.update(10, false),
                  WifiFailoverAction::kStartProfile, WifiProfile::kBackup);
}

void test_whole_round_backoff_doubles_and_caps_at_sixty_seconds() {
  quota_monitor::WifiFailoverPolicy policy;
  policy.configure(true, false);
  policy.begin(0);
  std::uint32_t now = 0;
  const std::array<std::uint32_t, 8> expected_delays = {
      1000, 2000, 4000, 8000, 16000, 32000, 60000, 60000};

  for (const std::uint32_t delay : expected_delays) {
    assert_decision(policy.update(now, false),
                    WifiFailoverAction::kStartProfile, WifiProfile::kPrimary);
    now += quota_monitor::kWifiProfileTimeoutMs;
    assert_decision(policy.update(now, false),
                    WifiFailoverAction::kRoundFailed, WifiProfile::kNone,
                    delay);
    now += delay;
  }
  TEST_ASSERT_EQUAL_UINT32(quota_monitor::kWifiMaximumRoundBackoffMs,
                           policy.next_round_backoff_ms());
}

void test_manual_reset_is_immediate_and_restores_initial_backoff() {
  quota_monitor::WifiFailoverPolicy policy;
  policy.configure(true, false);
  policy.begin(0);
  policy.update(0, false);
  assert_decision(policy.update(12000, false),
                  WifiFailoverAction::kRoundFailed, WifiProfile::kNone, 1000);

  policy.manual_reset(12100);
  assert_decision(policy.update(12100, false),
                  WifiFailoverAction::kStartProfile, WifiProfile::kPrimary);
  assert_decision(policy.update(24100, false),
                  WifiFailoverAction::kRoundFailed, WifiProfile::kNone, 1000);
}

void test_millis_wrap_is_safe_for_timeout_and_retry_deadline() {
  quota_monitor::WifiFailoverPolicy policy;
  policy.configure(true, false);
  const std::uint32_t start = 0xfffffff0U;
  policy.begin(start);
  assert_decision(policy.update(start, false),
                  WifiFailoverAction::kStartProfile, WifiProfile::kPrimary);

  const std::uint32_t before_timeout = start + 11999U;
  assert_decision(policy.update(before_timeout, false),
                  WifiFailoverAction::kNone, WifiProfile::kNone);
  const std::uint32_t timeout = start + 12000U;
  assert_decision(policy.update(timeout, false),
                  WifiFailoverAction::kRoundFailed, WifiProfile::kNone, 1000);
  assert_decision(policy.update(timeout + 999U, false),
                  WifiFailoverAction::kNone, WifiProfile::kNone);
  assert_decision(policy.update(timeout + 1000U, false),
                  WifiFailoverAction::kStartProfile, WifiProfile::kPrimary);
}

void test_single_profile_is_not_repeated_inside_a_failed_round() {
  quota_monitor::WifiFailoverPolicy policy;
  policy.configure(true, false);
  policy.begin(500);
  assert_decision(policy.update(500, false),
                  WifiFailoverAction::kStartProfile, WifiProfile::kPrimary);
  assert_decision(policy.update(12500, false),
                  WifiFailoverAction::kRoundFailed, WifiProfile::kNone, 1000);
  assert_decision(policy.update(12500, false), WifiFailoverAction::kNone,
                  WifiProfile::kNone);
  assert_decision(policy.update(13499, false), WifiFailoverAction::kNone,
                  WifiProfile::kNone);
  assert_decision(policy.update(13500, false),
                  WifiFailoverAction::kStartProfile, WifiProfile::kPrimary);
}

void test_duplicate_ssid_validation_helper() {
  TEST_ASSERT_TRUE(quota_monitor::wifi_profile_ssids_valid("home", ""));
  TEST_ASSERT_TRUE(
      quota_monitor::wifi_profile_ssids_valid("home", "phone-hotspot"));
  TEST_ASSERT_FALSE(quota_monitor::wifi_profile_ssids_valid("home", "home"));
  TEST_ASSERT_FALSE(quota_monitor::wifi_profile_ssids_valid("", "backup"));
}

void test_no_profiles_produces_no_action() {
  quota_monitor::WifiFailoverPolicy policy;
  policy.configure(false, false);
  policy.begin(0);
  assert_decision(policy.update(0, false), WifiFailoverAction::kNone,
                  WifiProfile::kNone);
  assert_decision(policy.update(100000, false), WifiFailoverAction::kNone,
                  WifiProfile::kNone);
}

int main(int, char**) {
  UNITY_BEGIN();
  RUN_TEST(test_primary_then_backup_and_profile_timeout);
  RUN_TEST(test_last_good_profile_is_tried_first_after_disconnect);
  RUN_TEST(test_note_connected_supports_an_externally_identified_profile);
  RUN_TEST(test_whole_round_backoff_doubles_and_caps_at_sixty_seconds);
  RUN_TEST(test_manual_reset_is_immediate_and_restores_initial_backoff);
  RUN_TEST(test_millis_wrap_is_safe_for_timeout_and_retry_deadline);
  RUN_TEST(test_single_profile_is_not_repeated_inside_a_failed_round);
  RUN_TEST(test_duplicate_ssid_validation_helper);
  RUN_TEST(test_no_profiles_produces_no_action);
  return UNITY_END();
}
