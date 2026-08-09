#include "DisplayText.h"

namespace quota_monitor {

std::string format_reset_line(const std::string& local_time) {
  return std::string(u8"重置 ") + (local_time.empty() ? "--" : local_time);
}

}  // namespace quota_monitor
