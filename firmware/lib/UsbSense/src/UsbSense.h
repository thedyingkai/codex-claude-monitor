#pragma once

#include <array>
#include <cstddef>
#include <cstdint>

namespace quota_monitor {

constexpr std::uint16_t kUsbSenseDecisionMv = 2250;
constexpr std::uint16_t kUsbSenseDisconnectMv = 2225;
constexpr std::uint16_t kUsbSenseConnectMv = 2275;
constexpr std::size_t kUsbSenseSampleCount = 9;
static_assert(kUsbSenseDisconnectMv < kUsbSenseDecisionMv);
static_assert(kUsbSenseDecisionMv < kUsbSenseConnectMv);
static_assert(kUsbSenseSampleCount % 2 == 1);

// Returns the middle sample. Taking an odd number of readings keeps a single
// ADC spike from changing the USB decision and requires no heap allocation.
std::uint16_t median_usb_sense_mv(
    std::array<std::uint16_t, kUsbSenseSampleCount> samples);

class UsbSenseFilter {
 public:
  bool update(std::uint16_t millivolts);
  bool initialized() const { return initialized_; }
  bool present() const { return present_; }

 private:
  bool initialized_ = false;
  bool present_ = false;
};

}  // namespace quota_monitor
