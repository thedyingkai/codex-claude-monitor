#pragma once

#include <cstdint>
#include <string_view>

namespace quota_monitor {

enum class WifiProfile : std::uint8_t {
  kNone = 0,
  kPrimary,
  kBackup1,
  kBackup2,

  // Compatibility alias for callers that have not yet migrated from the
  // original two-profile policy. New code should use kBackup1 explicitly.
  kBackup = kBackup1,
};

enum class WifiFailoverAction : std::uint8_t {
  kNone,
  // Abort any previous station attempt, then begin the selected profile.
  kStartProfile,
  // Abort the final attempt in this round. The next round starts after
  // retry_after_ms without repeating the final profile immediately.
  kRoundFailed,
};

struct WifiFailoverDecision {
  WifiFailoverAction action = WifiFailoverAction::kNone;
  WifiProfile profile = WifiProfile::kNone;
  std::uint32_t retry_after_ms = 0;
};

constexpr std::uint32_t kWifiProfileTimeoutMs = 12000U;
constexpr std::uint32_t kWifiInitialRoundBackoffMs = 1000U;
constexpr std::uint32_t kWifiMaximumRoundBackoffMs = 60000U;

// The primary SSID is required. Backup SSIDs are independently optional. Every
// populated SSID must be unique so the connected profile can be identified
// unambiguously after the ESP32 reports a successful connection.
bool wifi_profile_ssids_valid(std::string_view primary_ssid,
                              std::string_view backup1_ssid,
                              std::string_view backup2_ssid);

// Compatibility overload for the original primary/backup configuration.
bool wifi_profile_ssids_valid(std::string_view primary_ssid,
                              std::string_view backup_ssid);

// Pure timing/ordering policy; this class never calls Wi-Fi or sleeps. The
// caller applies kStartProfile asynchronously and feeds the observed station
// connection state back into update(). All time arithmetic is uint32_t
// wrap-safe because the largest interval is much less than 2^31 ms.
class WifiFailoverPolicy {
 public:
  // Availability is supplied in fixed priority order. Every connection round
  // starts at primary, then tries backup1 and backup2, skipping absent entries.
  void configure(bool primary_available, bool backup1_available,
                 bool backup2_available);

  // Compatibility overload for the original primary/backup configuration.
  void configure(bool primary_available, bool backup_available);
  void begin(std::uint32_t now_ms);

  WifiFailoverDecision update(std::uint32_t now_ms, bool connected);

  // Use this when the caller can identify the connected profile directly from
  // the associated SSID. It also handles a connection established outside the
  // currently active attempt.
  void note_connected(WifiProfile profile);

  // Cancels an active/waiting round and restores the one-second initial
  // backoff. The next update starts a new primary-first round. The caller
  // should disconnect the current station attempt before applying the next
  // kStartProfile decision.
  void manual_reset(std::uint32_t now_ms);

  WifiProfile active_profile() const { return active_profile_; }
  WifiProfile last_good_profile() const { return last_good_profile_; }
  std::uint32_t next_round_backoff_ms() const { return next_backoff_ms_; }

 private:
  enum class Phase : std::uint8_t {
    kIdle,
    kAttempting,
    kWaiting,
    kConnected,
  };

  bool available(WifiProfile profile) const;
  void prepare_round();
  WifiFailoverDecision start_current_profile(std::uint32_t now_ms);

  bool primary_available_ = false;
  bool backup1_available_ = false;
  bool backup2_available_ = false;
  Phase phase_ = Phase::kIdle;
  WifiProfile order_[3] = {WifiProfile::kNone, WifiProfile::kNone,
                           WifiProfile::kNone};
  std::uint8_t order_size_ = 0;
  std::uint8_t order_index_ = 0;
  WifiProfile active_profile_ = WifiProfile::kNone;
  WifiProfile last_good_profile_ = WifiProfile::kNone;
  std::uint32_t attempt_started_ms_ = 0;
  std::uint32_t next_round_ms_ = 0;
  std::uint32_t next_backoff_ms_ = kWifiInitialRoundBackoffMs;
};

}  // namespace quota_monitor
