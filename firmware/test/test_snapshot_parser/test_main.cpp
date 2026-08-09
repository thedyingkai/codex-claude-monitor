#include <SnapshotParser.h>
#include <unity.h>

#include <cstring>
#include <string>

using quota_monitor::Snapshot;
using quota_monitor::parse_snapshot;
using quota_monitor::parse_rfc3339;
using quota_monitor::validate_snapshot_time;
using quota_monitor::serialize_snapshot_cache;

void setUp() {}
void tearDown() {}

void test_full_snapshot() {
  const char* json = R"JSON({
    "schemaVersion": 1,
    "generatedAt": "2026-08-02T15:00:00Z",
    "providers": {
      "codex": {"observedAt":"2026-08-02T15:00:00Z","freshness":"fresh","plan":"pro20","windows":{
        "fiveHour":{"usedPercent":20,"remainingPercent":80,"resetsAt":"2026-08-02T16:00:00Z"},
        "sevenDay":null}},
      "claude": {"observedAt":"2026-08-02T15:00:00Z","freshness":"stale","loginRequired":true,"plan":"max20","windows":{
        "fiveHour":null,
        "sevenDay":{"usedPercent":99.5,"remainingPercent":0.5,"resetsAt":"2026-08-09T00:00:00Z"}}}
    },
    "tasks":{"codex":{"main":1,"sub":2},"claude":{"main":3,"sub":4},"total":{"main":4,"sub":6}},
    "agents":{"online":2,"total":3},"warnings":["late agent"],
    "futureField":{"ignored":true}
  })JSON";
  Snapshot value;
  std::string error;
  TEST_ASSERT_TRUE_MESSAGE(parse_snapshot(json, std::strlen(json), value, error),
                           error.c_str());
  TEST_ASSERT_EQUAL(1, value.schema_version);
  TEST_ASSERT_TRUE(value.codex.five_hour.present);
  TEST_ASSERT_FALSE(value.codex.seven_day.present);
  TEST_ASSERT_FLOAT_WITHIN(0.01F, 80.0F, value.codex.five_hour.remaining_percent);
  TEST_ASSERT_FALSE(value.codex.login_required);
  TEST_ASSERT_TRUE(value.claude.login_required);
  TEST_ASSERT_EQUAL_STRING("pro20", value.codex.plan.c_str());
  TEST_ASSERT_EQUAL_STRING("max20", value.claude.plan.c_str());
  TEST_ASSERT_EQUAL(4, value.total_tasks.main);
  TEST_ASSERT_EQUAL(6, value.total_tasks.sub);
  TEST_ASSERT_EQUAL(2, value.agents_online);

  std::string cached;
  TEST_ASSERT_TRUE_MESSAGE(serialize_snapshot_cache(value, cached, error),
                           error.c_str());
  TEST_ASSERT_LESS_THAN_UINT32(4096, cached.size());
  Snapshot restored;
  TEST_ASSERT_TRUE_MESSAGE(
      parse_snapshot(cached.data(), cached.size(), restored, error),
      error.c_str());
  TEST_ASSERT_EQUAL_INT64(value.generated_at_epoch,
                          restored.generated_at_epoch);
  TEST_ASSERT_FLOAT_WITHIN(0.01F, value.codex.five_hour.remaining_percent,
                           restored.codex.five_hour.remaining_percent);
  TEST_ASSERT_EQUAL_STRING(value.codex.plan.c_str(), restored.codex.plan.c_str());
  TEST_ASSERT_EQUAL(value.claude.login_required, restored.claude.login_required);
  TEST_ASSERT_EQUAL(value.total_tasks.main, restored.total_tasks.main);
  TEST_ASSERT_EQUAL(value.agents_total, restored.agents_total);
  TEST_ASSERT_EQUAL_UINT32(0, restored.warnings.size());
}

void test_rejects_incomplete_window() {
  const char* json = R"JSON({"schemaVersion":1,"generatedAt":"2026-08-02T15:00:00Z","providers":{"codex":{"observedAt":"2026-08-02T15:00:00Z","freshness":"fresh","windows":{"fiveHour":{"usedPercent":6},"sevenDay":null}},"claude":{"freshness":"unavailable","windows":{"fiveHour":null,"sevenDay":null}}},"tasks":{"codex":{"main":0,"sub":0},"claude":{"main":0,"sub":0},"total":{"main":0,"sub":0}},"agents":{"online":0,"total":0},"warnings":[]})JSON";
  Snapshot value;
  std::string error;
  TEST_ASSERT_FALSE(parse_snapshot(json, std::strlen(json), value, error));
  TEST_ASSERT_NOT_EQUAL(std::string::npos, error.find("remainingPercent"));
}

void test_rfc3339_and_time_policy() {
  std::int64_t utc = 0;
  std::int64_t offset = 0;
  std::string error;
  TEST_ASSERT_TRUE(parse_rfc3339("2026-08-02T15:00:00Z", utc, error));
  TEST_ASSERT_TRUE(parse_rfc3339("2026-08-02T23:00:00+08:00", offset, error));
  TEST_ASSERT_EQUAL_INT64(utc, offset);
  TEST_ASSERT_FALSE(parse_rfc3339("2026-02-30T00:00:00Z", offset, error));

  auto fresh = validate_snapshot_time(utc, utc + 30, 0);
  TEST_ASSERT_TRUE(fresh.accepted);
  TEST_ASSERT_FALSE(fresh.stale);
  auto old = validate_snapshot_time(utc, utc + 91, 0);
  TEST_ASSERT_TRUE(old.accepted);
  TEST_ASSERT_TRUE(old.stale);
  auto future = validate_snapshot_time(utc + 121, utc, 0);
  TEST_ASSERT_FALSE(future.accepted);
  auto replay_equal = validate_snapshot_time(utc, utc + 30, utc);
  TEST_ASSERT_FALSE(replay_equal.accepted);
  auto replay_older = validate_snapshot_time(utc - 1, utc + 30, utc);
  TEST_ASSERT_FALSE(replay_older.accepted);
  auto unsynced = validate_snapshot_time(utc, 1000, 0);
  TEST_ASSERT_FALSE(unsynced.accepted);
}

void test_rejects_malformed_generated_at() {
  const char* json = R"JSON({"schemaVersion":1,"generatedAt":"not-a-time","providers":{}})JSON";
  Snapshot value;
  std::string error;
  TEST_ASSERT_FALSE(parse_snapshot(json, std::strlen(json), value, error));
  TEST_ASSERT_NOT_EQUAL(std::string::npos, error.find("generatedAt"));
}

void test_rejects_bad_schema_and_percent() {
  Snapshot value;
  std::string error;
  const char* schema = R"JSON({"schemaVersion":2})JSON";
  TEST_ASSERT_FALSE(parse_snapshot(schema, std::strlen(schema), value, error));
  const char* percent = R"JSON({"schemaVersion":1,"generatedAt":"2026-08-02T15:00:00Z","providers":{"codex":{"observedAt":"2026-08-02T15:00:00Z","freshness":"fresh","windows":{"fiveHour":{"usedPercent":60,"remainingPercent":50,"resetsAt":"2026-08-02T16:00:00Z"},"sevenDay":null}},"claude":{"freshness":"unavailable","windows":{"fiveHour":null,"sevenDay":null}}},"tasks":{"codex":{"main":0,"sub":0},"claude":{"main":0,"sub":0},"total":{"main":0,"sub":0}},"agents":{"online":0,"total":0},"warnings":[]})JSON";
  TEST_ASSERT_FALSE(parse_snapshot(percent, std::strlen(percent), value, error));
  TEST_ASSERT_NOT_EQUAL(std::string::npos, error.find("sum to 100"));
}

void test_rejects_missing_required_sections_and_bad_totals() {
  Snapshot value;
  std::string error;
  const char* missing = R"JSON({"schemaVersion":1,"generatedAt":"2026-08-02T15:00:00Z"})JSON";
  TEST_ASSERT_FALSE(parse_snapshot(missing, std::strlen(missing), value, error));

  const char* totals = R"JSON({"schemaVersion":1,"generatedAt":"2026-08-02T15:00:00Z","providers":{"codex":{"freshness":"unavailable","windows":{"fiveHour":null,"sevenDay":null}},"claude":{"freshness":"unavailable","windows":{"fiveHour":null,"sevenDay":null}}},"tasks":{"codex":{"main":1,"sub":0},"claude":{"main":1,"sub":0},"total":{"main":1,"sub":0}},"agents":{"online":0,"total":0},"warnings":[]})JSON";
  TEST_ASSERT_FALSE(parse_snapshot(totals, std::strlen(totals), value, error));
  TEST_ASSERT_NOT_EQUAL(std::string::npos, error.find("tasks.total"));
}

int main(int, char**) {
  UNITY_BEGIN();
  RUN_TEST(test_full_snapshot);
  RUN_TEST(test_rejects_incomplete_window);
  RUN_TEST(test_rejects_bad_schema_and_percent);
  RUN_TEST(test_rejects_missing_required_sections_and_bad_totals);
  RUN_TEST(test_rfc3339_and_time_policy);
  RUN_TEST(test_rejects_malformed_generated_at);
  return UNITY_END();
}
