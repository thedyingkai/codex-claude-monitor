#pragma once

namespace quota_monitor {

// Estimate state of charge for an unloaded or lightly loaded 1-cell LiPo.
// Voltage-only SOC is necessarily approximate, especially while charging.
float lipo_percent_from_voltage(float volts);

}  // namespace quota_monitor
