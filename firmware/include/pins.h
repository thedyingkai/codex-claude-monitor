#pragma once

// Board capabilities are compile-time constants so GPIOs from one target are
// never even referenced by the other target. This matters on the classic ESP32:
// GPIO6..11 belong to its flash interface and must not inherit the old carrier's
// GPIO6/GPIO7 USB-sense/load-switch assignments.
#if defined(QUOTA_BOARD_E32R28T)

#define QUOTA_HAS_MAX17048 0
#define QUOTA_HAS_CARRIER_POWER 0
#define QUOTA_HAS_EXTERNAL_POWER_SENSE 1
#define QUOTA_HAS_SECOND_BUTTON 0
#define QUOTA_HAS_TOUCH 1

// Keyes 62520093 E32R28T (ESP32-WROOM-32E + ILI9341 + XPT2046).
constexpr int PIN_LCD_MISO = 12;
constexpr int PIN_LCD_MOSI = 13;
constexpr int PIN_LCD_SCLK = 14;
constexpr int PIN_LCD_CS = 15;
constexpr int PIN_LCD_DC = 2;
constexpr int PIN_LCD_RST = -1;  // LCD reset is tied to ESP32 EN.
constexpr int PIN_LCD_BACKLIGHT = 21;

constexpr int PIN_TOUCH_SCLK = 25;
constexpr int PIN_TOUCH_MOSI = 32;
constexpr int PIN_TOUCH_MISO = 39;
constexpr int PIN_TOUCH_CS = 33;
constexpr int PIN_TOUCH_IRQ = 36;  // Active low; input-only GPIO.
constexpr int PIN_BATTERY_ADC = 34;
// Optional USB +5 V detector input. GPIO35 must only be enabled after the
// documented 100k/150k divider and external pulldown are installed.
constexpr int PIN_EXTERNAL_POWER_SENSE = 35;

constexpr int PIN_SD_CS = 5;
constexpr int PIN_EXTERNAL_SPI_CS = 27;
constexpr int PIN_AUDIO_ENABLE = 4;  // Active low; keep high when unused.
constexpr int PIN_RGB_RED = 22;      // Common-anode RGB LED, active low.
constexpr int PIN_RGB_GREEN = 16;
constexpr int PIN_RGB_BLUE = 17;
constexpr int PIN_BUTTON_A = 0;  // BOOT button; only use after firmware boot.

#elif defined(QUOTA_BOARD_FIREBEETLE2)

#define QUOTA_HAS_MAX17048 1
#define QUOTA_HAS_CARRIER_POWER 1
#define QUOTA_HAS_EXTERNAL_POWER_SENSE 0
#define QUOTA_HAS_SECOND_BUTTON 1
#define QUOTA_HAS_TOUCH 0

// Legacy DFRobot FireBeetle 2 ESP32-S3 (DFR0975) GDI mapping.
// DFR0665 touch and micro-SD are intentionally unused on this target.
constexpr int PIN_LCD_MISO = 16;
constexpr int PIN_LCD_MOSI = 15;
constexpr int PIN_LCD_SCLK = 17;
constexpr int PIN_LCD_CS = 18;
constexpr int PIN_LCD_DC = 3;
constexpr int PIN_LCD_RST = 38;
constexpr int PIN_LCD_BACKLIGHT = 21;
constexpr int PIN_SD_CS = 9;      // DFR0665 microSD is unused; hold deselected.
constexpr int PIN_TOUCH_CS = 12;  // DFR0665 XPT2046 is unused; hold deselected.

constexpr int PIN_I2C_SDA = 1;
constexpr int PIN_I2C_SCL = 2;
constexpr int PIN_BUTTON_A = 47;       // FireBeetle onboard/user button line.
constexpr int PIN_BUTTON_B = 8;        // Carrier tactile switch to GND.
constexpr int PIN_POWER_SWITCH = 5;    // ON=open/high, OFF=closed to GND.
constexpr int PIN_USB_SENSE = 6;       // 100k/100k divider from USB/VCC 5 V.
constexpr int PIN_DISPLAY_ENABLE = 7;  // AP22804 load-switch EN.

constexpr uint8_t MAX17048_ADDRESS = 0x36;

#else
#error "Select exactly one supported quota-monitor board profile"
#endif
