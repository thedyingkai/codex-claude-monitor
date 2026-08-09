#include "BatteryEstimate.h"

#include <algorithm>
#include <array>

namespace quota_monitor {
namespace {

struct VoltagePoint {
  float volts;
  float percent;
};

// A deliberately conservative generic 1S LiPo discharge curve. It is only a
// display estimate; GPIO34 must still be checked against a meter on real units.
constexpr std::array<VoltagePoint, 11> kCurve{{
    {3.30F, 0.0F},
    {3.50F, 10.0F},
    {3.60F, 20.0F},
    {3.68F, 30.0F},
    {3.74F, 40.0F},
    {3.79F, 50.0F},
    {3.85F, 60.0F},
    {3.92F, 70.0F},
    {4.00F, 80.0F},
    {4.10F, 90.0F},
    {4.20F, 100.0F},
}};

}  // namespace

float lipo_percent_from_voltage(float volts) {
  if (volts <= kCurve.front().volts) return 0.0F;
  if (volts >= kCurve.back().volts) return 100.0F;

  for (std::size_t i = 1; i < kCurve.size(); ++i) {
    if (volts <= kCurve[i].volts) {
      const VoltagePoint& low = kCurve[i - 1];
      const VoltagePoint& high = kCurve[i];
      const float fraction = (volts - low.volts) / (high.volts - low.volts);
      return std::clamp(low.percent + fraction * (high.percent - low.percent),
                        0.0F, 100.0F);
    }
  }
  return 100.0F;
}

}  // namespace quota_monitor
