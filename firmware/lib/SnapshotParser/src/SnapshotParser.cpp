#include "SnapshotParser.h"

#include <ArduinoJson.h>
#include <algorithm>
#include <cmath>

namespace quota_monitor {
namespace {

bool digits(const std::string& text, std::size_t at, std::size_t count,
            int& output) {
  if (at + count > text.size()) return false;
  output = 0;
  for (std::size_t i = 0; i < count; ++i) {
    const char c = text[at + i];
    if (c < '0' || c > '9') return false;
    output = output * 10 + (c - '0');
  }
  return true;
}

bool leap_year(int year) {
  return year % 4 == 0 && (year % 100 != 0 || year % 400 == 0);
}

int month_days(int year, int month) {
  static const int values[] = {31, 28, 31, 30, 31, 30,
                               31, 31, 30, 31, 30, 31};
  return values[month - 1] + (month == 2 && leap_year(year) ? 1 : 0);
}

std::int64_t days_from_civil(int year, unsigned month, unsigned day) {
  year -= month <= 2;
  const int era = (year >= 0 ? year : year - 399) / 400;
  const unsigned yoe = static_cast<unsigned>(year - era * 400);
  const unsigned doy =
      (153U * (month + (month > 2 ? -3 : 9)) + 2U) / 5U + day - 1U;
  const unsigned doe = yoe * 365U + yoe / 4U - yoe / 100U + doy;
  return static_cast<std::int64_t>(era) * 146097 +
         static_cast<std::int64_t>(doe) - 719468;
}

bool finite_percent(float value) {
  return std::isfinite(value) && value >= 0.0F && value <= 100.0F;
}

bool read_window(JsonVariantConst value, RateWindow& output,
                 std::string& error, const char* path) {
  output = {};
  if (value.isNull()) return true;
  if (!value.is<JsonObjectConst>()) {
    error = std::string(path) + " must be object or null";
    return false;
  }

  JsonObjectConst obj = value.as<JsonObjectConst>();
  if ((!obj["usedPercent"].is<float>() && !obj["usedPercent"].is<int>()) ||
      (!obj["remainingPercent"].is<float>() &&
       !obj["remainingPercent"].is<int>())) {
    error = std::string(path) +
            ".usedPercent and .remainingPercent are required numbers";
    return false;
  }
  const float used = obj["usedPercent"].as<float>();
  const float remaining = obj["remainingPercent"].as<float>();
  if (!finite_percent(used) || !finite_percent(remaining)) {
    error = std::string(path) + " percentages must be within 0..100";
    return false;
  }
  if (std::fabs((used + remaining) - 100.0F) > 0.01F) {
    error = std::string(path) + " percentages must sum to 100";
    return false;
  }

  JsonVariantConst resets_at = obj["resetsAt"];
  if (resets_at.isUnbound()) {
    error = std::string(path) + ".resetsAt is required";
    return false;
  }
  if (resets_at.isNull()) {
    // Claude omits the reset timestamp when a freshly reset five-hour window
    // has not started yet. Only the unambiguous 0%-used state may use null;
    // accepting a partially consumed window without a reset would hide a
    // collector or schema error.
    if (std::fabs(used) > 0.01F || std::fabs(remaining - 100.0F) > 0.01F) {
      error = std::string(path) +
              ".resetsAt may be null only for an unused window";
      return false;
    }
  } else {
    if (!resets_at.is<const char*>()) {
      error = std::string(path) + ".resetsAt must be RFC3339 text or null";
      return false;
    }
    output.resets_at = resets_at.as<const char*>();
    if (output.resets_at.empty()) {
      error = std::string(path) + ".resetsAt must not be empty";
      return false;
    }
    if (!parse_rfc3339(output.resets_at, output.resets_at_epoch, error)) {
      error = std::string(path) + ".resetsAt: " + error;
      return false;
    }
    output.has_reset = true;
  }

  output.present = true;
  output.used_percent = used;
  output.remaining_percent = remaining;
  return true;
}

bool read_provider(JsonVariantConst value, ProviderSnapshot& output,
                   std::string& error, const char* path) {
  output = {};
  if (!value.is<JsonObjectConst>()) {
    error = std::string(path) + " must be an object";
    return false;
  }
  JsonObjectConst obj = value.as<JsonObjectConst>();
  output.present = true;
  if (!obj["freshness"].is<const char*>()) {
    error = std::string(path) + ".freshness is required";
    return false;
  }
  output.freshness = obj["freshness"].as<const char*>();
  if (output.freshness != "fresh" && output.freshness != "stale" &&
      output.freshness != "unavailable") {
    error = std::string(path) + ".freshness invalid";
    return false;
  }

  JsonVariantConst login_required = obj["loginRequired"];
  if (!login_required.isUnbound()) {
    if (!login_required.is<bool>()) {
      error = std::string(path) + ".loginRequired must be boolean";
      return false;
    }
    output.login_required = login_required.as<bool>();
  }

  JsonVariantConst plan = obj["plan"];
  if (!plan.isUnbound()) {
    if (!plan.is<const char*>()) {
      error = std::string(path) + ".plan must be text";
      return false;
    }
    output.plan = plan.as<const char*>();
    if (output.plan.size() > 32U ||
        std::any_of(output.plan.begin(), output.plan.end(),
                    [](unsigned char value) { return value < 0x20 || value > 0x7e; })) {
      error = std::string(path) + ".plan must be short printable ASCII";
      return false;
    }
  }

  JsonVariantConst observed_at = obj["observedAt"];
  if (!observed_at.isUnbound()) {
    if (!observed_at.is<const char*>()) {
      error = std::string(path) + ".observedAt must be RFC3339 text";
      return false;
    }
    output.observed_at = observed_at.as<const char*>();
    if (!parse_rfc3339(output.observed_at, output.observed_at_epoch, error)) {
      error = std::string(path) + ".observedAt: " + error;
      return false;
    }
  } else if (output.freshness != "unavailable") {
    error = std::string(path) + ".observedAt is required when data is available";
    return false;
  }

  if (!obj["windows"].is<JsonObjectConst>()) {
    error = std::string(path) + ".windows is required";
    return false;
  }
  JsonObjectConst windows = obj["windows"].as<JsonObjectConst>();
  if (windows["fiveHour"].isUnbound() || windows["sevenDay"].isUnbound()) {
    error = std::string(path) +
            ".windows.fiveHour and .sevenDay are required";
    return false;
  }
  if (!read_window(windows["fiveHour"], output.five_hour, error,
                   (std::string(path) + ".windows.fiveHour").c_str()))
    return false;
  if (!read_window(windows["sevenDay"], output.seven_day, error,
                   (std::string(path) + ".windows.sevenDay").c_str()))
    return false;
  return true;
}

bool read_tasks(JsonVariantConst value, TaskCount& tasks, std::string& error,
                const char* path) {
  tasks = {};
  if (!value.is<JsonObjectConst>()) {
    error = std::string(path) + " must be an object";
    return false;
  }
  JsonObjectConst obj = value.as<JsonObjectConst>();
  if (!obj["main"].is<int>() || !obj["sub"].is<int>()) {
    error = std::string(path) + ".main and .sub are required integers";
    return false;
  }
  tasks.main = obj["main"].as<int>();
  tasks.sub = obj["sub"].as<int>();
  if (tasks.main < 0 || tasks.sub < 0) {
    error = std::string(path) + ".main and .sub must be non-negative";
    return false;
  }
  return true;
}

bool read_nonnegative_int(JsonObjectConst object, const char* key, int& output,
                          std::string& error, const char* path) {
  if (!object[key].is<int>()) {
    error = std::string(path) + " is a required integer";
    return false;
  }
  output = object[key].as<int>();
  if (output < 0) {
    error = std::string(path) + " must be non-negative";
    return false;
  }
  return true;
}

void write_window(JsonObject windows, const char* key,
                  const RateWindow& window) {
  if (!window.present) {
    windows[key] = nullptr;
    return;
  }
  JsonObject output = windows[key].to<JsonObject>();
  output["usedPercent"] = window.used_percent;
  output["remainingPercent"] = window.remaining_percent;
  if (window.has_reset)
    output["resetsAt"] = window.resets_at;
  else
    output["resetsAt"] = nullptr;
}

void write_provider(JsonObject output, const ProviderSnapshot& provider) {
  if (!provider.observed_at.empty()) output["observedAt"] = provider.observed_at;
  output["freshness"] = provider.freshness;
  if (provider.login_required) output["loginRequired"] = true;
  if (!provider.plan.empty()) output["plan"] = provider.plan;
  JsonObject windows = output["windows"].to<JsonObject>();
  write_window(windows, "fiveHour", provider.five_hour);
  write_window(windows, "sevenDay", provider.seven_day);
}

void write_tasks(JsonObject output, const TaskCount& tasks) {
  output["main"] = tasks.main;
  output["sub"] = tasks.sub;
}

}  // namespace

bool parse_rfc3339(const std::string& value, std::int64_t& epoch,
                   std::string& error) {
  epoch = 0;
  error.clear();
  int year, month, day, hour, minute, second;
  if (value.size() < 20 || !digits(value, 0, 4, year) || value[4] != '-' ||
      !digits(value, 5, 2, month) || value[7] != '-' ||
      !digits(value, 8, 2, day) || value[10] != 'T' ||
      !digits(value, 11, 2, hour) || value[13] != ':' ||
      !digits(value, 14, 2, minute) || value[16] != ':' ||
      !digits(value, 17, 2, second)) {
    error = "invalid RFC3339 timestamp";
    return false;
  }
  if (year < 1970 || year > 9999 || month < 1 || month > 12 || day < 1 ||
      day > month_days(year, month) || hour > 23 || minute > 59 ||
      second > 59) {
    error = "timestamp field out of range";
    return false;
  }

  std::size_t cursor = 19;
  if (cursor < value.size() && value[cursor] == '.') {
    ++cursor;
    const std::size_t fraction_start = cursor;
    while (cursor < value.size() && value[cursor] >= '0' && value[cursor] <= '9')
      ++cursor;
    if (cursor == fraction_start) {
      error = "fractional seconds missing digits";
      return false;
    }
  }

  int offset_seconds = 0;
  if (cursor < value.size() && value[cursor] == 'Z') {
    ++cursor;
  } else if (cursor < value.size() &&
             (value[cursor] == '+' || value[cursor] == '-')) {
    const int sign = value[cursor] == '+' ? 1 : -1;
    int offset_hour, offset_minute;
    if (!digits(value, cursor + 1, 2, offset_hour) ||
        cursor + 5 >= value.size() || value[cursor + 3] != ':' ||
        !digits(value, cursor + 4, 2, offset_minute) || offset_hour > 23 ||
        offset_minute > 59) {
      error = "invalid RFC3339 offset";
      return false;
    }
    offset_seconds = sign * (offset_hour * 3600 + offset_minute * 60);
    cursor += 6;
  } else {
    error = "RFC3339 timezone required";
    return false;
  }
  if (cursor != value.size()) {
    error = "unexpected timestamp suffix";
    return false;
  }

  epoch = days_from_civil(year, static_cast<unsigned>(month),
                          static_cast<unsigned>(day)) *
              86400 +
          hour * 3600 + minute * 60 + second - offset_seconds;
  return true;
}

SnapshotTimeValidation validate_snapshot_time(
    std::int64_t generated_at_epoch, std::int64_t now_epoch,
    std::int64_t last_accepted_epoch, std::int64_t stale_after_seconds,
    std::int64_t max_future_skew_seconds) {
  SnapshotTimeValidation result;
  constexpr std::int64_t kMinimumSaneClock = 1704067200;  // 2024-01-01 UTC
  if (now_epoch < kMinimumSaneClock) {
    result.error = "device clock is not synchronized";
    return result;
  }
  if (generated_at_epoch <= 0) {
    result.error = "generatedAt missing";
    return result;
  }
  if (generated_at_epoch > now_epoch + max_future_skew_seconds) {
    result.error = "generatedAt is too far in the future";
    return result;
  }
  if (last_accepted_epoch > 0 && generated_at_epoch <= last_accepted_epoch) {
    result.error = "generatedAt replay or rollback";
    return result;
  }
  result.accepted = true;
  result.stale = now_epoch - generated_at_epoch > stale_after_seconds;
  return result;
}

bool parse_snapshot(const char* json, std::size_t length, Snapshot& out,
                    std::string& error) {
  out = {};
  error.clear();
  if (json == nullptr || length == 0 || length > 65536U) {
    error = "payload size invalid";
    return false;
  }

  JsonDocument document;
  const DeserializationError decode_error =
      deserializeJson(document, json, length, DeserializationOption::NestingLimit(12));
  if (decode_error) {
    error = std::string("invalid JSON: ") + decode_error.c_str();
    return false;
  }

  JsonObjectConst root = document.as<JsonObjectConst>();
  if (root.isNull() || (root["schemaVersion"] | 0) != 1) {
    error = "unsupported schemaVersion";
    return false;
  }
  out.schema_version = 1;
  out.generated_at = root["generatedAt"] | "";
  if (!parse_rfc3339(out.generated_at, out.generated_at_epoch, error)) {
    error = "generatedAt: " + error;
    return false;
  }

  JsonObjectConst providers = root["providers"].as<JsonObjectConst>();
  if (providers.isNull() || providers["codex"].isUnbound() ||
      providers["claude"].isUnbound()) {
    error = "providers.codex and providers.claude are required";
    return false;
  }
  if (!read_provider(providers["codex"], out.codex, error,
                     "providers.codex") ||
      !read_provider(providers["claude"], out.claude, error,
                     "providers.claude"))
    return false;

  JsonObjectConst tasks = root["tasks"].as<JsonObjectConst>();
  if (tasks.isNull() || !read_tasks(tasks["codex"], out.codex_tasks, error,
                                    "tasks.codex") ||
      !read_tasks(tasks["claude"], out.claude_tasks, error,
                  "tasks.claude") ||
      !read_tasks(tasks["total"], out.total_tasks, error, "tasks.total"))
    return false;
  if (out.total_tasks.main != out.codex_tasks.main + out.claude_tasks.main ||
      out.total_tasks.sub != out.codex_tasks.sub + out.claude_tasks.sub) {
    error = "tasks.total does not match provider task counts";
    return false;
  }

  JsonObjectConst agents = root["agents"].as<JsonObjectConst>();
  if (agents.isNull() ||
      !read_nonnegative_int(agents, "online", out.agents_online, error,
                            "agents.online") ||
      !read_nonnegative_int(agents, "total", out.agents_total, error,
                            "agents.total"))
    return false;
  if (out.agents_online > out.agents_total) {
    error = "agents.online cannot exceed agents.total";
    return false;
  }

  if (!root["warnings"].is<JsonArrayConst>()) {
    error = "warnings must be an array";
    return false;
  }
  JsonArrayConst warnings = root["warnings"].as<JsonArrayConst>();
  for (JsonVariantConst warning : warnings) {
    if (!warning.is<const char*>()) {
      error = "warnings entries must be strings";
      return false;
    }
    if (out.warnings.size() < 8U)
      out.warnings.emplace_back(warning.as<const char*>());
  }
  return true;
}

bool serialize_snapshot_cache(const Snapshot& value, std::string& json,
                              std::string& error) {
  json.clear();
  error.clear();
  if (value.schema_version != 1 || value.generated_at.empty()) {
    error = "snapshot is not a normalized v1 value";
    return false;
  }

  JsonDocument document;
  JsonObject root = document.to<JsonObject>();
  root["schemaVersion"] = 1;
  root["generatedAt"] = value.generated_at;
  JsonObject providers = root["providers"].to<JsonObject>();
  write_provider(providers["codex"].to<JsonObject>(), value.codex);
  write_provider(providers["claude"].to<JsonObject>(), value.claude);
  JsonObject tasks = root["tasks"].to<JsonObject>();
  write_tasks(tasks["codex"].to<JsonObject>(), value.codex_tasks);
  write_tasks(tasks["claude"].to<JsonObject>(), value.claude_tasks);
  write_tasks(tasks["total"].to<JsonObject>(), value.total_tasks);
  JsonObject agents = root["agents"].to<JsonObject>();
  agents["online"] = value.agents_online;
  agents["total"] = value.agents_total;
  root["warnings"].to<JsonArray>();

  serializeJson(document, json);
  if (json.empty() || json.size() > 4096U) {
    json.clear();
    error = "normalized snapshot cache is too large";
    return false;
  }
  return true;
}

}  // namespace quota_monitor
