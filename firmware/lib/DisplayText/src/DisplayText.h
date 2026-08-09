#pragma once

#include <string>

namespace quota_monitor {

// Formats the second line shown below every 5h/7d quota value. Keeping this
// provider-neutral helper outside the Arduino UI makes the required wording
// testable in the native PlatformIO environment.
std::string format_reset_line(const std::string& local_time);

}  // namespace quota_monitor
