#include <Arduino.h>
#include <ArduinoJson.h>
#include <DNSServer.h>
#include <HTTPClient.h>
#include <Preferences.h>
#include <TFT_eSPI.h>
#include <Update.h>
#include <WebServer.h>
#include <WiFi.h>
#include <WiFiClient.h>
#include <WiFiClientSecure.h>
#include <esp_heap_caps.h>
#include <esp_ota_ops.h>
#include <esp_sntp.h>
#include <mbedtls/sha256.h>
#include <lvgl.h>

#if defined(QUOTA_BOARD_E32R28T)
#include <SPI.h>
#include <XPT2046_Touchscreen.h>
#else
#include <Wire.h>
#include <driver/gpio.h>
#include <esp_sleep.h>
#endif

#include <algorithm>
#include <array>
#include <cstdint>
#include <ctime>
#include <initializer_list>
#include <memory>
#include <new>
#include <string>
#include <utility>

#include "BatteryEstimate.h"
#include "ChargeTrendDetector.h"
#include "DisplayText.h"
#include "ExternalPowerSense.h"
#include "SnapshotParser.h"
#include "ResponseBuffer.h"
#include "RuntimePolicy.h"
#include "WifiFailoverPolicy.h"
#include "pins.h"
#include "OtaPolicy.h"
#include "version.h"

#if QUOTA_HAS_CARRIER_POWER
#include "UsbSense.h"
#endif

#ifndef QUOTA_ALLOW_LAN_HTTP
#define QUOTA_ALLOW_LAN_HTTP 0
#endif

LV_FONT_DECLARE(lv_font_qmon_16);

extern const uint8_t isrg_root_x1_start[]
    asm("_binary_certs_isrgrootx1_pem_start");
extern const uint8_t device_root_ca_start[]
    asm("_binary_certs_device_root_ca_pem_start");
extern const uint8_t qmon_background_start[]
    asm("_binary_assets_qmon_background_rgb565_start");
extern const uint8_t qmon_background_end[]
    asm("_binary_assets_qmon_background_rgb565_end");

namespace {

constexpr uint32_t kDefaultRefreshSeconds = 15;
constexpr uint32_t kDefaultDimSeconds = 60;
constexpr uint32_t kDefaultScreenOffSeconds = 300;
constexpr uint32_t kDefaultScreenOffRefreshSeconds = 60;
constexpr uint8_t kDefaultBrightnessPercent = 60;
constexpr uint32_t kMaxResponseBytes = 65536;
constexpr std::int64_t kSnapshotPersistIntervalSeconds = 120;
constexpr std::int64_t kStaleAfterSeconds = 90;
constexpr uint32_t kLongPressMs = 1200;
constexpr uint32_t kPortalPressMs = 5000;
constexpr uint32_t kPortalIdleMs = 10U * 60U * 1000U;
constexpr uint32_t kOtaSelfTestMs = 30000;
constexpr size_t kPortalPostLimit = 2048;
#if defined(QUOTA_BOARD_E32R28T)
constexpr char kFirmwareBoard[] = "e32r28t";
#else
constexpr char kFirmwareBoard[] = "firebeetle2_esp32s3";
#endif
constexpr std::int64_t kMinimumSaneClock = 1704067200;  // 2024-01-01 UTC
constexpr uint16_t kScreenWidth = 320;
constexpr uint16_t kScreenHeight = 240;
constexpr uint32_t kBackgroundBytes =
    static_cast<uint32_t>(kScreenWidth) * kScreenHeight * 2U;
constexpr uint32_t kCodexAccent = 0x35d9ff;
constexpr uint32_t kClaudeAccent = 0xffa24a;

static_assert(LV_COLOR_DEPTH == 16,
              "qmon-background.rgb565 requires 16-bit LVGL color");

#if defined(QUOTA_BOARD_E32R28T)
// Keyes ships a panel-specific ILI9341V initialization table in its patched
// TFT_eSPI 2.5.43 package. Replay those power, VCOM and gamma values after the
// upstream library initializes the controller so the panel has the contrast
// and color response intended by the manufacturer.
void keyes_ili9341_post_init(TFT_eSPI& display) {
  const auto command = [&display](
                           uint8_t cmd,
                           std::initializer_list<uint8_t> data = {}) {
    display.writecommand(cmd);
    for (const uint8_t value : data) display.writedata(value);
  };

  display.writecommand(0x01);  // Software reset.
  delay(150);
  display.startWrite();
  command(0xCF, {0x00, 0xC9, 0x30});
  command(0xED, {0x64, 0x03, 0x12, 0x81});
  command(0xE8, {0x85, 0x10, 0x7A});
  command(0xCB, {0x39, 0x2C, 0x00, 0x34, 0x02});
  command(0xF7, {0x20});
  command(0xEA, {0x00, 0x00});
  command(0xC0, {0x1B});
  command(0xC1, {0x00});
  command(0xC5, {0x44, 0x30});
  command(0xC7, {0xB6});
  command(0x36, {0x08});
  command(0x3A, {0x55});  // RGB565.
  command(0xB1, {0x00, 0x1A});
  command(0xB6, {0x0A, 0x82});
  command(0xF2, {0x00});
  command(0x26, {0x01});
  command(0xE0, {0x0F, 0x2A, 0x28, 0x08, 0x0E, 0x08, 0x54, 0xA9,
                 0x43, 0x0A, 0x0F, 0x00, 0x00, 0x00, 0x00});
  command(0xE1, {0x00, 0x15, 0x17, 0x07, 0x11, 0x06, 0x2B, 0x56,
                 0x3C, 0x05, 0x10, 0x0F, 0x3F, 0x3F, 0x0F});
  command(0x2B, {0x00, 0x00, 0x01, 0x3F});
  command(0x2A, {0x00, 0x00, 0x00, 0xEF});
  command(0x11);  // Sleep out.
  display.endWrite();
  delay(120);
  display.writecommand(0x29);  // Display on.
  delay(20);
}
#endif

struct DeviceConfig {
  String ssid;
  String password;
  String ssid2;
  String password2;
  String ssid3;
  String password3;
  String base_url;
  String token;
  String timezone = "CST-8";
  uint32_t refresh_seconds = kDefaultRefreshSeconds;
  uint8_t brightness_percent = kDefaultBrightnessPercent;
  uint32_t dim_after_seconds = kDefaultDimSeconds;
  uint32_t screen_off_after_seconds = kDefaultScreenOffSeconds;
  uint32_t screen_off_refresh_seconds = kDefaultScreenOffRefreshSeconds;
  // GPIO35 is input-only and has no internal pull resistor. Keep the external
  // USB-power detector disabled until the documented divider is installed.
  bool external_power_sense_enabled = false;
#if QUOTA_HAS_TOUCH
  // Keyes' factory example values for a landscape E32R28T. Resistive panels
  // vary, so these can be replaced with values from a calibration run.
  uint16_t touch_x_min = 495;
  uint16_t touch_x_max = 3398;
  uint16_t touch_y_min = 721;
  uint16_t touch_y_max = 3448;
#endif
};

struct ButtonState {
  int pin;
  bool last_down = false;
  bool long_sent = false;
  bool swallow_release = false;
  uint32_t down_since = 0;

  explicit ButtonState(int gpio) : pin(gpio) {}
};

Preferences preferences;
DeviceConfig config;
DeviceConfig committed_config;
bool config_dirty = false;
bool network_config_dirty = false;
uint8_t config_active_slot = 0;
TFT_eSPI tft;
lv_disp_draw_buf_t draw_buffer;
lv_disp_drv_t display_driver;
lv_color_t* pixels = nullptr;

lv_img_dsc_t background_image{};
std::array<lv_obj_t*, 3> wifi_arcs{};
lv_obj_t* wifi_label = nullptr;
lv_obj_t* wifi_status_dot = nullptr;
lv_obj_t* wifi_offline_cross_a = nullptr;
lv_obj_t* wifi_offline_cross_b = nullptr;
lv_obj_t* clock_face = nullptr;
lv_obj_t* battery_label = nullptr;
lv_obj_t* battery_outline = nullptr;
lv_obj_t* battery_fill = nullptr;
lv_obj_t* age_label = nullptr;
lv_obj_t* codex_title_label = nullptr;
lv_obj_t* claude_title_label = nullptr;
lv_obj_t* codex_status_label = nullptr;
lv_obj_t* claude_status_label = nullptr;
lv_obj_t* codex_5h_label = nullptr;
lv_obj_t* codex_7d_label = nullptr;
lv_obj_t* claude_5h_label = nullptr;
lv_obj_t* claude_7d_label = nullptr;
lv_obj_t* codex_5h_bar = nullptr;
lv_obj_t* codex_7d_bar = nullptr;
lv_obj_t* claude_5h_bar = nullptr;
lv_obj_t* claude_7d_bar = nullptr;
lv_obj_t* message_panel = nullptr;
lv_obj_t* message_label = nullptr;

const lv_point_t kWifiCrossAPoints[] = {{0, 0}, {5, 5}};
const lv_point_t kWifiCrossBPoints[] = {{5, 0}, {0, 5}};
const lv_point_t kClockHourPoints[] = {{0, 4}, {0, 0}};
const lv_point_t kClockMinutePoints[] = {{0, 0}, {4, 2}};

quota_monitor::Snapshot snapshot;
bool have_snapshot = false;
String last_error = "not configured";
uint32_t last_success_ms = 0;
std::int64_t last_accepted_generated_epoch = 0;
std::int64_t last_persisted_generated_epoch = 0;
uint32_t next_fetch_ms = 0;
uint32_t api_backoff_ms = 1000;
uint32_t message_until_ms = 0;
const uint8_t brightness_percent[] = {30, 60, 100};
String serial_line;
ButtonState button_a{PIN_BUTTON_A};
#if QUOTA_HAS_SECOND_BUTTON
ButtonState button_b{PIN_BUTTON_B};
#endif
#if QUOTA_HAS_CARRIER_POWER
quota_monitor::UsbSenseFilter usb_sense_filter;
#endif
#if QUOTA_HAS_EXTERNAL_POWER_SENSE
quota_monitor::DigitalPresenceFilter external_power_filter{300U};
#endif
bool have_persisted_snapshot_cache = false;
float cached_battery_soc = -1.0F;
float cached_battery_voltage = -1.0F;
uint16_t cached_battery_mv = 0;
bool cached_battery_ok = false;
uint32_t next_battery_sample_ms = 0;
#if defined(QUOTA_BOARD_E32R28T)
quota_monitor::ChargeTrendDetector charge_trend_detector;
#endif

quota_monitor::DisplayStateMachine display_state;
quota_monitor::WifiFailoverPolicy wifi_failover_policy;
quota_monitor::DisplayState applied_display_state =
    quota_monitor::DisplayState::kAwake;
uint8_t applied_backlight_pwm = 0;
bool backlight_pwm_initialized = false;
quota_monitor::RefreshGate manual_refresh_gate;
bool fetch_in_flight = false;
uint32_t restart_after_ota_ms = 0;
uint32_t restart_after_config_ms = 0;
uint32_t ota_self_test_started_ms = 0;
bool ota_self_test_pending = false;
bool runtime_peripherals_ready = false;
bool nvs_storage_ready = false;
uint32_t snapshot_generation = 1;
bool reset_in_progress = false;

enum class NetworkJob : uint8_t {
  kFetchSnapshot,
  kTestCandidateConfig,
  kScanWifi,
  kCheckOta,
  kInstallOta,
};

QueueHandle_t network_jobs = nullptr;
SemaphoreHandle_t shared_mutex = nullptr;
TaskHandle_t network_task_handle = nullptr;
bool candidate_test_in_flight = false;
bool fetch_result_ready = false;
bool fetch_result_success = false;
String fetch_result_error;
String wifi_scan_json = "{\"scanning\":true,\"networks\":[]}";

struct CandidateResult {
  bool ready = false;
  bool success = false;
  String error;
};

DeviceConfig pending_candidate;
CandidateResult candidate_result;

struct OtaStatus {
  bool checking = false;
  bool installing = false;
  bool manifest_valid = false;
  bool update_available = false;
  bool result_ready = false;
  bool install_success = false;
  uint8_t progress_percent = 0;
  String latest_version;
  String published_at;
  size_t size_bytes = 0;
  String sha256;
  String error;
};

OtaStatus ota_status;

class SharedStateLock {
 public:
  SharedStateLock() {
    if (shared_mutex != nullptr) {
      xSemaphoreTake(shared_mutex, portMAX_DELAY);
      locked_ = true;
    }
  }
  ~SharedStateLock() {
    if (locked_) xSemaphoreGive(shared_mutex);
  }
  SharedStateLock(const SharedStateLock&) = delete;
  SharedStateLock& operator=(const SharedStateLock&) = delete;

 private:
  bool locked_ = false;
};

DeviceConfig copy_config() {
  SharedStateLock lock;
  return config;
}

OtaStatus copy_ota_status() {
  SharedStateLock lock;
  return ota_status;
}

DNSServer portal_dns;
WebServer portal_server(80);
bool portal_handlers_registered = false;
bool portal_active = false;
uint32_t portal_last_activity_ms = 0;
String portal_ssid;
String portal_password;
String portal_session;
String portal_csrf;

#if QUOTA_HAS_TOUCH
SPIClass touch_spi(HSPI);
XPT2046_Touchscreen touch(PIN_TOUCH_CS, PIN_TOUCH_IRQ);

struct TouchState {
  bool last_down = false;
  bool long_sent = false;
  bool swallow_release = false;
  uint32_t down_since = 0;
  int16_t start_x = 0;
};

TouchState touch_state;
#endif

void start_configuration_portal();

class BoundedHttpSink : public Stream {
 public:
  explicit BoundedHttpSink(std::size_t limit) : buffer_(limit) {}

  size_t write(uint8_t value) override {
    return buffer_.append(value) ? 1U : 0U;
  }

  size_t write(const uint8_t* data, size_t size) override {
    return buffer_.append(data, size) ? size : 0U;
  }

  int available() override { return 0; }
  int read() override { return -1; }
  int peek() override { return -1; }
  void flush() override {}

  const quota_monitor::BoundedResponseBuffer& buffer() const { return buffer_; }

 private:
  quota_monitor::BoundedResponseBuffer buffer_;
};

#if QUOTA_HAS_MAX17048
uint16_t max17048_read16(uint8_t reg, bool& ok) {
  Wire.beginTransmission(MAX17048_ADDRESS);
  Wire.write(reg);
  if (Wire.endTransmission(false) != 0) {
    ok = false;
    return 0;
  }
  if (Wire.requestFrom(MAX17048_ADDRESS, static_cast<uint8_t>(2)) != 2) {
    ok = false;
    return 0;
  }
  ok = true;
  return static_cast<uint16_t>(Wire.read() << 8) | Wire.read();
}

float max17048_soc(bool& ok) {
  const uint16_t raw = max17048_read16(0x04, ok);
  if (!ok) return -1.0F;
  return std::clamp(static_cast<float>(raw >> 8) +
                        static_cast<float>(raw & 0xff) / 256.0F,
                    0.0F, 100.0F);
}

float max17048_voltage(bool& ok) {
  const uint16_t raw = max17048_read16(0x02, ok);
  if (!ok) return -1.0F;
  return static_cast<float>(raw >> 4) * 0.00125F;
}
#endif

void read_battery(float& soc, float& voltage, bool& ok) {
#if QUOTA_HAS_MAX17048
  bool soc_ok = false;
  soc = max17048_soc(soc_ok);
  bool voltage_ok = false;
  voltage = max17048_voltage(voltage_ok);
  ok = soc_ok && voltage_ok;
#else
  // The E32R28T schematic and vendor example use a 100k/100k divider into
  // ADC1 GPIO34. analogReadMilliVolts applies the ESP32's eFuse calibration;
  // median filtering suppresses Wi-Fi/display noise before multiplying by 2.
  constexpr std::size_t kSamples = 9;
  std::array<uint32_t, kSamples> samples{};
  for (auto& sample : samples) {
    sample = analogReadMilliVolts(PIN_BATTERY_ADC);
    delay(2);
  }
  std::sort(samples.begin(), samples.end());
  const uint32_t battery_mv = samples[kSamples / 2] * 2U;
  voltage = static_cast<float>(battery_mv) / 1000.0F;
  // Reject an open/invalid ADC reading. A charger with no battery can still
  // float high, so actual pack presence remains a physical validation item.
  ok = battery_mv >= 2500U && battery_mv <= 4500U;
  soc = ok ? quota_monitor::lipo_percent_from_voltage(voltage) : -1.0F;
#endif
}

void sample_battery_if_due() {
  if (next_battery_sample_ms != 0 &&
      static_cast<int32_t>(millis() - next_battery_sample_ms) < 0)
    return;
  read_battery(cached_battery_soc, cached_battery_voltage, cached_battery_ok);
  cached_battery_mv =
      cached_battery_ok
          ? static_cast<uint16_t>(std::clamp(
                static_cast<int>(cached_battery_voltage * 1000.0F + 0.5F),
                0, 65535))
          : 0U;
#if defined(QUOTA_BOARD_E32R28T)
  charge_trend_detector.update(millis(), cached_battery_mv, cached_battery_ok,
                               applied_backlight_pwm);
#endif
  next_battery_sample_ms = millis() + 5000U;
}

String masked(const String& value) {
  if (value.isEmpty()) return "<empty>";
  return "********";
}

bool brightness_allowed(uint32_t value) {
  return value == 30 || value == 60 || value == 100;
}

void apply_backlight_pwm(uint8_t pwm) {
  if (backlight_pwm_initialized && applied_backlight_pwm == pwm) return;
#if defined(QUOTA_BOARD_E32R28T)
  charge_trend_detector.note_load_transition(millis(), pwm);
#endif
  ledcWrite(0, pwm);
  applied_backlight_pwm = pwm;
  backlight_pwm_initialized = true;
}

void normalize_config(DeviceConfig& value) {
  value.refresh_seconds =
      std::clamp<uint32_t>(value.refresh_seconds, 5U, 3600U);
  if (!brightness_allowed(value.brightness_percent))
    value.brightness_percent = kDefaultBrightnessPercent;
  value.dim_after_seconds =
      std::min<uint32_t>(value.dim_after_seconds, 86400U);
  value.screen_off_after_seconds =
      std::min<uint32_t>(value.screen_off_after_seconds, 86400U);
  value.screen_off_refresh_seconds = std::clamp<uint32_t>(
      value.screen_off_refresh_seconds, 60U, 3600U);
#if QUOTA_HAS_TOUCH
  if (value.touch_x_min + 100U >= value.touch_x_max ||
      value.touch_y_min + 100U >= value.touch_y_max) {
    value.touch_x_min = 495;
    value.touch_x_max = 3398;
    value.touch_y_min = 721;
    value.touch_y_max = 3448;
  }
#endif
}

String config_slot_namespace(uint8_t slot) {
  return slot == 0 ? "qmoncfg0" : "qmoncfg1";
}

bool read_config_slot(uint8_t slot, DeviceConfig& value) {
  Preferences slot_preferences;
  if (!slot_preferences.begin(config_slot_namespace(slot).c_str(), true))
    return false;
  const bool valid = slot_preferences.getBool("valid", false);
  if (valid) {
    value.ssid = slot_preferences.getString("ssid", "");
    value.password = slot_preferences.getString("password", "");
    value.ssid2 = slot_preferences.getString("ssid2", "");
    value.password2 = slot_preferences.getString("password2", "");
    value.ssid3 = slot_preferences.getString("ssid3", "");
    value.password3 = slot_preferences.getString("password3", "");
    value.base_url = slot_preferences.getString("base_url", "");
    value.token = slot_preferences.getString("token", "");
    value.timezone = slot_preferences.getString("timezone", "CST-8");
    value.refresh_seconds =
        slot_preferences.getUInt("refresh", kDefaultRefreshSeconds);
    value.brightness_percent =
        slot_preferences.getUChar("brightness", kDefaultBrightnessPercent);
    value.dim_after_seconds =
        slot_preferences.getUInt("dim_sec", kDefaultDimSeconds);
    value.screen_off_after_seconds =
        slot_preferences.getUInt("off_sec", kDefaultScreenOffSeconds);
    value.screen_off_refresh_seconds = slot_preferences.getUInt(
        "off_refresh", kDefaultScreenOffRefreshSeconds);
    value.external_power_sense_enabled =
        slot_preferences.getBool("ext_power", false);
#if QUOTA_HAS_TOUCH
    value.touch_x_min = slot_preferences.getUShort("touch_x0", 495);
    value.touch_x_max = slot_preferences.getUShort("touch_x1", 3398);
    value.touch_y_min = slot_preferences.getUShort("touch_y0", 721);
    value.touch_y_max = slot_preferences.getUShort("touch_y1", 3448);
#endif
  }
  slot_preferences.end();
  if (valid) normalize_config(value);
  return valid;
}

bool write_config_slot(uint8_t slot, const DeviceConfig& value) {
  Preferences slot_preferences;
  if (!slot_preferences.begin(config_slot_namespace(slot).c_str(), false))
    return false;
  const bool invalidated =
      slot_preferences.putBool("valid", false) == sizeof(bool) &&
      !slot_preferences.getBool("valid", true);
  if (!invalidated) {
    slot_preferences.end();
    return false;
  }
  bool ok = slot_preferences.clear();
  if (ok) {
    ok &= slot_preferences.putBool("valid", false) == sizeof(bool);
    ok &= !slot_preferences.getBool("valid", true);
  }
  ok &= slot_preferences.putString("ssid", value.ssid) == value.ssid.length();
  ok &= slot_preferences.putString("password", value.password) ==
        value.password.length();
  ok &= slot_preferences.putString("ssid2", value.ssid2) ==
        value.ssid2.length();
  ok &= slot_preferences.putString("password2", value.password2) ==
        value.password2.length();
  ok &= slot_preferences.putString("ssid3", value.ssid3) ==
        value.ssid3.length();
  ok &= slot_preferences.putString("password3", value.password3) ==
        value.password3.length();
  ok &= slot_preferences.putString("base_url", value.base_url) ==
        value.base_url.length();
  ok &= slot_preferences.putString("token", value.token) == value.token.length();
  ok &= slot_preferences.putString("timezone", value.timezone) ==
        value.timezone.length();
  ok &= slot_preferences.putUInt("refresh", value.refresh_seconds) ==
        sizeof(uint32_t);
  ok &= slot_preferences.putUChar("brightness", value.brightness_percent) ==
        sizeof(uint8_t);
  ok &= slot_preferences.putUInt("dim_sec", value.dim_after_seconds) ==
        sizeof(uint32_t);
  ok &= slot_preferences.putUInt("off_sec", value.screen_off_after_seconds) ==
        sizeof(uint32_t);
  ok &= slot_preferences.putUInt("off_refresh",
                                 value.screen_off_refresh_seconds) ==
        sizeof(uint32_t);
  ok &= slot_preferences.putBool("ext_power",
                                 value.external_power_sense_enabled) ==
        sizeof(bool);
#if QUOTA_HAS_TOUCH
  ok &= slot_preferences.putUShort("touch_x0", value.touch_x_min) ==
        sizeof(uint16_t);
  ok &= slot_preferences.putUShort("touch_x1", value.touch_x_max) ==
        sizeof(uint16_t);
  ok &= slot_preferences.putUShort("touch_y0", value.touch_y_min) ==
        sizeof(uint16_t);
  ok &= slot_preferences.putUShort("touch_y1", value.touch_y_max) ==
        sizeof(uint16_t);
#endif
  if (ok) {
    ok &= slot_preferences.putBool("valid", true) == sizeof(bool);
    ok &= slot_preferences.getBool("valid", false);
  }
  slot_preferences.end();
  return ok;
}

void load_config() {
  Preferences metadata;
  const bool metadata_open = metadata.begin("qmoncfgmeta", false);
  uint8_t requested_slot = metadata_open ? metadata.getUChar("active", 0) : 0;
  if (requested_slot > 1) requested_slot = 0;
  const bool had_pending_commit =
      metadata_open && metadata.getBool("pending", false);
  const uint8_t pending_target =
      metadata_open ? metadata.getUChar("target", 0xff) : 0xff;
  if (metadata_open) metadata.end();

  const bool rollback_pending = had_pending_commit && pending_target <= 1;
  if (rollback_pending) requested_slot = pending_target ^ 1U;
  config_active_slot = requested_slot;

  DeviceConfig stored;
  bool loaded_slot = read_config_slot(requested_slot, stored);
  if (!loaded_slot && !rollback_pending) {
    requested_slot ^= 1U;
    loaded_slot = read_config_slot(requested_slot, stored);
  }

  preferences.begin("quota-monitor", true);
  if (loaded_slot) {
    config = stored;
    config_active_slot = requested_slot;
  } else {
    // One-time migration from the v0.2 namespace. It remains intact until the
    // first successful dual-slot commit, providing an additional fallback.
    config.ssid = preferences.getString("ssid", "");
    config.password = preferences.getString("password", "");
    config.ssid2 = preferences.getString("ssid2", "");
    config.password2 = preferences.getString("password2", "");
    config.ssid3 = preferences.getString("ssid3", "");
    config.password3 = preferences.getString("password3", "");
    config.base_url = preferences.getString("base_url", "");
    config.token = preferences.getString("token", "");
    config.timezone = preferences.getString("timezone", "CST-8");
    config.refresh_seconds =
        preferences.getUInt("refresh", kDefaultRefreshSeconds);
    config.external_power_sense_enabled =
        preferences.getBool("ext_power", false);
#if QUOTA_HAS_TOUCH
    config.touch_x_min = preferences.getUShort("touch_x0", 495);
    config.touch_x_max = preferences.getUShort("touch_x1", 3398);
    config.touch_y_min = preferences.getUShort("touch_y0", 721);
    config.touch_y_max = preferences.getUShort("touch_y1", 3448);
#endif
  }
  last_accepted_generated_epoch =
      static_cast<std::int64_t>(preferences.getULong64("last_epoch", 0));
  const String cached_snapshot = preferences.getString("snapshot", "");
  preferences.end();

  if (!cached_snapshot.isEmpty()) {
    quota_monitor::Snapshot restored;
    std::string cache_error;
    if (quota_monitor::parse_snapshot(cached_snapshot.c_str(),
                                      cached_snapshot.length(), restored,
                                      cache_error)) {
      snapshot = std::move(restored);
      have_snapshot = true;
      have_persisted_snapshot_cache = true;
      last_accepted_generated_epoch = std::max(
          last_accepted_generated_epoch, snapshot.generated_at_epoch);
    } else {
      last_error = "cached snapshot invalid";
    }
  }
  last_persisted_generated_epoch = last_accepted_generated_epoch;
  normalize_config(config);
  committed_config = config;
  if (had_pending_commit) {
    bool recovered = metadata.begin("qmoncfgmeta", false);
    if (recovered) {
      recovered &= metadata.putUChar("active", config_active_slot) ==
                   sizeof(uint8_t);
      recovered &= metadata.putBool("pending", false) == sizeof(bool);
      recovered &= metadata.getUChar("active", 0xff) == config_active_slot;
      recovered &= !metadata.getBool("pending", true);
      metadata.end();
    }
    Serial.println(recovered
                       ? "Recovered configuration after interrupted commit"
                       : "Configuration recovery metadata write failed");
  }
}

bool save_config(const DeviceConfig& requested) {
  SharedStateLock lock;
  DeviceConfig value = requested;
  normalize_config(value);
  const uint8_t target_slot = config_active_slot ^ 1U;
  Preferences metadata;
  if (!metadata.begin("qmoncfgmeta", false)) return false;
  bool marker_written =
      metadata.putUChar("target", target_slot) == sizeof(uint8_t);
  marker_written &= metadata.putBool("pending", true) == sizeof(bool);
  marker_written &= metadata.getUChar("target", 0xff) == target_slot;
  marker_written &= metadata.getBool("pending", false);
  metadata.end();
  if (!marker_written || !write_config_slot(target_slot, value)) return false;

  if (!metadata.begin("qmoncfgmeta", false)) return false;
  bool committed = metadata.putUChar("active", target_slot) == sizeof(uint8_t);
  committed &= metadata.putBool("pending", false) == sizeof(bool);
  committed &= metadata.getUChar("active", 0xff) == target_slot;
  committed &= !metadata.getBool("pending", true);
  metadata.end();
  if (!committed) return false;
  config_active_slot = target_slot;
  committed_config = value;
  config_dirty = false;
  network_config_dirty = false;
  return true;
}

bool verify_nvs_health() {
  Preferences health;
  if (!health.begin("qmonhealth", false)) return false;
  const uint32_t probe = esp_random();
  const bool ok = health.putUInt("probe", probe) == sizeof(uint32_t) &&
                  health.getUInt("probe", probe ^ 0xffffffffU) == probe;
  health.remove("probe");
  health.end();
  return ok;
}

void clear_config() {
  SharedStateLock lock;
  reset_in_progress = true;
  ++snapshot_generation;
  preferences.begin("quota-monitor", false);
  preferences.clear();
  preferences.end();
  for (const char* name : {"qmoncfg0", "qmoncfg1", "qmoncfgmeta",
                           "qmonhealth"}) {
    Preferences item;
    item.begin(name, false);
    item.clear();
    item.end();
  }
  config = {};
  committed_config = {};
  config_active_slot = 0;
  config_dirty = false;
  network_config_dirty = false;
  last_accepted_generated_epoch = 0;
  last_persisted_generated_epoch = 0;
  snapshot = {};
  have_snapshot = false;
  have_persisted_snapshot_cache = false;
  fetch_result_ready = false;
  WiFi.disconnect(true, true);
}

void persist_snapshot_state(bool force, uint32_t expected_generation = 0) {
  SharedStateLock lock;
  if (reset_in_progress ||
      (expected_generation != 0 &&
       expected_generation != snapshot_generation))
    return;
  std::int64_t accepted_epoch = 0;
  std::int64_t persisted_epoch = 0;
  bool persisted_cache = false;
  bool snapshot_available = false;
  quota_monitor::Snapshot current_snapshot;
  accepted_epoch = last_accepted_generated_epoch;
  persisted_epoch = last_persisted_generated_epoch;
  persisted_cache = have_persisted_snapshot_cache;
  snapshot_available = have_snapshot;
  if (snapshot_available) current_snapshot = snapshot;
  if (accepted_epoch <= 0) return;
  if (force && accepted_epoch == persisted_epoch && persisted_cache)
    return;
  if (!force && persisted_epoch > 0 &&
      accepted_epoch - persisted_epoch <
          kSnapshotPersistIntervalSeconds)
    return;

  std::string cache_json;
  std::string cache_error;
  const bool cache_ready =
      snapshot_available && quota_monitor::serialize_snapshot_cache(
                                current_snapshot, cache_json, cache_error);
  Preferences state_preferences;
  if (!state_preferences.begin("quota-monitor", false)) {
    Serial.println("Snapshot cache open failed");
    return;
  }
  bool cache_written = persisted_cache;
  if (cache_ready) {
    cache_written = state_preferences.putString("snapshot", cache_json.c_str()) ==
                    cache_json.size();
  }
  const bool epoch_written =
      state_preferences.putULong64("last_epoch",
                                   static_cast<uint64_t>(accepted_epoch)) ==
      sizeof(uint64_t);
  state_preferences.end();
  if (!reset_in_progress &&
      (expected_generation == 0 ||
       expected_generation == snapshot_generation) &&
      last_accepted_generated_epoch == accepted_epoch) {
    if (cache_ready && cache_written) have_persisted_snapshot_cache = true;
    if (epoch_written) last_persisted_generated_epoch = accepted_epoch;
  }
  if (!cache_error.empty()) Serial.println(("Snapshot cache: " + cache_error).c_str());
  if ((cache_ready && !cache_written) || !epoch_written)
    Serial.println("Snapshot cache write failed");
}

void flush_display(lv_disp_drv_t* drv, const lv_area_t* area,
                   lv_color_t* color_p) {
  const uint32_t width = area->x2 - area->x1 + 1;
  const uint32_t height = area->y2 - area->y1 + 1;
  tft.startWrite();
  tft.setAddrWindow(area->x1, area->y1, width, height);
  tft.pushColors(reinterpret_cast<uint16_t*>(&color_p->full), width * height,
                 true);
  tft.endWrite();
  lv_disp_flush_ready(drv);
}

lv_obj_t* make_line(lv_obj_t* parent, const lv_point_t* points,
                    uint16_t point_count, int x, int y, uint32_t color,
                    int width) {
  lv_obj_t* line = lv_line_create(parent);
  lv_line_set_points(line, points, point_count);
  lv_obj_set_pos(line, x, y);
  lv_obj_set_style_line_color(line, lv_color_hex(color), 0);
  lv_obj_set_style_line_width(line, width, 0);
  lv_obj_set_style_line_rounded(line, true, 0);
  return line;
}

lv_obj_t* make_card(lv_obj_t* parent, const char* title, uint32_t accent,
                    int x, lv_obj_t** title_label, lv_obj_t** status,
                    lv_obj_t** five_label, lv_obj_t** five_bar,
                    lv_obj_t** seven_label, lv_obj_t** seven_bar) {
  lv_obj_t* card = lv_obj_create(parent);
  lv_obj_set_size(card, 154, 195);
  lv_obj_set_pos(card, x, 39);
  lv_obj_clear_flag(card, LV_OBJ_FLAG_SCROLLABLE);
  lv_obj_set_style_radius(card, 10, 0);
  lv_obj_set_style_pad_all(card, 7, 0);
  lv_obj_set_style_bg_color(card, lv_color_hex(0x0c1b35), 0);
  // Keep the same navy card color and geometry, but suppress more of the
  // detailed background image so 16 px glyph edges stay distinct on the LCD.
  lv_obj_set_style_bg_opa(card, static_cast<lv_opa_t>(235), 0);
  lv_obj_set_style_border_color(card, lv_color_hex(accent), 0);
  lv_obj_set_style_border_opa(card, static_cast<lv_opa_t>(205), 0);
  lv_obj_set_style_border_width(card, 1, 0);
  lv_obj_set_style_shadow_color(card, lv_color_hex(0x020617), 0);
  lv_obj_set_style_shadow_opa(card, static_cast<lv_opa_t>(150), 0);
  lv_obj_set_style_shadow_width(card, 8, 0);
  lv_obj_set_style_text_color(card, lv_color_hex(0xf8fafc), 0);

  *title_label = lv_label_create(card);
  lv_label_set_text(*title_label, title);
  lv_obj_set_width(*title_label, 138);
  lv_label_set_long_mode(*title_label, LV_LABEL_LONG_CLIP);
  lv_obj_set_style_text_font(*title_label, &lv_font_qmon_16, 0);
  lv_obj_set_style_text_color(*title_label, lv_color_hex(accent), 0);
  lv_obj_set_style_text_opa(*title_label, LV_OPA_COVER, 0);

  *status = lv_label_create(card);
  lv_obj_set_width(*status, 138);
  lv_obj_set_pos(*status, 0, 23);
  lv_label_set_long_mode(*status, LV_LABEL_LONG_CLIP);
  lv_obj_set_style_text_align(*status, LV_TEXT_ALIGN_RIGHT, 0);

  *five_label = lv_label_create(card);
  lv_obj_set_width(*five_label, 138);
  lv_label_set_long_mode(*five_label, LV_LABEL_LONG_CLIP);
  lv_obj_set_pos(*five_label, 0, 45);
  *five_bar = lv_bar_create(card);
  lv_obj_set_size(*five_bar, 138, 14);
  lv_obj_set_pos(*five_bar, 0, 86);
  lv_bar_set_range(*five_bar, 0, 100);
  lv_obj_set_style_radius(*five_bar, 7, LV_PART_MAIN);
  lv_obj_set_style_radius(*five_bar, 7, LV_PART_INDICATOR);
  lv_obj_set_style_bg_color(*five_bar, lv_color_hex(0x263650), LV_PART_MAIN);
  lv_obj_set_style_bg_opa(*five_bar, LV_OPA_COVER, LV_PART_MAIN);

  *seven_label = lv_label_create(card);
  lv_obj_set_width(*seven_label, 138);
  lv_label_set_long_mode(*seven_label, LV_LABEL_LONG_CLIP);
  lv_obj_set_pos(*seven_label, 0, 108);
  *seven_bar = lv_bar_create(card);
  lv_obj_set_size(*seven_bar, 138, 14);
  lv_obj_set_pos(*seven_bar, 0, 149);
  lv_bar_set_range(*seven_bar, 0, 100);
  lv_obj_set_style_radius(*seven_bar, 7, LV_PART_MAIN);
  lv_obj_set_style_radius(*seven_bar, 7, LV_PART_INDICATOR);
  lv_obj_set_style_bg_color(*seven_bar, lv_color_hex(0x263650), LV_PART_MAIN);
  lv_obj_set_style_bg_opa(*seven_bar, LV_OPA_COVER, LV_PART_MAIN);
  return card;
}

void create_ui() {
  lv_obj_t* screen = lv_scr_act();
  lv_obj_clear_flag(screen, LV_OBJ_FLAG_SCROLLABLE);
  lv_obj_set_style_bg_color(screen, lv_color_hex(0x071225), 0);
  lv_obj_set_style_text_color(screen, lv_color_hex(0xf8fafc), 0);
  lv_obj_set_style_text_font(screen, &lv_font_qmon_16, 0);
  lv_obj_set_style_pad_all(screen, 0, 0);

  const std::size_t embedded_size =
      static_cast<std::size_t>(qmon_background_end - qmon_background_start);
  if (embedded_size == kBackgroundBytes) {
    background_image.header.always_zero = 0;
    background_image.header.w = kScreenWidth;
    background_image.header.h = kScreenHeight;
    background_image.header.cf = LV_IMG_CF_TRUE_COLOR;
    background_image.data_size = kBackgroundBytes;
    background_image.data = qmon_background_start;
    lv_obj_t* background = lv_img_create(screen);
    lv_img_set_src(background, &background_image);
    lv_obj_set_pos(background, 0, 0);
    lv_obj_clear_flag(background, LV_OBJ_FLAG_CLICKABLE);
    lv_obj_clear_flag(background, LV_OBJ_FLAG_SCROLLABLE);
  } else {
    Serial.printf("Background size invalid: %u (expected %u)\n",
                  static_cast<unsigned>(embedded_size),
                  static_cast<unsigned>(kBackgroundBytes));
  }

  lv_obj_t* topbar = lv_obj_create(screen);
  lv_obj_set_size(topbar, kScreenWidth, 35);
  lv_obj_set_pos(topbar, 0, 0);
  lv_obj_clear_flag(topbar, LV_OBJ_FLAG_SCROLLABLE);
  lv_obj_set_style_radius(topbar, 0, 0);
  lv_obj_set_style_pad_all(topbar, 0, 0);
  lv_obj_set_style_bg_color(topbar, lv_color_hex(0x061126), 0);
  lv_obj_set_style_bg_opa(topbar, static_cast<lv_opa_t>(225), 0);
  lv_obj_set_style_border_width(topbar, 0, 0);

  const int wifi_sizes[] = {24, 17, 10};
  const int wifi_x[] = {7, 11, 14};
  const int wifi_y[] = {4, 8, 12};
  for (std::size_t i = 0; i < wifi_arcs.size(); ++i) {
    wifi_arcs[i] = lv_arc_create(topbar);
    lv_obj_set_size(wifi_arcs[i], wifi_sizes[i], wifi_sizes[i]);
    lv_obj_set_pos(wifi_arcs[i], wifi_x[i], wifi_y[i]);
    lv_arc_set_bg_angles(wifi_arcs[i], 215, 325);
    lv_obj_set_style_arc_width(wifi_arcs[i], 2, LV_PART_MAIN);
    lv_obj_set_style_arc_opa(wifi_arcs[i], LV_OPA_TRANSP, LV_PART_INDICATOR);
    lv_obj_set_style_opa(wifi_arcs[i], LV_OPA_TRANSP, LV_PART_KNOB);
    lv_obj_clear_flag(wifi_arcs[i], LV_OBJ_FLAG_CLICKABLE);
  }
  wifi_label = lv_label_create(topbar);
  lv_label_set_text(wifi_label, "WiFi");
  lv_obj_set_pos(wifi_label, 35, 7);
  wifi_status_dot = lv_obj_create(topbar);
  lv_obj_set_size(wifi_status_dot, 10, 10);
  lv_obj_set_pos(wifi_status_dot, 75, 11);
  lv_obj_set_style_radius(wifi_status_dot, LV_RADIUS_CIRCLE, 0);
  lv_obj_set_style_pad_all(wifi_status_dot, 0, 0);
  lv_obj_set_style_border_width(wifi_status_dot, 0, 0);
  wifi_offline_cross_a =
      make_line(topbar, kWifiCrossAPoints, 2, 77, 13, 0xffffff, 1);
  wifi_offline_cross_b =
      make_line(topbar, kWifiCrossBPoints, 2, 77, 13, 0xffffff, 1);

  clock_face = lv_obj_create(topbar);
  lv_obj_set_size(clock_face, 20, 20);
  lv_obj_set_pos(clock_face, 108, 7);
  lv_obj_set_style_radius(clock_face, LV_RADIUS_CIRCLE, 0);
  lv_obj_set_style_bg_opa(clock_face, LV_OPA_TRANSP, 0);
  lv_obj_set_style_border_width(clock_face, 2, 0);
  lv_obj_set_style_border_color(clock_face, lv_color_hex(0xdbeafe), 0);
  lv_obj_set_style_pad_all(clock_face, 0, 0);
  make_line(topbar, kClockHourPoints, 2, 117, 13, 0xdbeafe, 2);
  make_line(topbar, kClockMinutePoints, 2, 117, 17, 0xdbeafe, 2);
  age_label = lv_label_create(topbar);
  lv_obj_set_width(age_label, 70);
  lv_obj_set_pos(age_label, 132, 7);
  lv_obj_set_style_text_align(age_label, LV_TEXT_ALIGN_LEFT, 0);

  battery_outline = lv_obj_create(topbar);
  lv_obj_set_size(battery_outline, 26, 14);
  lv_obj_set_pos(battery_outline, 241, 10);
  lv_obj_set_style_radius(battery_outline, 3, 0);
  lv_obj_set_style_bg_opa(battery_outline, LV_OPA_TRANSP, 0);
  lv_obj_set_style_border_width(battery_outline, 2, 0);
  lv_obj_set_style_border_color(battery_outline, lv_color_hex(0xf8fafc), 0);
  lv_obj_set_style_pad_all(battery_outline, 0, 0);
  battery_fill = lv_obj_create(battery_outline);
  lv_obj_set_size(battery_fill, 20, 8);
  lv_obj_set_pos(battery_fill, 1, 1);
  lv_obj_set_style_radius(battery_fill, 1, 0);
  lv_obj_set_style_border_width(battery_fill, 0, 0);
  lv_obj_set_style_pad_all(battery_fill, 0, 0);
  lv_obj_t* battery_terminal = lv_obj_create(topbar);
  lv_obj_set_size(battery_terminal, 3, 6);
  lv_obj_set_pos(battery_terminal, 268, 14);
  lv_obj_set_style_radius(battery_terminal, 1, 0);
  lv_obj_set_style_bg_color(battery_terminal, lv_color_hex(0xf8fafc), 0);
  lv_obj_set_style_border_width(battery_terminal, 0, 0);
  lv_obj_set_style_pad_all(battery_terminal, 0, 0);
  battery_label = lv_label_create(topbar);
  lv_obj_set_width(battery_label, 43);
  lv_obj_set_pos(battery_label, 274, 7);
  lv_obj_set_style_text_align(battery_label, LV_TEXT_ALIGN_RIGHT, 0);

  make_card(screen, "CODEX --", kCodexAccent, 4, &codex_title_label,
            &codex_status_label, &codex_5h_label, &codex_5h_bar,
            &codex_7d_label, &codex_7d_bar);
  make_card(screen, "CLAUDE --", kClaudeAccent, 162, &claude_title_label,
            &claude_status_label, &claude_5h_label, &claude_5h_bar,
            &claude_7d_label, &claude_7d_bar);

  message_panel = lv_obj_create(screen);
  lv_obj_set_size(message_panel, 300, 72);
  lv_obj_align(message_panel, LV_ALIGN_CENTER, 0, 5);
  lv_obj_clear_flag(message_panel, LV_OBJ_FLAG_SCROLLABLE);
  lv_obj_set_style_radius(message_panel, 10, 0);
  lv_obj_set_style_bg_color(message_panel, lv_color_hex(0x07111f), 0);
  lv_obj_set_style_bg_opa(message_panel, static_cast<lv_opa_t>(238), 0);
  lv_obj_set_style_border_color(message_panel, lv_color_hex(0xf59e0b), 0);
  lv_obj_set_style_border_width(message_panel, 1, 0);
  lv_obj_set_style_pad_all(message_panel, 8, 0);
  message_label = lv_label_create(message_panel);
  lv_obj_set_width(message_label, 282);
  lv_label_set_long_mode(message_label, LV_LABEL_LONG_WRAP);
  lv_obj_set_style_text_align(message_label, LV_TEXT_ALIGN_CENTER, 0);
  lv_obj_set_style_text_color(message_label, lv_color_hex(0xfef3c7), 0);
  lv_obj_center(message_label);
  lv_obj_add_flag(message_panel, LV_OBJ_FLAG_HIDDEN);
}

String local_reset_time(std::int64_t epoch) {
  if (epoch <= 0) return "--";
  const time_t value = static_cast<time_t>(epoch);
  struct tm local {};
  localtime_r(&value, &local);
  char output[20];
  if (strftime(output, sizeof(output), "%m/%d %H:%M", &local) == 0) return "--";
  return String(output);
}

void set_bar(lv_obj_t* label, lv_obj_t* bar,
             const quota_monitor::RateWindow& window, const char* name,
             bool degraded, uint32_t accent) {
  if (!window.present) {
    const std::string reset_line = quota_monitor::format_reset_line("--");
    lv_label_set_text_fmt(label, "%s  N/A\n%s", name, reset_line.c_str());
    lv_obj_set_style_text_color(label, lv_color_hex(0xe2e8f0), 0);
    lv_bar_set_value(bar, 0, LV_ANIM_OFF);
    lv_obj_set_style_bg_color(bar, lv_color_hex(0x64748b), LV_PART_INDICATOR);
    return;
  }
  const int remaining = std::clamp(
      static_cast<int>(window.remaining_percent + 0.5F), 0, 100);
  const String reset =
      window.has_reset ? local_reset_time(window.resets_at_epoch) : "未开始";
  const std::string reset_line =
      quota_monitor::format_reset_line(reset.c_str());
  // The card's content width is only 136 px. Keep the percentage and reset
  // timestamp on separate lines, and label the second line explicitly so it
  // cannot be mistaken for the observation time.
  lv_label_set_text_fmt(label, "%s  剩%d%%\n%s", name, remaining,
                        reset_line.c_str());
  lv_bar_set_value(bar, remaining, LV_ANIM_OFF);
  const uint32_t text_color =
      degraded ? 0xe2e8f0 : (remaining < 20 ? 0xffd0d0 : 0xffffff);
  const uint32_t bar_color =
      degraded ? 0x64748b : (remaining < 20 ? 0xef4444 : accent);
  lv_obj_set_style_text_color(label, lv_color_hex(text_color), 0);
  lv_obj_set_style_bg_color(bar, lv_color_hex(bar_color), LV_PART_INDICATOR);
}

String display_plan(const std::string& raw, bool codex_provider) {
  String value(raw.c_str());
  value.trim();
  String compact;
  for (unsigned int i = 0; i < value.length(); ++i) {
    char c = value[i];
    if (c >= 'A' && c <= 'Z') c = static_cast<char>(c - 'A' + 'a');
    if ((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')) compact += c;
  }
  if (compact.isEmpty()) return "--";
  if (compact == "none" || compact == "unsubscribed" ||
      compact == "loginrequired" || compact == "unauthenticated" ||
      compact == "notloggedin")
    return "未登录";
  if (compact == "max20" || compact == "max20x") return "MAX 20";
  if (compact == "max5" || compact == "max5x") return "MAX 5";
  if (compact == "max") return "MAX";
  if (compact == "off" || compact == "disabled") return "OFF";
  if (compact == "free") return "FREE";
  if (codex_provider) {
    if (compact == "prolite" || compact == "pro5" || compact == "pro5x")
      return "PRO 5";
    if (compact == "pro" || compact == "chatgptpro" || compact == "pro20" ||
         compact == "pro20x")
      return "PRO 20";
  } else if (compact == "pro" || compact == "claudepro") {
    return "PRO";
  }
  if (compact == "plus" || compact == "chatgptplus") return "PLUS";
  if (compact == "team") return "TEAM";
  if (compact == "enterprise") return "ENT";
  if (compact == "business") return "BIZ";
  if (compact == "api" || compact == "payg") return "API";
  compact.toUpperCase();
  if (compact.length() > 8) compact.remove(8);
  return compact;
}

void set_provider(lv_obj_t* title, const char* provider_name,
                  lv_obj_t* status, lv_obj_t* five_label, lv_obj_t* five_bar,
                  lv_obj_t* seven_label, lv_obj_t* seven_bar,
                  const quota_monitor::ProviderSnapshot& provider,
                  bool snapshot_stale) {
  const bool unavailable = !provider.present || provider.freshness == "unavailable";
  const bool stale = !unavailable && (snapshot_stale || provider.freshness == "stale");
  const bool login_required = provider.login_required;
  const bool codex_provider = strcmp(provider_name, "CODEX") == 0;
  const String plan =
      login_required ? String("未登录")
                     : display_plan(provider.plan, codex_provider);
  const String title_text = String(provider_name) + " " + plan;
  lv_label_set_text(title, title_text.c_str());
  const char* state = login_required ? "AUTH" : (unavailable ? "离线" : (stale ? "过期" : "正常"));
  lv_label_set_text(status, state);
  const uint32_t status_color = login_required ? 0xfca5a5
                                 : unavailable ? 0xcbd5e1
                                 : stale     ? 0xcbd5e1
                                             : 0x86efac;
  lv_obj_set_style_text_color(status, lv_color_hex(status_color), 0);
  const uint32_t accent = codex_provider ? kCodexAccent : kClaudeAccent;
  const bool degraded = unavailable || stale;
  set_bar(five_label, five_bar, provider.five_hour, "5小时", degraded, accent);
  set_bar(seven_label, seven_bar, provider.seven_day, "7天", degraded, accent);
}

bool clock_is_sane() {
  return static_cast<std::int64_t>(time(nullptr)) >= kMinimumSaneClock;
}

void refresh_ui() {
  if (shared_mutex != nullptr) xSemaphoreTake(shared_mutex, portMAX_DELAY);
  const bool wifi_online = WiFi.status() == WL_CONNECTED;
  const uint32_t wifi_color = wifi_online ? 0x4ade80 : 0xf87171;
  for (lv_obj_t* arc : wifi_arcs)
    lv_obj_set_style_arc_color(arc, lv_color_hex(wifi_color), LV_PART_MAIN);
  lv_obj_set_style_text_color(wifi_label, lv_color_hex(0xf8fafc), 0);
  lv_obj_set_style_bg_color(wifi_status_dot, lv_color_hex(wifi_color), 0);
  if (wifi_online) {
    lv_obj_add_flag(wifi_offline_cross_a, LV_OBJ_FLAG_HIDDEN);
    lv_obj_add_flag(wifi_offline_cross_b, LV_OBJ_FLAG_HIDDEN);
  } else {
    lv_obj_clear_flag(wifi_offline_cross_a, LV_OBJ_FLAG_HIDDEN);
    lv_obj_clear_flag(wifi_offline_cross_b, LV_OBJ_FLAG_HIDDEN);
  }

  sample_battery_if_due();
  if (cached_battery_ok) {
    const int battery_percent =
        std::clamp(static_cast<int>(cached_battery_soc + 0.5F), 0, 100);
    const String battery_text = String(battery_percent) + "%";
    lv_label_set_text(battery_label, battery_text.c_str());
    lv_obj_set_width(battery_fill,
                     std::max(1, (20 * battery_percent + 99) / 100));
    lv_obj_clear_flag(battery_fill, LV_OBJ_FLAG_HIDDEN);
  } else {
    lv_label_set_text(battery_label, "N/A");
    lv_obj_add_flag(battery_fill, LV_OBJ_FLAG_HIDDEN);
  }
  const uint32_t battery_color =
      !cached_battery_ok
          ? 0x94a3b8
          : (cached_battery_soc < 20.0F ? 0xef4444 : 0x86efac);
  lv_obj_set_style_text_color(battery_label, lv_color_hex(battery_color), 0);
  lv_obj_set_style_border_color(battery_outline,
                                lv_color_hex(battery_color), 0);
  lv_obj_set_style_bg_color(battery_fill, lv_color_hex(battery_color), 0);

  if (have_snapshot) {
    const std::int64_t now_epoch = static_cast<std::int64_t>(time(nullptr));
    const std::int64_t age = clock_is_sane()
                                 ? std::max<std::int64_t>(0, now_epoch - snapshot.generated_at_epoch)
                                 : 0;
    const bool snapshot_stale = !clock_is_sane() || age > kStaleAfterSeconds;
    lv_label_set_text_fmt(age_label, "%llds%s", static_cast<long long>(age),
                           snapshot_stale ? "!" : "");
    const uint32_t age_color = snapshot_stale ? 0x94a3b8 : 0xdbeafe;
    lv_obj_set_style_text_color(age_label, lv_color_hex(age_color), 0);
    lv_obj_set_style_border_color(clock_face, lv_color_hex(age_color), 0);
    set_provider(codex_title_label, "CODEX", codex_status_label,
                 codex_5h_label, codex_5h_bar,
                 codex_7d_label, codex_7d_bar, snapshot.codex, snapshot_stale);
    set_provider(claude_title_label, "CLAUDE", claude_status_label,
                 claude_5h_label, claude_5h_bar,
                 claude_7d_label, claude_7d_bar, snapshot.claude, snapshot_stale);
  } else {
    lv_label_set_text(age_label, "--s");
    lv_obj_set_style_text_color(age_label, lv_color_hex(0x94a3b8), 0);
    lv_obj_set_style_border_color(clock_face, lv_color_hex(0x94a3b8), 0);
    quota_monitor::ProviderSnapshot unavailable;
    set_provider(codex_title_label, "CODEX", codex_status_label,
                 codex_5h_label, codex_5h_bar,
                 codex_7d_label, codex_7d_bar, unavailable, true);
    set_provider(claude_title_label, "CLAUDE", claude_status_label,
                 claude_5h_label, claude_5h_bar,
                 claude_7d_label, claude_7d_bar, unavailable, true);
  }

  if (message_until_ms != 0 &&
      static_cast<int32_t>(message_until_ms - millis()) > 0 &&
      !last_error.isEmpty()) {
    lv_label_set_text(message_label, last_error.c_str());
    lv_obj_clear_flag(message_panel, LV_OBJ_FLAG_HIDDEN);
    lv_obj_move_foreground(message_panel);
  } else {
    lv_label_set_text(message_label, "");
    lv_obj_add_flag(message_panel, LV_OBJ_FLAG_HIDDEN);
  }
  if (shared_mutex != nullptr) xSemaphoreGive(shared_mutex);
}

void show_message(const String& message, uint32_t duration_ms = 5000) {
  {
    SharedStateLock lock;
    last_error = message;
    message_until_ms = millis() + duration_ms;
  }
  refresh_ui();
}

#if QUOTA_ALLOW_LAN_HTTP
bool is_private_lan_http_url(const String& url) {
  if (!url.startsWith("http://")) return false;

  String authority = url.substring(7);
  const int path_start = authority.indexOf('/');
  if (path_start >= 0) authority.remove(path_start);
  if (authority.isEmpty() || authority.indexOf('@') >= 0 ||
      authority.indexOf('[') >= 0) {
    return false;
  }

  const int port_start = authority.indexOf(':');
  if (port_start >= 0) authority.remove(port_start);
  IPAddress address;
  if (!address.fromString(authority)) return false;

  return address[0] == 10 ||
         (address[0] == 172 && address[1] >= 16 && address[1] <= 31) ||
         (address[0] == 192 && address[1] == 168);
}
#endif

bool config_ready(const DeviceConfig& candidate, String& why) {
  if (candidate.ssid.isEmpty()) {
    why = "SSID missing";
    return false;
  }
  if (!quota_monitor::wifi_profile_ssids_valid(
          candidate.ssid.c_str(), candidate.ssid2.c_str(),
          candidate.ssid3.c_str())) {
    why = "Wi-Fi SSIDs must be unique";
    return false;
  }
  const bool https_url = candidate.base_url.startsWith("https://");
#if QUOTA_ALLOW_LAN_HTTP
  const bool allowed_lan_http = is_private_lan_http_url(candidate.base_url);
#else
  constexpr bool allowed_lan_http = false;
#endif
  if (!https_url && !allowed_lan_http) {
#if QUOTA_ALLOW_LAN_HTTP
    why = "HTTPS or private-LAN HTTP required";
#else
    why = "HTTPS URL required";
#endif
    return false;
  }
  const int authority_start = candidate.base_url.indexOf("://") + 3;
  int authority_end = candidate.base_url.indexOf('/', authority_start);
  if (authority_end < 0) authority_end = candidate.base_url.length();
  const String authority =
      candidate.base_url.substring(authority_start, authority_end);
  if (authority_start < 3 || authority.isEmpty() || authority.indexOf('@') >= 0 ||
      candidate.base_url.indexOf('#') >= 0 ||
      candidate.base_url.indexOf('?') >= 0) {
    why = "server URL invalid";
    return false;
  }
  if (candidate.token.length() < 16) {
    why = "display token missing";
    return false;
  }
#if QUOTA_HAS_TOUCH
  if (candidate.touch_x_min + 100U >= candidate.touch_x_max ||
      candidate.touch_y_min + 100U >= candidate.touch_y_max) {
    why = "touch calibration invalid";
    return false;
  }
#endif
  return true;
}

bool config_ready(String& why) { return config_ready(config, why); }

String snapshot_url(const DeviceConfig& candidate) {
  String url = candidate.base_url;
  while (url.endsWith("/")) url.remove(url.length() - 1);
  if (!url.endsWith("/api/v1/display/snapshot"))
    url += "/api/v1/display/snapshot";
  return url;
}

String snapshot_url() { return snapshot_url(config); }

String server_origin(const DeviceConfig& candidate) {
  const int scheme_end = candidate.base_url.indexOf("://");
  if (scheme_end < 0) return "";
  const int authority_start = scheme_end + 3;
  int authority_end = candidate.base_url.indexOf('/', authority_start);
  if (authority_end < 0) authority_end = candidate.base_url.length();
  const String authority =
      candidate.base_url.substring(authority_start, authority_end);
  if (authority.isEmpty() || authority.indexOf('@') >= 0 ||
      authority.indexOf('?') >= 0 || authority.indexOf('#') >= 0)
    return "";
  return candidate.base_url.substring(0, authority_start) + authority;
}

// Arduino-ESP32 2.x uses Mbed TLS 2.28, which verifies a numeric URL host as
// a dNSName rather than an iPAddress SAN. The device endpoint therefore uses
// a leaf containing both SAN forms. Keep its private trust anchor isolated to
// the dedicated TLS port; normal HTTPS continues to trust only ISRG Root X1.
bool uses_device_tls_ca(const String& url) {
  constexpr char kHttpsPrefix[] = "https://";
  if (!url.startsWith(kHttpsPrefix)) return false;

  constexpr int kAuthorityStart = sizeof(kHttpsPrefix) - 1;
  int authority_end = url.indexOf('/', kAuthorityStart);
  if (authority_end < 0) authority_end = url.length();
  const String authority = url.substring(kAuthorityStart, authority_end);
  if (authority.isEmpty() || authority.indexOf('@') >= 0) return false;

  // Both IPv4/hostname authorities (host:18788) and bracketed IPv6
  // authorities ([addr]:18788) are handled by the same unambiguous suffix.
  return authority.endsWith(":18788");
}

bool fetch_snapshot_for(const DeviceConfig& candidate,
                        std::int64_t replay_epoch,
                        quota_monitor::Snapshot& parsed, String& error) {
  if (WiFi.status() != WL_CONNECTED) {
    error = "WiFi offline";
    return false;
  }
  String why;
  if (!config_ready(candidate, why)) {
    error = why;
    return false;
  }

  const String url = snapshot_url(candidate);
  HTTPClient http;
  http.setConnectTimeout(8000);
  http.setTimeout(10000);
  http.setFollowRedirects(HTTPC_DISABLE_FOLLOW_REDIRECTS);

  bool began = false;
#if QUOTA_ALLOW_LAN_HTTP
  WiFiClient plain;
  if (url.startsWith("http://")) began = http.begin(plain, url);
#endif
  WiFiClientSecure secure;
  if (url.startsWith("https://")) {
    const uint8_t* trust_anchor = uses_device_tls_ca(url)
                                      ? device_root_ca_start
                                      : isrg_root_x1_start;
    secure.setCACert(reinterpret_cast<const char*>(trust_anchor));
    began = http.begin(secure, url);
  }
  if (!began) {
    error = "HTTP begin failed";
    return false;
  }
  http.addHeader("Authorization", "Bearer " + candidate.token);
  http.addHeader("Accept", "application/json");
  http.addHeader("User-Agent", "quota-display/" QUOTA_MONITOR_FIRMWARE_VERSION);
  const int code = http.GET();
  if (code != HTTP_CODE_OK) {
    error = "HTTP " + String(code);
    http.end();
    return false;
  }
  const int content_length = http.getSize();
  if (content_length > static_cast<int>(kMaxResponseBytes)) {
    error = "response too large";
    http.end();
    return false;
  }
  BoundedHttpSink sink(kMaxResponseBytes);
  const int bytes_read = http.writeToStream(&sink);
  http.end();
  if (sink.buffer().overflowed()) {
    error = "response too large";
    return false;
  }
  if (bytes_read < 0) {
    error = "response read failed";
    return false;
  }
  const std::string& body = sink.buffer().value();

  std::string parse_error;
  if (!quota_monitor::parse_snapshot(body.data(), body.size(), parsed,
                                      parse_error)) {
    error = "JSON: " + String(parse_error.c_str());
    return false;
  }
  const std::int64_t now_epoch = static_cast<std::int64_t>(time(nullptr));
  const auto time_validation = quota_monitor::validate_snapshot_time(
      parsed.generated_at_epoch, now_epoch, replay_epoch,
      kStaleAfterSeconds, 120);
  if (!time_validation.accepted) {
    error = "TIME: " + String(time_validation.error.c_str());
    return false;
  }
  error.clear();
  return true;
}

bool fetch_snapshot(String& error) {
  DeviceConfig current_config;
  std::int64_t replay_epoch = 0;
  uint32_t generation = 0;
  {
    SharedStateLock lock;
    if (reset_in_progress) {
      error = "request superseded";
      return false;
    }
    current_config = config;
    replay_epoch = last_accepted_generated_epoch;
    generation = snapshot_generation;
  }
  quota_monitor::Snapshot parsed;
  if (!fetch_snapshot_for(current_config, replay_epoch, parsed, error))
    return false;
  {
    SharedStateLock lock;
    if (reset_in_progress || generation != snapshot_generation) {
      error = "request superseded";
      return false;
    }
    snapshot = std::move(parsed);
    have_snapshot = true;
    last_success_ms = millis();
    last_accepted_generated_epoch = snapshot.generated_at_epoch;
    last_error = "";
    message_until_ms = 0;
  }
  persist_snapshot_state(false, generation);
  return true;
}

void request_wifi_now() {
  const DeviceConfig current_config = copy_config();
  wifi_failover_policy.configure(!current_config.ssid.isEmpty(),
                                 !current_config.ssid2.isEmpty(),
                                 !current_config.ssid3.isEmpty());
  wifi_failover_policy.manual_reset(millis());
  WiFi.disconnect();
}

const String& wifi_ssid_for_profile(const DeviceConfig& current_config,
                                    quota_monitor::WifiProfile profile) {
  if (profile == quota_monitor::WifiProfile::kBackup1)
    return current_config.ssid2;
  if (profile == quota_monitor::WifiProfile::kBackup2)
    return current_config.ssid3;
  return current_config.ssid;
}

const String& wifi_password_for_profile(const DeviceConfig& current_config,
                                        quota_monitor::WifiProfile profile) {
  if (profile == quota_monitor::WifiProfile::kBackup1)
    return current_config.password2;
  if (profile == quota_monitor::WifiProfile::kBackup2)
    return current_config.password3;
  return current_config.password;
}

void service_wifi() {
  DeviceConfig current_config;
  bool network_reserved = false;
  {
    SharedStateLock lock;
    current_config = config;
    network_reserved = candidate_test_in_flight || ota_status.installing;
  }
  if (portal_active || network_reserved || network_config_dirty) return;

  wifi_failover_policy.configure(!current_config.ssid.isEmpty(),
                                 !current_config.ssid2.isEmpty(),
                                 !current_config.ssid3.isEmpty());
  const bool connected = WiFi.status() == WL_CONNECTED;
  if (connected) {
    const String connected_ssid = WiFi.SSID();
    if (connected_ssid == current_config.ssid) {
      wifi_failover_policy.note_connected(
          quota_monitor::WifiProfile::kPrimary);
    } else if (!current_config.ssid2.isEmpty() &&
               connected_ssid == current_config.ssid2) {
      wifi_failover_policy.note_connected(
          quota_monitor::WifiProfile::kBackup1);
    } else if (!current_config.ssid3.isEmpty() &&
               connected_ssid == current_config.ssid3) {
      wifi_failover_policy.note_connected(
          quota_monitor::WifiProfile::kBackup2);
    }
  }

  const quota_monitor::WifiFailoverDecision decision =
      wifi_failover_policy.update(millis(), connected);
  if (decision.action == quota_monitor::WifiFailoverAction::kStartProfile) {
    const String& ssid = wifi_ssid_for_profile(current_config, decision.profile);
    const String& password =
        wifi_password_for_profile(current_config, decision.profile);
    WiFi.disconnect(false, false);
    WiFi.mode(WIFI_STA);
    WiFi.begin(ssid.c_str(), password.c_str());
  } else if (decision.action ==
             quota_monitor::WifiFailoverAction::kRoundFailed) {
    // Stop the final timed-out association while the pure policy observes the
    // whole-round backoff. The display and cached quota data remain usable.
    WiFi.disconnect(false, false);
  }
}

bool queue_network_job(NetworkJob job) {
  return network_jobs != nullptr && xQueueSend(network_jobs, &job, 0) == pdTRUE;
}

String random_alphanumeric(size_t length) {
  static constexpr char alphabet[] =
      "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789";
  String result;
  result.reserve(length);
  for (size_t i = 0; i < length; ++i)
    result += alphabet[esp_random() % (sizeof(alphabet) - 1U)];
  return result;
}

void add_portal_security_headers() {
  portal_server.sendHeader("Cache-Control", "no-store");
  portal_server.sendHeader("X-Content-Type-Options", "nosniff");
  portal_server.sendHeader("X-Frame-Options", "DENY");
  portal_server.sendHeader("Referrer-Policy", "no-referrer");
  portal_server.sendHeader(
      "Content-Security-Policy",
      "default-src 'none'; style-src 'unsafe-inline'; script-src "
      "'unsafe-inline'; connect-src 'self'; form-action 'self'; frame-ancestors "
      "'none'");
}

bool portal_subnet_request() {
  if (!portal_active) return false;
  WiFiClient client = portal_server.client();
  const IPAddress remote = client.remoteIP();
  const IPAddress local = client.localIP();
  const IPAddress ap = WiFi.softAPIP();
  return local == ap && remote[0] == ap[0] && remote[1] == ap[1] &&
         remote[2] == ap[2];
}

bool portal_session_valid() {
  return portal_server.header("Cookie").indexOf("QMONSID=" + portal_session) >=
         0;
}

void portal_touch() {
  portal_last_activity_ms = millis();
  const OtaStatus status = copy_ota_status();
  if (status.installing)
    display_state.enter_ota(millis());
  else
    display_state.enter_portal(millis());
}

bool require_portal_get() {
  if (portal_subnet_request()) {
    portal_touch();
    return true;
  }
  add_portal_security_headers();
  portal_server.send(403, "application/json", "{\"error\":\"forbidden\"}");
  return false;
}

bool require_portal_post() {
  if (!portal_subnet_request() || !portal_session_valid() ||
      portal_server.header("X-CSRF-Token") != portal_csrf) {
    add_portal_security_headers();
    portal_server.send(403, "application/json", "{\"error\":\"forbidden\"}");
    return false;
  }
  if (portal_server.arg("plain").length() > kPortalPostLimit) {
    add_portal_security_headers();
    portal_server.send(413, "application/json",
                       "{\"error\":\"request_too_large\"}");
    return false;
  }
  portal_touch();
  return true;
}

template <typename TDocument>
void send_portal_json(int status, TDocument& document) {
  String body;
  serializeJson(document, body);
  add_portal_security_headers();
  portal_server.send(status, "application/json; charset=utf-8", body);
}

const char kPortalPage[] PROGMEM = R"HTML(<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>QMON 配置</title><style>
body{font-family:system-ui,sans-serif;background:#071225;color:#f8fafc;margin:0;padding:18px}main{max-width:620px;margin:auto}fieldset{border:1px solid #28527a;border-radius:12px;margin:12px 0;padding:14px;background:#0c1b35}label{display:block;margin:9px 0 4px}input,select,button{box-sizing:border-box;width:100%;padding:10px;border-radius:8px;border:1px solid #52779c;background:#102746;color:#fff}button{background:#1677d2;font-weight:700;margin-top:12px}button:disabled{opacity:.5}.row{display:grid;grid-template-columns:1fr 1fr;gap:10px}.muted{color:#a9bfd7;font-size:.9em}.ok{color:#86efac}.bad{color:#fca5a5}@media(max-width:520px){.row{grid-template-columns:1fr}}</style></head>
<body><main><h1>QMON 配置</h1><p id="device" class="muted">正在读取设备…</p>
<fieldset><legend>Wi-Fi 与服务器</legend><label>扫描到的 Wi-Fi</label><select id="scan"><option>正在扫描…</option></select><label>主 Wi-Fi 名称</label><input id="ssid" maxlength="32" autocomplete="off"><label>主 Wi-Fi 密码</label><input id="password" type="password" maxlength="64" placeholder="留空保留原密码"><label>备用 Wi-Fi 1 名称（可选）</label><input id="ssid2" maxlength="32" autocomplete="off"><label>备用 Wi-Fi 1 密码</label><input id="password2" type="password" maxlength="64" placeholder="留空保留原密码"><label>备用 Wi-Fi 2 名称（可选）</label><input id="ssid3" maxlength="32" autocomplete="off"><label>备用 Wi-Fi 2 密码</label><input id="password3" type="password" maxlength="64" placeholder="留空保留原密码"><label>HTTPS 服务器地址</label><input id="base_url" maxlength="255"><label>显示令牌</label><input id="token" type="password" maxlength="256" placeholder="留空保留原令牌"><label>时区</label><input id="timezone" maxlength="64" value="CST-8"><label>正常刷新（秒）</label><input id="refresh_seconds" type="number" min="5" max="3600"></fieldset>
<fieldset><legend>屏幕</legend><label>正常亮度</label><select id="brightness_percent"><option>30</option><option>60</option><option>100</option></select><div class="row"><div><label>降亮时间（秒，0 禁用）</label><input id="dim_after_seconds" type="number" min="0" max="86400"></div><div><label>熄屏时间（秒，0 禁用）</label><input id="screen_off_after_seconds" type="number" min="0" max="86400"></div></div><label>熄屏刷新（秒）</label><input id="screen_off_refresh_seconds" type="number" min="60" max="3600"><label><input id="external_power_sense_enabled" type="checkbox" style="width:auto"> 已按接线说明安装 USB +5V 到 GPIO35 的分压检测线</label><button id="save">测试并保存</button><p id="configStatus" class="muted"></p></fieldset>
<fieldset><legend>无线升级</legend><p id="ota" class="muted">正在检查…</p><label><input id="usb" type="checkbox" style="width:auto"> 已连接稳定 USB 电源</label><button id="install" disabled>确认安装升级</button><p id="otaProgress" class="muted"></p></fieldset></main>
<script>
let csrf='';const $=id=>document.getElementById(id);async function j(url,opt){const r=await fetch(url,opt);const x=await r.json();if(!r.ok)throw Error(x.error||r.status);return x}
async function status(){try{const s=await j('/api/status');csrf=s.csrf||csrf;$('device').textContent=`固件 ${s.firmware} · ${s.ip||'未联网'}`;for(const k of ['ssid','ssid2','ssid3','base_url','timezone','refresh_seconds','brightness_percent','dim_after_seconds','screen_off_after_seconds','screen_off_refresh_seconds'])if(s[k]!==undefined)$(k).value=s[k];$('external_power_sense_enabled').checked=!!s.external_power_sense_enabled}catch(e){$('device').textContent=e.message}}
async function wifi(){try{const w=await j('/api/wifi'),s=$('scan');s.replaceChildren(new Option('手动输入',''));for(const n of w.networks)s.add(new Option(`${n.ssid} (${n.rssi} dBm)`,n.ssid));s.onchange=()=>{if(s.value)$('ssid').value=s.value}}catch(e){setTimeout(wifi,1500)}}
$('save').onclick=async()=>{const body={};for(const k of ['ssid','password','ssid2','password2','ssid3','password3','base_url','token','timezone'])body[k]=$(k).value;for(const k of ['refresh_seconds','brightness_percent','dim_after_seconds','screen_off_after_seconds','screen_off_refresh_seconds'])body[k]=Number($(k).value);body.external_power_sense_enabled=$('external_power_sense_enabled').checked;try{await j('/api/config',{method:'POST',headers:{'Content-Type':'application/json','X-CSRF-Token':csrf},body:JSON.stringify(body)});$('configStatus').textContent='正在测试 Wi-Fi、时间和服务器…'}catch(e){$('configStatus').textContent=e.message}}
async function cfg(){try{const c=await j('/api/config/status');$('configStatus').textContent=c.status+(c.error?': '+c.error:'')}catch(e){}setTimeout(cfg,1000)}
async function ota(){try{const o=await j('/api/ota/status');$('ota').textContent=`当前 ${o.currentVersion} · 最新 ${o.latestVersion||'--'}${o.error?' · '+o.error:''}`;$('install').disabled=!o.updateAvailable||o.installing;$('otaProgress').textContent=o.installing?`升级中 ${o.progressPercent}%`:o.status}catch(e){}setTimeout(ota,1200)}
$('install').onclick=async()=>{if(!$('usb').checked)return alert('请先确认稳定 USB 电源');if(!confirm('安装后设备会重启，继续？'))return;try{await j('/api/ota/install',{method:'POST',headers:{'Content-Type':'application/json','X-CSRF-Token':csrf},body:JSON.stringify({confirmUsbPower:true})})}catch(e){alert(e.message)}};
status();wifi();cfg();ota();</script></body></html>)HTML";

void register_portal_handlers() {
  if (portal_handlers_registered) return;
  static const char* headers[] = {"Cookie", "X-CSRF-Token"};
  portal_server.collectHeaders(headers, 2);

  portal_server.on("/", HTTP_GET, [] {
    if (!require_portal_get()) return;
    portal_server.sendHeader("Set-Cookie",
                             "QMONSID=" + portal_session +
                                 "; Path=/; HttpOnly; SameSite=Strict");
    add_portal_security_headers();
    portal_server.send_P(200, "text/html; charset=utf-8", kPortalPage);
  });
  portal_server.on("/api/status", HTTP_GET, [] {
    if (!require_portal_get()) return;
    const DeviceConfig current_config = copy_config();
    JsonDocument body;
    body["firmware"] = QUOTA_MONITOR_FIRMWARE_VERSION;
    body["board"] = kFirmwareBoard;
    body["ip"] = WiFi.status() == WL_CONNECTED ? WiFi.localIP().toString() : "";
    body["ssid"] = current_config.ssid;
    body["ssid2"] = current_config.ssid2;
    body["ssid3"] = current_config.ssid3;
    body["base_url"] = current_config.base_url;
    body["timezone"] = current_config.timezone;
    body["refresh_seconds"] = current_config.refresh_seconds;
    body["brightness_percent"] = current_config.brightness_percent;
    body["dim_after_seconds"] = current_config.dim_after_seconds;
    body["screen_off_after_seconds"] = current_config.screen_off_after_seconds;
    body["screen_off_refresh_seconds"] =
        current_config.screen_off_refresh_seconds;
    body["external_power_sense_enabled"] =
        current_config.external_power_sense_enabled;
    body["passwordConfigured"] = !current_config.password.isEmpty();
    body["password2Configured"] = !current_config.password2.isEmpty();
    body["password3Configured"] = !current_config.password3.isEmpty();
    body["tokenConfigured"] = !current_config.token.isEmpty();
    if (portal_session_valid()) body["csrf"] = portal_csrf;
    send_portal_json(200, body);
  });
  portal_server.on("/api/wifi", HTTP_GET, [] {
    if (!require_portal_get()) return;
    String result;
    if (shared_mutex != nullptr) xSemaphoreTake(shared_mutex, portMAX_DELAY);
    result = wifi_scan_json;
    if (shared_mutex != nullptr) xSemaphoreGive(shared_mutex);
    add_portal_security_headers();
    portal_server.send(200, "application/json; charset=utf-8", result);
  });
  portal_server.on("/api/config", HTTP_POST, [] {
    if (!require_portal_post()) return;
    bool network_reserved = false;
    {
      SharedStateLock lock;
      network_reserved = candidate_test_in_flight || ota_status.installing;
    }
    if (network_reserved) {
      add_portal_security_headers();
      portal_server.send(409, "application/json",
                         "{\"error\":\"test_in_progress\"}");
      return;
    }
    JsonDocument input;
    if (deserializeJson(input, portal_server.arg("plain"))) {
      add_portal_security_headers();
      portal_server.send(400, "application/json",
                         "{\"error\":\"invalid_json\"}");
      return;
    }
    const DeviceConfig original = copy_config();
    DeviceConfig candidate = original;
    const auto assign_string = [&input](const char* key, String& target,
                                        size_t maximum, bool blank_keeps) {
      if (!input[key].is<const char*>()) return true;
      const String value = input[key].as<String>();
      if (value.length() > maximum) return false;
      if (!blank_keeps || !value.isEmpty()) target = value;
      return true;
    };
    bool valid = assign_string("ssid", candidate.ssid, 32, false) &&
                  assign_string("ssid2", candidate.ssid2, 32, false) &&
                  assign_string("ssid3", candidate.ssid3, 32, false) &&
                  assign_string("base_url", candidate.base_url, 255, false) &&
                 assign_string("token", candidate.token, 256, true) &&
                 assign_string("timezone", candidate.timezone, 64, false);
    const auto assign_password = [&input](const char* key,
                                          const String& old_ssid,
                                          const String& new_ssid,
                                          String& target) {
      if (!input[key].is<const char*>()) return true;
      const String value = input[key].as<String>();
      if (value.length() > 64) return false;
      if (!value.isEmpty()) {
        target = value;
      } else if (new_ssid != old_ssid || new_ssid.isEmpty()) {
        // Blank only means "keep" for the same SSID. Never reuse a stored
        // password after the corresponding network name has changed.
        target = "";
      }
      return true;
    };
    valid &= assign_password("password", original.ssid, candidate.ssid,
                             candidate.password);
    valid &= assign_password("password2", original.ssid2, candidate.ssid2,
                              candidate.password2);
    valid &= assign_password("password3", original.ssid3, candidate.ssid3,
                              candidate.password3);
    if (!valid || candidate.timezone.isEmpty()) {
      add_portal_security_headers();
      portal_server.send(400, "application/json",
                         "{\"error\":\"invalid_field_length\"}");
      return;
    }
    const auto assign_uint = [&input](const char* key, uint32_t& target) {
      if (input[key].isNull()) return true;
      if (!input[key].is<uint32_t>()) return false;
      target = input[key].as<uint32_t>();
      return true;
    };
    uint32_t brightness = candidate.brightness_percent;
    valid &= assign_uint("refresh_seconds", candidate.refresh_seconds);
    valid &= assign_uint("brightness_percent", brightness);
    valid &= assign_uint("dim_after_seconds", candidate.dim_after_seconds);
    valid &= assign_uint("screen_off_after_seconds",
                         candidate.screen_off_after_seconds);
    valid &= assign_uint("screen_off_refresh_seconds",
                          candidate.screen_off_refresh_seconds);
    if (!input["external_power_sense_enabled"].isNull()) {
      if (!input["external_power_sense_enabled"].is<bool>()) {
        valid = false;
      } else {
        candidate.external_power_sense_enabled =
            input["external_power_sense_enabled"].as<bool>();
      }
    }
    String why;
    if (!valid || candidate.refresh_seconds < 5 ||
        candidate.refresh_seconds > 3600 ||
        !brightness_allowed(brightness) ||
        candidate.dim_after_seconds > 86400 ||
        candidate.screen_off_after_seconds > 86400 ||
        candidate.screen_off_refresh_seconds < 60 ||
        candidate.screen_off_refresh_seconds > 3600 ||
        !config_ready(candidate, why)) {
      add_portal_security_headers();
      portal_server.send(400, "application/json",
                         "{\"error\":\"invalid_configuration\"}");
      return;
    }
    candidate.brightness_percent = static_cast<uint8_t>(brightness);
    if (shared_mutex != nullptr) xSemaphoreTake(shared_mutex, portMAX_DELAY);
    pending_candidate = candidate;
    candidate_result = {};
    candidate_test_in_flight = true;
    if (shared_mutex != nullptr) xSemaphoreGive(shared_mutex);
    if (!queue_network_job(NetworkJob::kTestCandidateConfig)) {
      {
        SharedStateLock lock;
        candidate_test_in_flight = false;
      }
      add_portal_security_headers();
      portal_server.send(503, "application/json",
                         "{\"error\":\"network_queue_busy\"}");
      return;
    }
    JsonDocument response;
    response["status"] = "testing";
    send_portal_json(202, response);
  });
  portal_server.on("/api/config/status", HTTP_GET, [] {
    if (!require_portal_get()) return;
    CandidateResult result;
    bool testing = false;
    {
      SharedStateLock lock;
      result = candidate_result;
      testing = candidate_test_in_flight;
    }
    JsonDocument body;
    if (testing) {
      body["status"] = "testing";
    } else if (result.ready) {
      body["status"] = result.success ? "success" : "failed";
      if (!result.success) body["error"] = result.error;
    } else {
      body["status"] = "idle";
    }
    send_portal_json(200, body);
  });
  portal_server.on("/api/ota/status", HTTP_GET, [] {
    if (!require_portal_get()) return;
    OtaStatus status;
    if (shared_mutex != nullptr) xSemaphoreTake(shared_mutex, portMAX_DELAY);
    status = ota_status;
    if (shared_mutex != nullptr) xSemaphoreGive(shared_mutex);
    JsonDocument body;
    body["currentVersion"] = QUOTA_MONITOR_FIRMWARE_VERSION;
    body["latestVersion"] = status.latest_version;
    body["updateAvailable"] = status.update_available;
    body["installing"] = status.installing;
    body["progressPercent"] = status.progress_percent;
    body["status"] = status.checking ? "checking"
                     : status.installing ? "installing"
                     : status.install_success ? "installed"
                                                  : "ready";
    if (!status.error.isEmpty()) body["error"] = status.error;
    send_portal_json(200, body);
  });
  portal_server.on("/api/ota/progress", HTTP_GET, [] {
    if (!require_portal_get()) return;
    OtaStatus status;
    if (shared_mutex != nullptr) xSemaphoreTake(shared_mutex, portMAX_DELAY);
    status = ota_status;
    if (shared_mutex != nullptr) xSemaphoreGive(shared_mutex);
    JsonDocument body;
    body["installing"] = status.installing;
    body["progressPercent"] = status.progress_percent;
    body["success"] = status.install_success;
    if (!status.error.isEmpty()) body["error"] = status.error;
    send_portal_json(200, body);
  });
  portal_server.on("/api/ota/install", HTTP_POST, [] {
    if (!require_portal_post()) return;
    JsonDocument input;
    if (deserializeJson(input, portal_server.arg("plain")) ||
        input["confirmUsbPower"] != true) {
      add_portal_security_headers();
      portal_server.send(400, "application/json",
                         "{\"error\":\"usb_power_confirmation_required\"}");
      return;
    }
    bool installable = false;
    {
      SharedStateLock lock;
      installable = ota_status.update_available && !ota_status.installing &&
                    !ota_status.result_ready && !ota_status.install_success &&
                    !candidate_test_in_flight && restart_after_ota_ms == 0;
      if (installable) {
        ota_status.installing = true;
        ota_status.progress_percent = 0;
        ota_status.error = "";
      }
    }
    if (!installable) {
      add_portal_security_headers();
      portal_server.send(409, "application/json",
                         "{\"error\":\"no_installable_update\"}");
      return;
    }
    display_state.enter_ota(millis());
    if (!queue_network_job(NetworkJob::kInstallOta)) {
      {
        SharedStateLock lock;
        ota_status.installing = false;
      }
      add_portal_security_headers();
      portal_server.send(503, "application/json",
                         "{\"error\":\"network_queue_busy\"}");
      return;
    }
    JsonDocument body;
    body["status"] = "installing";
    send_portal_json(202, body);
  });
  portal_server.onNotFound([] {
    if (!portal_subnet_request()) {
      add_portal_security_headers();
      portal_server.send(403, "text/plain", "Forbidden");
      return;
    }
    portal_server.sendHeader("Location", "http://192.168.4.1/", true);
    portal_server.send(302, "text/plain", "");
  });
  portal_handlers_registered = true;
}

void start_configuration_portal() {
  if (portal_active) {
    portal_touch();
    return;
  }
  char suffix[7];
  snprintf(suffix, sizeof(suffix), "%06X",
           static_cast<unsigned>(ESP.getEfuseMac() & 0x00ffffffULL));
  portal_ssid = "QMON-" + String(suffix);
  portal_password = random_alphanumeric(12);
  portal_session = random_alphanumeric(24);
  portal_csrf = random_alphanumeric(32);
  WiFi.mode(WIFI_AP_STA);
  const IPAddress portal_ip(192, 168, 4, 1);
  if (!WiFi.softAPConfig(portal_ip, portal_ip, IPAddress(255, 255, 255, 0))) {
    show_message("配网页地址配置失败", 5000);
    return;
  }
  if (!WiFi.softAP(portal_ssid.c_str(), portal_password.c_str(), 1, false, 1)) {
    show_message("配网页热点启动失败", 5000);
    return;
  }
  portal_active = true;
  portal_last_activity_ms = millis();
  display_state.enter_portal(millis());
  register_portal_handlers();
  portal_dns.start(53, "*", IPAddress(192, 168, 4, 1));
  portal_server.begin();
  {
    SharedStateLock lock;
    ota_status = {};
    ota_status.checking = true;
    wifi_scan_json = "{\"scanning\":true,\"networks\":[]}";
  }
  queue_network_job(NetworkJob::kScanWifi);
  queue_network_job(NetworkJob::kCheckOta);
  show_message(portal_ssid + "\n密码 " + portal_password +
                   "\nhttp://192.168.4.1",
               kPortalIdleMs);
}

void stop_configuration_portal() {
  if (!portal_active) return;
  portal_server.stop();
  portal_dns.stop();
  WiFi.softAPdisconnect(true);
  portal_active = false;
  display_state.leave_forced_mode(millis());
  WiFi.mode(WIFI_STA);
  request_wifi_now();
}

void service_portal() {
  if (!portal_active) return;
  portal_dns.processNextRequest();
  portal_server.handleClient();
  const bool installing = copy_ota_status().installing;
  if (!installing &&
      millis() - portal_last_activity_ms >= kPortalIdleMs)
    stop_configuration_portal();
}

void worker_scan_wifi() {
  const int count = WiFi.scanNetworks(false, true, false, 300U);
  JsonDocument document;
  document["scanning"] = false;
  JsonArray networks = document["networks"].to<JsonArray>();
  if (count > 0) {
    const int limited = std::min(count, 20);
    for (int i = 0; i < limited; ++i) {
      JsonObject network = networks.add<JsonObject>();
      network["ssid"] = WiFi.SSID(i);
      network["rssi"] = WiFi.RSSI(i);
      network["secure"] = WiFi.encryptionType(i) != WIFI_AUTH_OPEN;
    }
  }
  WiFi.scanDelete();
  String result;
  serializeJson(document, result);
  if (shared_mutex != nullptr) xSemaphoreTake(shared_mutex, portMAX_DELAY);
  wifi_scan_json = result;
  if (shared_mutex != nullptr) xSemaphoreGive(shared_mutex);
}

void worker_test_candidate() {
  DeviceConfig candidate;
  DeviceConfig previous;
  if (shared_mutex != nullptr) xSemaphoreTake(shared_mutex, portMAX_DELAY);
  candidate = pending_candidate;
  previous = config;
  if (shared_mutex != nullptr) xSemaphoreGive(shared_mutex);

  const String previously_connected_ssid =
      WiFi.status() == WL_CONNECTED ? WiFi.SSID() : "";
  const bool primary_changed = candidate.ssid != previous.ssid ||
                               candidate.password != previous.password;
  const bool backup_changed = candidate.ssid2 != previous.ssid2 ||
                              candidate.password2 != previous.password2;
  const bool backup2_changed = candidate.ssid3 != previous.ssid3 ||
                               candidate.password3 != previous.password3;
  quota_monitor::WifiProfile test_profile =
      quota_monitor::WifiProfile::kPrimary;
  if (!primary_changed && backup_changed && !candidate.ssid2.isEmpty()) {
    test_profile = quota_monitor::WifiProfile::kBackup1;
  } else if (!primary_changed && !backup_changed && backup2_changed &&
             !candidate.ssid3.isEmpty()) {
    test_profile = quota_monitor::WifiProfile::kBackup2;
  } else if (!primary_changed && !backup_changed && !backup2_changed) {
    if (!candidate.ssid2.isEmpty() &&
        previously_connected_ssid == candidate.ssid2) {
      test_profile = quota_monitor::WifiProfile::kBackup1;
    } else if (!candidate.ssid3.isEmpty() &&
               previously_connected_ssid == candidate.ssid3) {
      test_profile = quota_monitor::WifiProfile::kBackup2;
    }
  }
  const String& candidate_ssid =
      wifi_ssid_for_profile(candidate, test_profile);
  const String& candidate_password =
      wifi_password_for_profile(candidate, test_profile);

  String error;
  WiFi.disconnect(false, false);
  WiFi.mode(WIFI_AP_STA);
  WiFi.begin(candidate_ssid.c_str(), candidate_password.c_str());
  const uint32_t wifi_deadline = millis() + 20000U;
  while (WiFi.status() != WL_CONNECTED &&
         static_cast<int32_t>(millis() - wifi_deadline) < 0)
    vTaskDelay(pdMS_TO_TICKS(100));
  bool success = WiFi.status() == WL_CONNECTED;
  if (!success) error = "Wi-Fi connection failed";

  if (success) {
    esp_sntp_set_sync_status(SNTP_SYNC_STATUS_RESET);
    configTzTime(candidate.timezone.c_str(), "pool.ntp.org",
                 "time.cloudflare.com");
    const uint32_t ntp_deadline = millis() + 15000U;
    bool synchronized = false;
    while (static_cast<int32_t>(millis() - ntp_deadline) < 0) {
      if (esp_sntp_get_sync_status() == SNTP_SYNC_STATUS_COMPLETED) {
        synchronized = clock_is_sane();
        break;
      }
      vTaskDelay(pdMS_TO_TICKS(100));
    }
    success = synchronized;
    if (!success) error = "time synchronization failed";
  }

  if (success) {
    quota_monitor::Snapshot candidate_snapshot;
    success = fetch_snapshot_for(candidate, 0, candidate_snapshot, error);
  }

  if (!success) {
    configTzTime(previous.timezone.c_str(), "pool.ntp.org",
                 "time.cloudflare.com");
    WiFi.disconnect(false, false);
    quota_monitor::WifiProfile restore_profile =
        quota_monitor::WifiProfile::kPrimary;
    if (!previous.ssid2.isEmpty() &&
        previously_connected_ssid == previous.ssid2) {
      restore_profile = quota_monitor::WifiProfile::kBackup1;
    } else if (!previous.ssid3.isEmpty() &&
               previously_connected_ssid == previous.ssid3) {
      restore_profile = quota_monitor::WifiProfile::kBackup2;
    }
    const String& restore_ssid =
        wifi_ssid_for_profile(previous, restore_profile);
    const String& restore_password =
        wifi_password_for_profile(previous, restore_profile);
    WiFi.begin(restore_ssid.c_str(), restore_password.c_str());
  }
  if (shared_mutex != nullptr) xSemaphoreTake(shared_mutex, portMAX_DELAY);
  candidate_result.ready = true;
  candidate_result.success = success;
  candidate_result.error = error;
  candidate_test_in_flight = false;
  if (shared_mutex != nullptr) xSemaphoreGive(shared_mutex);
}

bool worker_get_manifest(String& error) {
  const DeviceConfig current_config = copy_config();
  const String origin = server_origin(current_config);
  if (origin.isEmpty() || !origin.startsWith("https://")) {
    error = "OTA requires HTTPS server";
    return false;
  }
  const String url = origin + "/api/v1/display/firmware/" + kFirmwareBoard +
                     "/manifest";
  WiFiClientSecure secure;
  const uint8_t* trust_anchor = uses_device_tls_ca(url)
                                    ? device_root_ca_start
                                    : isrg_root_x1_start;
  secure.setCACert(reinterpret_cast<const char*>(trust_anchor));
  HTTPClient http;
  http.setConnectTimeout(8000);
  http.setTimeout(10000);
  http.setFollowRedirects(HTTPC_DISABLE_FOLLOW_REDIRECTS);
  if (!http.begin(secure, url)) {
    error = "manifest connection failed";
    return false;
  }
  http.addHeader("Authorization", "Bearer " + current_config.token);
  http.addHeader("Accept", "application/json");
  http.addHeader("User-Agent", "quota-display/" QUOTA_MONITOR_FIRMWARE_VERSION);
  const int code = http.GET();
  if (code != HTTP_CODE_OK) {
    error = "manifest HTTP " + String(code);
    http.end();
    return false;
  }
  if (http.getSize() > 8192) {
    error = "manifest too large";
    http.end();
    return false;
  }
  BoundedHttpSink sink(8192);
  const int read = http.writeToStream(&sink);
  http.end();
  if (read < 0 || sink.buffer().overflowed()) {
    error = "manifest read failed";
    return false;
  }
  JsonDocument manifest;
  const std::string& json = sink.buffer().value();
  if (deserializeJson(manifest, json.data(), json.size())) {
    error = "manifest JSON invalid";
    return false;
  }
  if (!manifest["schemaVersion"].is<uint32_t>() ||
      !manifest["board"].is<const char*>() ||
      !manifest["version"].is<const char*>() ||
      !manifest["publishedAt"].is<const char*>() ||
      !manifest["sizeBytes"].is<size_t>() ||
      !manifest["sha256"].is<const char*>()) {
    error = "manifest fields invalid";
    return false;
  }
  const String published_at = manifest["publishedAt"].as<String>();
  std::int64_t published_epoch = 0;
  std::string published_error;
  if (published_at.isEmpty() ||
      !quota_monitor::parse_rfc3339(published_at.c_str(), published_epoch,
                                    published_error)) {
    error = "manifest timestamp invalid";
    return false;
  }
  const esp_partition_t* partition = esp_ota_get_next_update_partition(nullptr);
  if (partition == nullptr) {
    error = "OTA partition unavailable";
    return false;
  }
  quota_monitor::FirmwareManifestPolicy policy{
      kFirmwareBoard, QUOTA_MONITOR_FIRMWARE_VERSION, partition->size, 0};
  std::string validation_error;
  const String board = manifest["board"].as<String>();
  const String version = manifest["version"].as<String>();
  const String sha256 = manifest["sha256"].as<String>();
  const size_t size_bytes = manifest["sizeBytes"].as<size_t>();
  if (!quota_monitor::validate_firmware_manifest(
          manifest["schemaVersion"].as<uint32_t>(), board.c_str(),
          version.c_str(), size_bytes, sha256.c_str(), policy,
          validation_error)) {
    error = validation_error.c_str();
    return false;
  }
  if (shared_mutex != nullptr) xSemaphoreTake(shared_mutex, portMAX_DELAY);
  ota_status.manifest_valid = true;
  ota_status.update_available = true;
  ota_status.latest_version = version;
  ota_status.published_at = published_at;
  ota_status.size_bytes = size_bytes;
  ota_status.sha256 = sha256;
  ota_status.error = "";
  if (shared_mutex != nullptr) xSemaphoreGive(shared_mutex);
  return true;
}

String sha256_hex(const uint8_t digest[32]) {
  static constexpr char hex[] = "0123456789abcdef";
  char output[65];
  for (size_t i = 0; i < 32; ++i) {
    output[i * 2] = hex[digest[i] >> 4];
    output[i * 2 + 1] = hex[digest[i] & 0x0f];
  }
  output[64] = '\0';
  return String(output);
}

bool worker_install_ota(String& error) {
  OtaStatus selected;
  DeviceConfig current_config;
  if (shared_mutex != nullptr) xSemaphoreTake(shared_mutex, portMAX_DELAY);
  selected = ota_status;
  current_config = config;
  if (shared_mutex != nullptr) xSemaphoreGive(shared_mutex);
  if (!selected.manifest_valid || !selected.update_available) {
    error = "no validated update";
    return false;
  }
  const String origin = server_origin(current_config);
  const String url = origin + "/api/v1/display/firmware/" + kFirmwareBoard +
                     "/" + selected.latest_version + ".bin";
  if (!url.startsWith("https://")) {
    error = "OTA requires HTTPS server";
    return false;
  }
  WiFiClientSecure secure;
  const uint8_t* trust_anchor = uses_device_tls_ca(url)
                                    ? device_root_ca_start
                                    : isrg_root_x1_start;
  secure.setCACert(reinterpret_cast<const char*>(trust_anchor));
  HTTPClient http;
  http.setConnectTimeout(8000);
  http.setTimeout(15000);
  http.setFollowRedirects(HTTPC_DISABLE_FOLLOW_REDIRECTS);
  if (!http.begin(secure, url)) {
    error = "firmware connection failed";
    return false;
  }
  http.addHeader("Authorization", "Bearer " + current_config.token);
  http.addHeader("Accept", "application/octet-stream");
  http.addHeader("User-Agent", "quota-display/" QUOTA_MONITOR_FIRMWARE_VERSION);
  const int code = http.GET();
  if (code != HTTP_CODE_OK) {
    error = "firmware HTTP " + String(code);
    http.end();
    return false;
  }
  if (http.getSize() < 0 ||
      static_cast<size_t>(http.getSize()) != selected.size_bytes) {
    error = "firmware size mismatch";
    http.end();
    return false;
  }
  if (!Update.begin(selected.size_bytes, U_FLASH)) {
    error = "OTA slot initialization failed";
    http.end();
    return false;
  }

  mbedtls_sha256_context context;
  mbedtls_sha256_init(&context);
  bool ok = mbedtls_sha256_starts_ret(&context, 0) == 0;
  std::unique_ptr<uint8_t[]> buffer(new (std::nothrow) uint8_t[4096]);
  if (!buffer) ok = false;
  WiFiClient* stream = http.getStreamPtr();
  size_t total = 0;
  uint32_t idle_deadline = millis() + 15000U;
  while (ok && total < selected.size_bytes) {
    const size_t available = stream->available();
    if (available == 0) {
      if (!http.connected() ||
          static_cast<int32_t>(millis() - idle_deadline) >= 0) {
        ok = false;
        error = "firmware download interrupted";
        break;
      }
      vTaskDelay(pdMS_TO_TICKS(10));
      continue;
    }
    const size_t wanted = std::min<size_t>(
        4096, std::min(available, selected.size_bytes - total));
    const int count = stream->readBytes(buffer.get(), wanted);
    if (count <= 0 ||
        Update.write(buffer.get(), static_cast<size_t>(count)) !=
            static_cast<size_t>(count) ||
        mbedtls_sha256_update_ret(&context, buffer.get(), count) != 0) {
      ok = false;
      error = "firmware write failed";
      break;
    }
    total += static_cast<size_t>(count);
    idle_deadline = millis() + 15000U;
    {
      SharedStateLock lock;
      ota_status.progress_percent = static_cast<uint8_t>(
          std::min<size_t>(99, (total * 100U) / selected.size_bytes));
    }
  }
  uint8_t digest[32]{};
  if (ok && mbedtls_sha256_finish_ret(&context, digest) != 0) ok = false;
  mbedtls_sha256_free(&context);
  http.end();
  if (ok && total != selected.size_bytes) {
    ok = false;
    error = "firmware truncated";
  }
  if (ok && sha256_hex(digest) != selected.sha256) {
    ok = false;
    error = "firmware SHA-256 mismatch";
  }
  if (!ok) {
    Update.abort();
    if (error.isEmpty()) error = "firmware verification failed";
    return false;
  }
  if (!Update.end(false) || !Update.isFinished()) {
    Update.abort();
    error = "OTA finalization failed";
    return false;
  }
  {
    SharedStateLock lock;
    ota_status.progress_percent = 100;
  }
  return true;
}

void network_worker(void*) {
  NetworkJob job;
  for (;;) {
    if (xQueueReceive(network_jobs, &job, portMAX_DELAY) != pdTRUE) continue;
    if (job == NetworkJob::kFetchSnapshot) {
      String error;
      const bool success = fetch_snapshot(error);
      if (shared_mutex != nullptr) xSemaphoreTake(shared_mutex, portMAX_DELAY);
      fetch_result_success = success;
      fetch_result_error = error;
      fetch_result_ready = true;
      if (shared_mutex != nullptr) xSemaphoreGive(shared_mutex);
    } else if (job == NetworkJob::kTestCandidateConfig) {
      worker_test_candidate();
    } else if (job == NetworkJob::kScanWifi) {
      worker_scan_wifi();
    } else if (job == NetworkJob::kCheckOta) {
      String error;
      const bool success = worker_get_manifest(error);
      if (shared_mutex != nullptr) xSemaphoreTake(shared_mutex, portMAX_DELAY);
      ota_status.checking = false;
      if (!success) ota_status.error = error;
      if (shared_mutex != nullptr) xSemaphoreGive(shared_mutex);
    } else if (job == NetworkJob::kInstallOta) {
      String error;
      const bool success = worker_install_ota(error);
      if (shared_mutex != nullptr) xSemaphoreTake(shared_mutex, portMAX_DELAY);
      ota_status.installing = false;
      ota_status.result_ready = true;
      ota_status.install_success = success;
      if (success) {
        // Make the successful image one-shot immediately. The main loop still
        // needs a short window to acknowledge the HTTP response before reboot,
        // but another POST must not erase the newly selected boot partition.
        ota_status.update_available = false;
        ota_status.manifest_valid = false;
      }
      ota_status.error = error;
      if (shared_mutex != nullptr) xSemaphoreGive(shared_mutex);
    }
  }
}

void clear_snapshot_cache() {
  SharedStateLock lock;
  ++snapshot_generation;
  snapshot = {};
  have_snapshot = false;
  have_persisted_snapshot_cache = false;
  last_accepted_generated_epoch = 0;
  last_persisted_generated_epoch = 0;
  preferences.begin("quota-monitor", false);
  preferences.remove("snapshot");
  preferences.remove("last_epoch");
  preferences.end();
}

bool commit_config(const DeviceConfig& requested) {
  DeviceConfig candidate = requested;
  normalize_config(candidate);
  bool endpoint_changed = false;
  {
    SharedStateLock lock;
    endpoint_changed = candidate.base_url != committed_config.base_url ||
                       candidate.token != committed_config.token;
  }
  if (!save_config(candidate)) return false;
  {
    SharedStateLock lock;
    config = candidate;
  }
  if (endpoint_changed) clear_snapshot_cache();
  return true;
}

void service_network_results() {
  String error;
  bool success = false;
  bool have_fetch_result = false;
  {
    SharedStateLock lock;
    if (fetch_result_ready) {
      success = fetch_result_success;
      error = fetch_result_error;
      fetch_result_ready = false;
      have_fetch_result = true;
    }
  }
  if (have_fetch_result) {
    fetch_in_flight = false;
    if (success) {
      api_backoff_ms = 1000;
      const DeviceConfig current_config = copy_config();
      const uint32_t seconds =
          display_state.state() == quota_monitor::DisplayState::kBacklightOff
              ? quota_monitor::screen_off_refresh_seconds(
                    current_config.refresh_seconds,
                    current_config.screen_off_refresh_seconds)
              : current_config.refresh_seconds;
      next_fetch_ms = millis() + seconds * 1000UL;
    } else {
      {
        SharedStateLock lock;
        last_error = error;
      }
      next_fetch_ms = millis() + api_backoff_ms;
      api_backoff_ms = std::min(api_backoff_ms * 2UL, 60000UL);
    }
  }

  DeviceConfig candidate;
  bool candidate_ready = false;
  {
    SharedStateLock lock;
    candidate_ready = candidate_result.ready && candidate_result.success &&
                      restart_after_config_ms == 0;
    if (candidate_ready) candidate = pending_candidate;
  }
  if (candidate_ready) {
    if (!commit_config(candidate)) {
      SharedStateLock lock;
      candidate_result.success = false;
      candidate_result.error = "configuration commit failed";
    } else {
      show_message("配置验证成功，正在重启", 3000);
      restart_after_config_ms = millis() + 1800U;
    }
  }

  OtaStatus ota_result;
  bool ota_result_ready = false;
  {
    SharedStateLock lock;
    if (ota_status.result_ready) {
      ota_result = ota_status;
      ota_status.result_ready = false;
      ota_result_ready = true;
    }
  }
  if (ota_result_ready) {
    if (ota_result.install_success) {
      show_message("升级验证成功，正在重启", 3000);
      restart_after_ota_ms = millis() + 1800U;
    } else {
      display_state.enter_portal(millis());
      show_message("升级失败：" + ota_result.error, 6000);
    }
  }
}

void print_config() {
  const DeviceConfig current_config = copy_config();
  Serial.printf("ssid=%s\n", current_config.ssid.c_str());
  Serial.printf("password=%s\n", masked(current_config.password).c_str());
  Serial.printf("ssid2=%s\n", current_config.ssid2.c_str());
  Serial.printf("password2=%s\n", masked(current_config.password2).c_str());
  Serial.printf("ssid3=%s\n", current_config.ssid3.c_str());
  Serial.printf("password3=%s\n", masked(current_config.password3).c_str());
  Serial.printf("base_url=%s\n", current_config.base_url.c_str());
  Serial.printf("token=%s\n", masked(current_config.token).c_str());
  Serial.printf("timezone=%s\n", current_config.timezone.c_str());
  Serial.printf("refresh_seconds=%lu\n",
                static_cast<unsigned long>(current_config.refresh_seconds));
  Serial.printf("brightness_percent=%u\n",
                current_config.brightness_percent);
  Serial.printf("dim_after_seconds=%lu\n",
                static_cast<unsigned long>(current_config.dim_after_seconds));
  Serial.printf("screen_off_after_seconds=%lu\n",
                static_cast<unsigned long>(
                    current_config.screen_off_after_seconds));
  Serial.printf("screen_off_refresh_seconds=%lu\n",
                 static_cast<unsigned long>(
                     current_config.screen_off_refresh_seconds));
  Serial.printf("external_power_sense_enabled=%s\n",
                current_config.external_power_sense_enabled ? "yes" : "no");
  Serial.printf("external_power_present=%s\n",
                display_state.external_power_present() ? "yes" : "no");
  if (cached_battery_ok)
    Serial.printf("battery_voltage_mv=%u\n", cached_battery_mv);
  else
    Serial.println("battery_voltage_mv=N/A");
#if defined(QUOTA_BOARD_E32R28T)
  Serial.printf("charging_inferred=%s\n",
                charge_trend_detector.charging() ? "yes" : "no");
#else
  Serial.println("charging_inferred=no");
#endif
  Serial.printf("battery_savings_bypass=%s\n",
                display_state.external_power_present() ? "yes" : "no");
#if QUOTA_HAS_TOUCH
  Serial.printf("touch_cal=%u,%u,%u,%u\n", current_config.touch_x_min,
                current_config.touch_x_max, current_config.touch_y_min,
                current_config.touch_y_max);
#endif
  Serial.printf("dirty=%s wifi=%s ip=%s\n", config_dirty ? "yes" : "no",
                WiFi.status() == WL_CONNECTED ? "connected" : "offline",
                WiFi.localIP().toString().c_str());
}

void print_help() {
  Serial.println("Commands:");
  Serial.println("  show");
  Serial.println("  set ssid|password|ssid2|password2|ssid3|password3 VALUE");
  Serial.println("  set base_url|token|timezone|refresh_seconds VALUE");
  Serial.println("  set brightness_percent|dim_after_seconds VALUE");
  Serial.println("  set screen_off_after_seconds|screen_off_refresh_seconds VALUE");
  Serial.println("  set external_power_sense_enabled 0|1");
#if QUOTA_HAS_TOUCH
  Serial.println("  set touch_x_min|touch_x_max|touch_y_min|touch_y_max VALUE");
#endif
  Serial.println("  test");
  Serial.println("  save");
  Serial.println("  wifi-promote {\"ssid\":\"...\",\"password\":\"...\"}");
  Serial.println("  portal");
  Serial.println("  factory-reset");
}

void set_config_value(const String& key, const String& value) {
  DeviceConfig updated = copy_config();
  if (key == "ssid")
    updated.ssid = value;
  else if (key == "password")
    updated.password = value;
  else if (key == "ssid2")
    updated.ssid2 = value;
  else if (key == "password2")
    updated.password2 = value;
  else if (key == "ssid3")
    updated.ssid3 = value;
  else if (key == "password3")
    updated.password3 = value;
  else if (key == "base_url")
    updated.base_url = value;
  else if (key == "token")
    updated.token = value;
  else if (key == "timezone")
    updated.timezone = value;
  else if (key == "refresh_seconds") {
    const long seconds = value.toInt();
    if (seconds < 5 || seconds > 3600) {
      Serial.println("ERR refresh_seconds must be 5..3600");
      return;
    }
    updated.refresh_seconds = seconds;
  } else if (key == "brightness_percent") {
    const long percent = value.toInt();
    if (!brightness_allowed(percent)) {
      Serial.println("ERR brightness_percent must be 30, 60, or 100");
      return;
    }
    updated.brightness_percent = static_cast<uint8_t>(percent);
  } else if (key == "dim_after_seconds" ||
             key == "screen_off_after_seconds") {
    const long seconds = value.toInt();
    if (seconds < 0 || seconds > 86400) {
      Serial.println("ERR inactivity seconds must be 0..86400");
      return;
    }
    if (key == "dim_after_seconds")
      updated.dim_after_seconds = static_cast<uint32_t>(seconds);
    else
      updated.screen_off_after_seconds = static_cast<uint32_t>(seconds);
  } else if (key == "screen_off_refresh_seconds") {
    const long seconds = value.toInt();
    if (seconds < 60 || seconds > 3600) {
      Serial.println("ERR screen_off_refresh_seconds must be 60..3600");
      return;
    }
    updated.screen_off_refresh_seconds = static_cast<uint32_t>(seconds);
  } else if (key == "external_power_sense_enabled") {
    if (value == "1" || value == "true" || value == "yes") {
      updated.external_power_sense_enabled = true;
    } else if (value == "0" || value == "false" || value == "no") {
      updated.external_power_sense_enabled = false;
    } else {
      Serial.println("ERR external_power_sense_enabled must be 0 or 1");
      return;
    }
#if QUOTA_HAS_TOUCH
  } else if (key == "touch_x_min" || key == "touch_x_max" ||
             key == "touch_y_min" || key == "touch_y_max") {
    const long raw = value.toInt();
    if (raw < 0 || raw > 4095) {
      Serial.println("ERR touch calibration must be 0..4095");
      return;
    }
    if (key == "touch_x_min") updated.touch_x_min = raw;
    if (key == "touch_x_max") updated.touch_x_max = raw;
    if (key == "touch_y_min") updated.touch_y_min = raw;
    if (key == "touch_y_max") updated.touch_y_max = raw;
#endif
  } else {
    Serial.println("ERR unknown key");
    return;
  }
  {
    SharedStateLock lock;
    config = updated;
    config_dirty = true;
    if (key == "ssid" || key == "password" || key == "ssid2" ||
        key == "password2" || key == "ssid3" || key == "password3" ||
        key == "base_url" || key == "token" || key == "timezone")
      network_config_dirty = true;
  }
  Serial.println("OK staged; run save to persist");
}

void stage_promoted_wifi(const String& json) {
  JsonDocument input;
  if (deserializeJson(input, json) || !input["ssid"].is<const char*>() ||
      !input["password"].is<const char*>()) {
    Serial.println("ERR wifi-promote requires JSON string ssid and password");
    return;
  }
  const String new_ssid = input["ssid"].as<String>();
  const String new_password = input["password"].as<String>();
  if (new_ssid.isEmpty() || new_ssid.length() > 32 ||
      new_password.length() > 64) {
    Serial.println("ERR invalid Wi-Fi credential length");
    return;
  }

  DeviceConfig updated = copy_config();
  if (new_ssid == updated.ssid) {
    if (!new_password.isEmpty()) updated.password = new_password;
  } else if (new_ssid == updated.ssid2) {
    const String promoted_password =
        new_password.isEmpty() ? updated.password2 : new_password;
    updated.ssid2 = updated.ssid;
    updated.password2 = updated.password;
    updated.ssid = new_ssid;
    updated.password = promoted_password;
  } else if (new_ssid == updated.ssid3) {
    const String promoted_password =
        new_password.isEmpty() ? updated.password3 : new_password;
    updated.ssid3 = updated.ssid2;
    updated.password3 = updated.password2;
    updated.ssid2 = updated.ssid;
    updated.password2 = updated.password;
    updated.ssid = new_ssid;
    updated.password = promoted_password;
  } else {
    updated.ssid3 = updated.ssid2;
    updated.password3 = updated.password2;
    updated.ssid2 = updated.ssid;
    updated.password2 = updated.password;
    updated.ssid = new_ssid;
    updated.password = new_password;
  }

  String why;
  if (!config_ready(updated, why)) {
    Serial.println("ERR " + why);
    return;
  }
  {
    SharedStateLock lock;
    config = updated;
    config_dirty = true;
    network_config_dirty = true;
  }
  Serial.println("OK Wi-Fi priority staged; run save to persist");
}

void handle_serial_command(String line) {
  line.trim();
  if (line.isEmpty()) return;
  if (line == "show") {
    print_config();
  } else if (line == "save") {
    const DeviceConfig current_config = copy_config();
    String why;
    if (!config_ready(current_config, why)) {
      Serial.println("ERR " + why);
      return;
    }
    if (!commit_config(current_config)) {
      Serial.println("ERR configuration commit failed; previous slot retained");
      return;
    }
    const DeviceConfig saved_config = copy_config();
    display_state.configure(
        {saved_config.dim_after_seconds,
         saved_config.screen_off_after_seconds});
    display_state.note_activity(millis());
    configTzTime(saved_config.timezone.c_str(), "pool.ntp.org",
                 "time.cloudflare.com");
    request_wifi_now();
    Serial.println("OK saved");
  } else if (line == "test") {
    manual_refresh_gate.request(millis());
    if (WiFi.status() != WL_CONNECTED) request_wifi_now();
    Serial.println("OK test queued; use show to inspect connection state");
  } else if (line == "factory-reset") {
    clear_config();
    Serial.println("OK configuration erased; rebooting");
    delay(100);
    ESP.restart();
  } else if (line == "portal") {
    start_configuration_portal();
  } else if (line == "help") {
    print_help();
  } else if (line.startsWith("wifi-promote ")) {
    stage_promoted_wifi(line.substring(13));
  } else if (line.startsWith("set ")) {
    const int key_end = line.indexOf(' ', 4);
    if (key_end < 0) {
      Serial.println("ERR usage: set KEY VALUE");
      return;
    }
    set_config_value(line.substring(4, key_end), line.substring(key_end + 1));
  } else {
    Serial.println("ERR unknown command; type help");
  }
}

void service_serial() {
  while (Serial.available()) {
    const char c = static_cast<char>(Serial.read());
    if (c == '\r') continue;
    if (c == '\n') {
      handle_serial_command(serial_line);
      serial_line = "";
    } else if (serial_line.length() < 512) {
      serial_line += c;
    }
  }
}

#if QUOTA_HAS_CARRIER_POWER
bool usb_present() {
  // DFR0975 VCC is ~5 V on USB and tracks the 1S cell on battery.  With the
  // carrier's 100k/100k divider, 2.25 V separates USB from a 4.2 V cell. Read
  // repeatedly and use the median so a single ADC spike cannot wake the full
  // runtime. The stateful filter then adds hysteresis around that boundary.
  std::array<std::uint16_t, quota_monitor::kUsbSenseSampleCount> samples{};
  for (auto& sample : samples) {
    sample = static_cast<std::uint16_t>(analogReadMilliVolts(PIN_USB_SENSE));
    delay(1);
  }
  return usb_sense_filter.update(quota_monitor::median_usb_sense_mv(samples));
}

void sleep_soft_off(bool runtime_active) {
  if (runtime_active) {
    WiFi.disconnect(true);
    WiFi.mode(WIFI_OFF);
    persist_snapshot_state(true);
    Serial.flush();
    ledcWrite(0, 0);
    ledcDetachPin(PIN_LCD_BACKLIGHT);
  }

  // Remove every possible phantom-power path into the load-switched display.
  digitalWrite(PIN_DISPLAY_ENABLE, LOW);
  pinMode(PIN_DISPLAY_ENABLE, OUTPUT);
  digitalWrite(PIN_LCD_BACKLIGHT, LOW);
  pinMode(PIN_LCD_BACKLIGHT, OUTPUT);
  for (const int pin : {PIN_LCD_MISO, PIN_LCD_MOSI, PIN_LCD_SCLK, PIN_LCD_CS,
                        PIN_LCD_DC, PIN_LCD_RST, PIN_SD_CS, PIN_TOUCH_CS,
                        PIN_I2C_SDA, PIN_I2C_SCL})
    pinMode(pin, INPUT);
  gpio_hold_en(static_cast<gpio_num_t>(PIN_DISPLAY_ENABLE));
  gpio_hold_en(static_cast<gpio_num_t>(PIN_LCD_BACKLIGHT));
  gpio_deep_sleep_hold_en();

  esp_sleep_enable_ext0_wakeup(static_cast<gpio_num_t>(PIN_POWER_SWITCH), 1);
  // VCC sense is not an RTC wake source on every ESP32-S3 board revision.
  // Periodically wake so inserting USB enters charge/config mode. setup()
  // checks OFF/USB before display, LVGL, I2C or Wi-Fi initialization, so these
  // checks do not create five-second screen/Wi-Fi current spikes.
  esp_sleep_enable_timer_wakeup(5ULL * 1000ULL * 1000ULL);
  esp_deep_sleep_start();
}

void enter_soft_off() {
  if (usb_present() || digitalRead(PIN_POWER_SWITCH) != LOW) return;
  sleep_soft_off(true);
}
#endif

bool external_power_present_now() {
#if QUOTA_HAS_CARRIER_POWER
  return usb_sense_filter.initialized() ? usb_sense_filter.present()
                                        : usb_present();
#elif QUOTA_HAS_EXTERNAL_POWER_SENSE
  const DeviceConfig current_config = copy_config();
  if (!current_config.external_power_sense_enabled) {
    external_power_filter.reset();
    return false;
  }
  return external_power_filter.update(
      millis(), digitalRead(PIN_EXTERNAL_POWER_SENSE) == HIGH);
#else
  return false;
#endif
}

void change_brightness() {
  DeviceConfig current_config = copy_config();
  uint8_t index = 0;
  while (index < 3 &&
         brightness_percent[index] != current_config.brightness_percent)
    ++index;
  index = static_cast<uint8_t>((index + 1U) % 3U);
  current_config.brightness_percent = brightness_percent[index];
  {
    SharedStateLock lock;
    config = current_config;
  }
  config_dirty = true;
  display_state.note_activity(millis());
  show_message(String("亮度 ") + String(current_config.brightness_percent) +
                   "%",
               2000);
}

void service_button(ButtonState& button, void (*short_action)(),
                    void (*long_action)()) {
  const bool down = digitalRead(button.pin) == LOW;
  if (down && !button.last_down) {
    button.down_since = millis();
    button.long_sent = false;
  }
  if (down && !button.long_sent && millis() - button.down_since >= kLongPressMs) {
    button.long_sent = true;
    long_action();
  }
  if (!down && button.last_down && !button.long_sent &&
      millis() - button.down_since >= 30)
    short_action();
  button.last_down = down;
}

void manual_refresh() {
  display_state.note_activity(millis());
  bool network_reserved = false;
  {
    SharedStateLock lock;
    network_reserved = candidate_test_in_flight || ota_status.installing ||
                       ota_status.result_ready || ota_status.install_success;
  }
  if (portal_active || network_reserved || restart_after_ota_ms != 0 ||
      restart_after_config_ms != 0)
    return;
  manual_refresh_gate.request(millis());
  api_backoff_ms = 1000;
  if (WiFi.status() != WL_CONNECTED) request_wifi_now();
  show_message("立即刷新", 1200);
}

void show_diagnostics() {
  String text = WiFi.status() == WL_CONNECTED ? WiFi.localIP().toString() : "OFFLINE";
  String error;
  {
    SharedStateLock lock;
    error = last_error;
  }
  if (!error.isEmpty()) text += " " + error;
  show_message(text, 6000);
}

void show_device_info() {
  const String ip = WiFi.status() == WL_CONNECTED ? WiFi.localIP().toString()
                                                  : "OFFLINE";
  String text = "FW " QUOTA_MONITOR_FIRMWARE_VERSION "\nIP " + ip;
  quota_monitor::Snapshot current_snapshot;
  bool snapshot_available = false;
  {
    SharedStateLock lock;
    snapshot_available = have_snapshot;
    if (snapshot_available) current_snapshot = snapshot;
  }
  if (snapshot_available) {
    text += "\nC " +
            local_reset_time(current_snapshot.codex.observed_at_epoch);
    text += "  A " +
            local_reset_time(current_snapshot.claude.observed_at_epoch);
  } else {
    text += "\nC --  A --";
  }
  show_message(text, 6000);
}

#if QUOTA_HAS_TOUCH
int16_t map_touch_axis(uint16_t raw, uint16_t raw_min, uint16_t raw_max,
                       int16_t resolution) {
  const long mapped = map(static_cast<long>(raw), static_cast<long>(raw_min),
                          static_cast<long>(raw_max), 30L,
                          static_cast<long>(resolution - 30));
  return static_cast<int16_t>(
      std::clamp<long>(mapped, 0L, static_cast<long>(resolution - 1)));
}

void service_touch() {
  const bool down = touch.touched();
  int16_t x = touch_state.start_x;
  if (down) {
    const TS_Point point = touch.getPoint();
    x = map_touch_axis(point.x, config.touch_x_min, config.touch_x_max, 320);
  }

  if (down && !touch_state.last_down) {
    const bool was_off = display_state.state() ==
                         quota_monitor::DisplayState::kBacklightOff;
    display_state.note_activity(millis());
    touch_state.swallow_release = was_off;
    if (was_off) manual_refresh();
    touch_state.down_since = millis();
    touch_state.long_sent = false;
    touch_state.start_x = x;
  }
  if (down && !touch_state.long_sent &&
      millis() - touch_state.down_since >= kLongPressMs) {
    touch_state.long_sent = true;
    if (touch_state.start_x < 160)
      show_diagnostics();
    else
      show_device_info();
  }
  if (!down && touch_state.last_down && !touch_state.long_sent &&
      millis() - touch_state.down_since >= 30) {
    if (!touch_state.swallow_release) manual_refresh();
  }
  if (!down) touch_state.swallow_release = false;
  touch_state.last_down = down;
}
#endif

void service_boot_button() {
  ButtonState& button = button_a;
  const bool down = digitalRead(button.pin) == LOW;
  if (down && !button.last_down) {
    const bool was_off = display_state.state() ==
                         quota_monitor::DisplayState::kBacklightOff;
    display_state.note_activity(millis());
    button.swallow_release = was_off;
    if (was_off) manual_refresh();
    button.down_since = millis();
    button.long_sent = false;
  }
  const uint32_t held_ms = millis() - button.down_since;
  if (down && !button.long_sent && held_ms >= kLongPressMs) {
    button.long_sent = true;
    show_diagnostics();
  }
  if (down && held_ms >= kPortalPressMs && !portal_active)
    start_configuration_portal();
  if (!down && button.last_down && !button.long_sent &&
      !button.swallow_release && held_ms >= 30)
    manual_refresh();
  if (!down) button.swallow_release = false;
  button.last_down = down;
}

#if QUOTA_HAS_SECOND_BUTTON
void check_boot_factory_reset() {
  if (digitalRead(PIN_BUTTON_A) != LOW || digitalRead(PIN_BUTTON_B) != LOW) return;
  const uint32_t started = millis();
  while (digitalRead(PIN_BUTTON_A) == LOW && digitalRead(PIN_BUTTON_B) == LOW) {
    if (millis() - started >= 5000) {
      clear_config();
      Serial.println("Configuration erased by buttons");
      delay(200);
      ESP.restart();
    }
    delay(20);
  }
}
#endif

void initialize_ota_self_test() {
  const esp_partition_t* running = esp_ota_get_running_partition();
  esp_ota_img_states_t state = ESP_OTA_IMG_UNDEFINED;
  if (running != nullptr && esp_ota_get_state_partition(running, &state) == ESP_OK &&
      state == ESP_OTA_IMG_PENDING_VERIFY) {
    ota_self_test_pending = true;
    ota_self_test_started_ms = millis();
    Serial.println("OTA self-test pending for 30 seconds");
  }
}

void service_ota_self_test() {
  if (!ota_self_test_pending ||
      millis() - ota_self_test_started_ms < kOtaSelfTestMs)
    return;
  if (runtime_peripherals_ready) {
    if (esp_ota_mark_app_valid_cancel_rollback() == ESP_OK) {
      ota_self_test_pending = false;
      Serial.println("OTA self-test passed; image marked valid");
      return;
    }
  }
  Serial.println("OTA self-test failed; rebooting for bootloader rollback");
  delay(50);
  ESP.restart();
}

}  // namespace

void setup() {
#if QUOTA_HAS_CARRIER_POWER
  // GPIO holds survive deep sleep. Release them before deciding whether this
  // boot is an OFF-state polling wake or a full interactive startup.
  gpio_deep_sleep_hold_dis();
  gpio_hold_dis(static_cast<gpio_num_t>(PIN_DISPLAY_ENABLE));
  gpio_hold_dis(static_cast<gpio_num_t>(PIN_LCD_BACKLIGHT));
  pinMode(PIN_BUTTON_A, INPUT_PULLUP);
  pinMode(PIN_BUTTON_B, INPUT_PULLUP);
  pinMode(PIN_POWER_SWITCH, INPUT_PULLUP);
  pinMode(PIN_USB_SENSE, INPUT);
  analogReadResolution(12);
  analogSetPinAttenuation(PIN_USB_SENSE, ADC_11db);
  if (digitalRead(PIN_POWER_SWITCH) == LOW && !usb_present())
    sleep_soft_off(false);

  // DFR0665 touch and microSD are unused on the legacy target.
  pinMode(PIN_SD_CS, OUTPUT);
  digitalWrite(PIN_SD_CS, HIGH);
  pinMode(PIN_TOUCH_CS, OUTPUT);
  digitalWrite(PIN_TOUCH_CS, HIGH);
  pinMode(PIN_DISPLAY_ENABLE, OUTPUT);
  digitalWrite(PIN_DISPLAY_ENABLE, HIGH);
#else
  pinMode(PIN_BUTTON_A, INPUT_PULLUP);
  analogReadResolution(12);
  analogSetPinAttenuation(PIN_BATTERY_ADC, ADC_11db);
#if QUOTA_HAS_EXTERNAL_POWER_SENSE
  // GPIO35 has no internal pull resistor. It is only sampled after the user
  // explicitly enables the documented external 100k/150k VBUS divider.
  pinMode(PIN_EXTERNAL_POWER_SENSE, INPUT);
#endif

  // Deselect/disable every unused onboard peripheral before the two display
  // buses start. The RGB LED is common-anode, so HIGH means off.
  for (const int pin : {PIN_SD_CS, PIN_EXTERNAL_SPI_CS, PIN_TOUCH_CS}) {
    pinMode(pin, OUTPUT);
    digitalWrite(pin, HIGH);
  }
  pinMode(PIN_TOUCH_IRQ, INPUT);
  pinMode(PIN_AUDIO_ENABLE, OUTPUT);
  digitalWrite(PIN_AUDIO_ENABLE, HIGH);
  for (const int pin : {PIN_RGB_RED, PIN_RGB_GREEN, PIN_RGB_BLUE}) {
    pinMode(pin, OUTPUT);
    digitalWrite(pin, HIGH);
  }
#endif

  Serial.begin(115200);
  delay(100);
#if QUOTA_HAS_SECOND_BUTTON
  check_boot_factory_reset();
#endif
  load_config();
  nvs_storage_ready = verify_nvs_health();
  if (!nvs_storage_ready) Serial.println("NVS self-test failed");
  display_state.configure(
      {config.dim_after_seconds, config.screen_off_after_seconds});
  display_state.begin(millis());
  print_help();

#if QUOTA_HAS_MAX17048
  Wire.begin(PIN_I2C_SDA, PIN_I2C_SCL);
#endif
  tft.begin();
#if defined(QUOTA_BOARD_E32R28T)
  keyes_ili9341_post_init(tft);
#endif
  tft.setRotation(1);
  // TFT_eSPI drives TFT_BL as a plain HIGH during begin(). Attach PWM only
  // afterwards so the selected 60% startup brightness is not overwritten.
  ledcSetup(0, 20000, 8);
  ledcAttachPin(PIN_LCD_BACKLIGHT, 0);
  apply_backlight_pwm(quota_monitor::desired_backlight_pwm(
      quota_monitor::DisplayState::kAwake, config.brightness_percent, false));
#if QUOTA_HAS_TOUCH
  // TFT_eSPI uses the ESP32's default VSPI controller. The touch controller is
  // physically wired to a separate pin set, so give it HSPI explicitly.
  touch_spi.begin(PIN_TOUCH_SCLK, PIN_TOUCH_MISO, PIN_TOUCH_MOSI,
                  PIN_TOUCH_CS);
  const bool touch_started = touch.begin(touch_spi);
  touch.setRotation(1);
#else
  constexpr bool touch_started = true;
#endif
  lv_init();
  uint32_t pixel_count = 320 * 24;
  pixels = static_cast<lv_color_t*>(
      heap_caps_malloc(320 * 24 * sizeof(lv_color_t), MALLOC_CAP_DMA));
  if (pixels == nullptr) {
    pixel_count = 320 * 12;
    pixels = new lv_color_t[pixel_count];
  }
  if (pixels == nullptr) {
    Serial.println("Display buffer allocation failed");
    delay(100);
    ESP.restart();
  }
  lv_disp_draw_buf_init(&draw_buffer, pixels, nullptr, pixel_count);
  lv_disp_drv_init(&display_driver);
  display_driver.hor_res = 320;
  display_driver.ver_res = 240;
  display_driver.flush_cb = flush_display;
  display_driver.draw_buf = &draw_buffer;
  lv_disp_drv_register(&display_driver);
  create_ui();

  WiFi.mode(WIFI_STA);
  configTzTime(config.timezone.c_str(), "pool.ntp.org", "time.cloudflare.com");
  request_wifi_now();
  shared_mutex = xSemaphoreCreateMutex();
  network_jobs = xQueueCreate(5, sizeof(NetworkJob));
  if (shared_mutex != nullptr && network_jobs != nullptr) {
    xTaskCreatePinnedToCore(network_worker, "qmon-network", 12288, nullptr, 1,
                            &network_task_handle, 0);
  }
  runtime_peripherals_ready = pixels != nullptr && touch_started &&
                               nvs_storage_ready &&
                               shared_mutex != nullptr &&
                              network_task_handle != nullptr;
  initialize_ota_self_test();
  refresh_ui();
  String why;
  if (!config_ready(why)) start_configuration_portal();
}

void loop() {
  static uint32_t previous_lv_tick = millis();
  const uint32_t now = millis();
  lv_tick_inc(now - previous_lv_tick);
  previous_lv_tick = now;
  service_serial();
  service_portal();
  service_wifi();
  service_network_results();
#if QUOTA_HAS_CARRIER_POWER
  enter_soft_off();
#endif
  service_boot_button();
#if QUOTA_HAS_SECOND_BUTTON
  service_button(button_b, change_brightness, show_device_info);
#endif
#if QUOTA_HAS_TOUCH
  service_touch();
#endif

  const bool previous_external_power = display_state.external_power_present();
  const bool physical_external_power = external_power_present_now();
#if defined(QUOTA_BOARD_E32R28T)
  const bool inferred_charging = charge_trend_detector.charging();
#else
  constexpr bool inferred_charging = false;
#endif
  const bool external_power = physical_external_power || inferred_charging;
  const quota_monitor::DisplayState next_display_state =
      display_state.update(millis(), external_power);
  if (external_power != previous_external_power) {
    // A cable transition restores the normal cadence immediately. Inserting
    // USB also wakes an offline station instead of waiting for its old backoff.
    next_fetch_ms = 0;
    api_backoff_ms = 1000;
    manual_refresh_gate.request(millis());
    if (external_power && WiFi.status() != WL_CONNECTED) request_wifi_now();
  }
  if (next_display_state != applied_display_state) {
    if (next_display_state == quota_monitor::DisplayState::kBacklightOff) {
      const uint32_t seconds = quota_monitor::screen_off_refresh_seconds(
          config.refresh_seconds, config.screen_off_refresh_seconds);
      next_fetch_ms = millis() + seconds * 1000UL;
    }
    applied_display_state = next_display_state;
  }
  const DeviceConfig current_config = copy_config();
  apply_backlight_pwm(quota_monitor::desired_backlight_pwm(
      next_display_state, current_config.brightness_percent, external_power));

  if (manual_refresh_gate.take_if_ready(millis(), fetch_in_flight))
    next_fetch_ms = 0;

  const bool ota_installing = copy_ota_status().installing;
  if (!portal_active && !ota_installing && !network_config_dirty &&
      WiFi.status() == WL_CONNECTED && !fetch_in_flight &&
      static_cast<int32_t>(millis() - next_fetch_ms) >= 0) {
    if (!clock_is_sane()) {
      show_message("正在校准时间", 1500);
      next_fetch_ms = millis() + 1000;
    } else {
      if (queue_network_job(NetworkJob::kFetchSnapshot)) {
        fetch_in_flight = true;
      } else {
        next_fetch_ms = millis() + 250U;
      }
    }
  }

  static uint32_t next_ui_ms = 0;
  if (static_cast<int32_t>(millis() - next_ui_ms) >= 0) {
    refresh_ui();
    next_ui_ms = millis() + 1000;
  }
  service_ota_self_test();
  if (restart_after_config_ms != 0 &&
      static_cast<int32_t>(millis() - restart_after_config_ms) >= 0)
    ESP.restart();
  if (restart_after_ota_ms != 0 &&
      static_cast<int32_t>(millis() - restart_after_ota_ms) >= 0) {
    ESP.restart();
  }
  lv_timer_handler();
  delay(5);
}
