#pragma once

#include <cstddef>
#include <cstdint>
#include <string>

namespace quota_monitor {

struct SemVersion {
  std::uint32_t major = 0;
  std::uint32_t minor = 0;
  std::uint32_t patch = 0;
};

bool parse_semver(const std::string& value, SemVersion& out);
bool is_strictly_newer_version(const std::string& candidate,
                               const std::string& current);
bool is_lower_hex_sha256(const std::string& value);

struct FirmwareManifestPolicy {
  std::string expected_board;
  std::string current_version;
  std::size_t maximum_size = 0;
  std::size_t reserved_headroom = 0;
};

bool validate_firmware_manifest(std::uint32_t schema_version,
                                const std::string& board,
                                const std::string& version,
                                std::size_t size_bytes,
                                const std::string& sha256,
                                const FirmwareManifestPolicy& policy,
                                std::string& error);

}  // namespace quota_monitor
