#include "WifiFailoverPolicy.h"

#include <algorithm>

namespace quota_monitor {

namespace {

bool deadline_reached(std::uint32_t now_ms, std::uint32_t deadline_ms) {
  return static_cast<std::int32_t>(now_ms - deadline_ms) >= 0;
}

}  // namespace

bool wifi_profile_ssids_valid(std::string_view primary_ssid,
                              std::string_view backup1_ssid,
                              std::string_view backup2_ssid) {
  if (primary_ssid.empty()) return false;
  if (!backup1_ssid.empty() && backup1_ssid == primary_ssid) return false;
  if (!backup2_ssid.empty() && backup2_ssid == primary_ssid) return false;
  return backup1_ssid.empty() || backup2_ssid.empty() ||
         backup1_ssid != backup2_ssid;
}

bool wifi_profile_ssids_valid(std::string_view primary_ssid,
                              std::string_view backup_ssid) {
  return wifi_profile_ssids_valid(primary_ssid, backup_ssid, {});
}

void WifiFailoverPolicy::configure(bool primary_available,
                                   bool backup1_available,
                                   bool backup2_available) {
  primary_available_ = primary_available;
  backup1_available_ = backup1_available;
  backup2_available_ = backup2_available;
  if (!available(last_good_profile_)) last_good_profile_ = WifiProfile::kNone;
  if (!available(active_profile_)) {
    active_profile_ = WifiProfile::kNone;
    phase_ = Phase::kIdle;
  }
}

void WifiFailoverPolicy::configure(bool primary_available,
                                   bool backup_available) {
  configure(primary_available, backup_available, false);
}

void WifiFailoverPolicy::begin(std::uint32_t now_ms) {
  manual_reset(now_ms);
}

bool WifiFailoverPolicy::available(WifiProfile profile) const {
  if (profile == WifiProfile::kPrimary) return primary_available_;
  if (profile == WifiProfile::kBackup1) return backup1_available_;
  if (profile == WifiProfile::kBackup2) return backup2_available_;
  return false;
}

void WifiFailoverPolicy::prepare_round() {
  order_[0] = WifiProfile::kNone;
  order_[1] = WifiProfile::kNone;
  order_[2] = WifiProfile::kNone;
  order_size_ = 0;
  order_index_ = 0;

  const auto append = [this](WifiProfile profile) {
    if (!available(profile)) return;
    for (std::uint8_t index = 0; index < order_size_; ++index) {
      if (order_[index] == profile) return;
    }
    order_[order_size_++] = profile;
  };

  // last_good_profile_ is deliberately not used for ordering. It records
  // connection history for diagnostics, while configured priority remains
  // stable across disconnects and retry rounds.
  append(WifiProfile::kPrimary);
  append(WifiProfile::kBackup1);
  append(WifiProfile::kBackup2);
}

WifiFailoverDecision WifiFailoverPolicy::start_current_profile(
    std::uint32_t now_ms) {
  if (order_index_ >= order_size_) return {};
  active_profile_ = order_[order_index_];
  attempt_started_ms_ = now_ms;
  phase_ = Phase::kAttempting;
  return {WifiFailoverAction::kStartProfile, active_profile_, 0};
}

WifiFailoverDecision WifiFailoverPolicy::update(std::uint32_t now_ms,
                                                bool connected) {
  if (connected) {
    if (available(active_profile_)) last_good_profile_ = active_profile_;
    phase_ = Phase::kConnected;
    next_backoff_ms_ = kWifiInitialRoundBackoffMs;
    return {};
  }

  if (!primary_available_ && !backup1_available_ && !backup2_available_) {
    active_profile_ = WifiProfile::kNone;
    phase_ = Phase::kIdle;
    return {};
  }

  if (phase_ == Phase::kConnected || phase_ == Phase::kIdle) {
    prepare_round();
    return start_current_profile(now_ms);
  }

  if (phase_ == Phase::kWaiting) {
    if (!deadline_reached(now_ms, next_round_ms_)) return {};
    prepare_round();
    return start_current_profile(now_ms);
  }

  if (now_ms - attempt_started_ms_ < kWifiProfileTimeoutMs) return {};

  ++order_index_;
  if (order_index_ < order_size_) return start_current_profile(now_ms);

  const std::uint32_t retry_after_ms = next_backoff_ms_;
  next_round_ms_ = now_ms + retry_after_ms;
  next_backoff_ms_ =
      std::min(next_backoff_ms_ * 2U, kWifiMaximumRoundBackoffMs);
  active_profile_ = WifiProfile::kNone;
  phase_ = Phase::kWaiting;
  return {WifiFailoverAction::kRoundFailed, WifiProfile::kNone,
          retry_after_ms};
}

void WifiFailoverPolicy::note_connected(WifiProfile profile) {
  if (!available(profile)) return;
  active_profile_ = profile;
  last_good_profile_ = profile;
  phase_ = Phase::kConnected;
  next_backoff_ms_ = kWifiInitialRoundBackoffMs;
}

void WifiFailoverPolicy::manual_reset(std::uint32_t now_ms) {
  (void)now_ms;
  phase_ = Phase::kIdle;
  order_size_ = 0;
  order_index_ = 0;
  active_profile_ = WifiProfile::kNone;
  attempt_started_ms_ = 0;
  next_round_ms_ = 0;
  next_backoff_ms_ = kWifiInitialRoundBackoffMs;
}

}  // namespace quota_monitor
