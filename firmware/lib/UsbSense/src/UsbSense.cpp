#include "UsbSense.h"

#include <algorithm>

namespace quota_monitor {

std::uint16_t median_usb_sense_mv(
    std::array<std::uint16_t, kUsbSenseSampleCount> samples) {
  std::sort(samples.begin(), samples.end());
  return samples[samples.size() / 2];
}

bool UsbSenseFilter::update(std::uint16_t millivolts) {
  if (!initialized_) {
    // Preserve the original 2250 mV boot convention. Subsequent decisions use
    // separate connect/disconnect thresholds to avoid oscillation near it.
    present_ = millivolts > kUsbSenseDecisionMv;
    initialized_ = true;
    return present_;
  }

  if (present_) {
    if (millivolts <= kUsbSenseDisconnectMv) present_ = false;
  } else if (millivolts >= kUsbSenseConnectMv) {
    present_ = true;
  }
  return present_;
}

}  // namespace quota_monitor
