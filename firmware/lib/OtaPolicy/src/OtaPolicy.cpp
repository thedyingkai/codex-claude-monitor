#include "OtaPolicy.h"

#include <cctype>
#include <limits>

namespace quota_monitor {

namespace {

bool parse_part(const std::string& value, std::size_t begin, std::size_t end,
                std::uint32_t& out) {
  if (begin == end || (end - begin > 1 && value[begin] == '0')) return false;
  std::uint64_t result = 0;
  for (std::size_t i = begin; i < end; ++i) {
    if (!std::isdigit(static_cast<unsigned char>(value[i]))) return false;
    result = result * 10U + static_cast<unsigned>(value[i] - '0');
    if (result > std::numeric_limits<std::uint32_t>::max()) return false;
  }
  out = static_cast<std::uint32_t>(result);
  return true;
}

}  // namespace

bool parse_semver(const std::string& value, SemVersion& out) {
  const std::size_t first = value.find('.');
  if (first == std::string::npos) return false;
  const std::size_t second = value.find('.', first + 1);
  if (second == std::string::npos || value.find('.', second + 1) != std::string::npos)
    return false;
  SemVersion parsed;
  if (!parse_part(value, 0, first, parsed.major) ||
      !parse_part(value, first + 1, second, parsed.minor) ||
      !parse_part(value, second + 1, value.size(), parsed.patch))
    return false;
  out = parsed;
  return true;
}

bool is_strictly_newer_version(const std::string& candidate,
                               const std::string& current) {
  SemVersion a;
  SemVersion b;
  if (!parse_semver(candidate, a) || !parse_semver(current, b)) return false;
  if (a.major != b.major) return a.major > b.major;
  if (a.minor != b.minor) return a.minor > b.minor;
  return a.patch > b.patch;
}

bool is_lower_hex_sha256(const std::string& value) {
  if (value.size() != 64) return false;
  for (const char c : value)
    if (!((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f'))) return false;
  return true;
}

bool validate_firmware_manifest(std::uint32_t schema_version,
                                const std::string& board,
                                const std::string& version,
                                std::size_t size_bytes,
                                const std::string& sha256,
                                const FirmwareManifestPolicy& policy,
                                std::string& error) {
  if (schema_version != 1) {
    error = "unsupported manifest schema";
    return false;
  }
  if (board != policy.expected_board) {
    error = "firmware board mismatch";
    return false;
  }
  if (!is_strictly_newer_version(version, policy.current_version)) {
    error = "firmware is not newer";
    return false;
  }
  if (size_bytes == 0 || policy.maximum_size == 0 ||
      policy.reserved_headroom > policy.maximum_size ||
      size_bytes > policy.maximum_size - policy.reserved_headroom) {
    error = "firmware does not fit OTA slot";
    return false;
  }
  if (!is_lower_hex_sha256(sha256)) {
    error = "invalid firmware sha256";
    return false;
  }
  error.clear();
  return true;
}

}  // namespace quota_monitor
