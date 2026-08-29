#pragma once

#include <cstddef>
#include <cstdint>
#include <string>
#include <vector>

namespace quota_monitor {

struct RateWindow {
  bool present = false;
  bool has_reset = false;
  float used_percent = 0.0F;
  float remaining_percent = 0.0F;
  std::string resets_at;
  std::int64_t resets_at_epoch = 0;
};

struct ProviderSnapshot {
  bool present = false;
  std::string observed_at;
  std::int64_t observed_at_epoch = 0;
  std::string freshness = "unavailable";
  bool login_required = false;
  std::string plan;
  RateWindow five_hour;
  RateWindow seven_day;
};

struct TaskCount {
  int main = 0;
  int sub = 0;
};

struct Snapshot {
  int schema_version = 0;
  std::string generated_at;
  std::int64_t generated_at_epoch = 0;
  ProviderSnapshot codex;
  ProviderSnapshot claude;
  TaskCount codex_tasks;
  TaskCount claude_tasks;
  TaskCount total_tasks;
  int agents_online = 0;
  int agents_total = 0;
  std::vector<std::string> warnings;
};

bool parse_snapshot(const char* json, std::size_t length, Snapshot& out,
                    std::string& error);

// Serialize only the normalized v1 fields needed by the display. Unknown
// server fields and warnings are intentionally omitted so the NVS cache stays
// small and contains no bearer token or transport metadata.
bool serialize_snapshot_cache(const Snapshot& value, std::string& json,
                              std::string& error);

bool parse_rfc3339(const std::string& value, std::int64_t& epoch,
                   std::string& error);

struct SnapshotTimeValidation {
  bool accepted = false;
  bool stale = false;
  std::string error;
};

SnapshotTimeValidation validate_snapshot_time(
    std::int64_t generated_at_epoch, std::int64_t now_epoch,
    std::int64_t last_accepted_epoch, std::int64_t stale_after_seconds = 90,
    std::int64_t max_future_skew_seconds = 120);

}  // namespace quota_monitor
