#include <Arduino.h>
#include <TFT_eSPI.h>

namespace {

constexpr int kBacklightPin = 21;
TFT_eSPI tft;

struct TestFrame {
  uint16_t color;
  uint16_t text_color;
  const char* name;
};

constexpr TestFrame kFrames[] = {
    {TFT_RED, TFT_WHITE, "RED"},
    {TFT_GREEN, TFT_BLACK, "GREEN"},
    {TFT_BLUE, TFT_WHITE, "BLUE"},
    {TFT_WHITE, TFT_BLACK, "WHITE"},
    {TFT_BLACK, TFT_WHITE, "BLACK"},
};

void draw_frame(const TestFrame& frame) {
  tft.fillScreen(frame.color);
  tft.setTextDatum(MC_DATUM);
  tft.setTextColor(frame.text_color, frame.color);
  tft.drawString("E32R28T LCD TEST", 160, 100, 4);
  tft.drawString(frame.name, 160, 140, 4);
  Serial.printf("LCD frame: %s\n", frame.name);
}

}  // namespace

void setup() {
  Serial.begin(115200);
  delay(250);
  Serial.println("E32R28T LCD smoke test starting");

  // Keep the panel visibly powered even if display-controller setup fails.
  pinMode(kBacklightPin, OUTPUT);
  digitalWrite(kBacklightPin, HIGH);
  delay(100);

  tft.begin();
  tft.setRotation(1);
  Serial.printf("TFT size: %d x %d\n", tft.width(), tft.height());
  Serial.printf("RDDID(0x04): %02X %02X %02X %02X\n",
                tft.readcommand8(0x04, 0), tft.readcommand8(0x04, 1),
                tft.readcommand8(0x04, 2), tft.readcommand8(0x04, 3));
  Serial.printf("RDDID4(0xD3): %02X %02X %02X %02X\n",
                tft.readcommand8(0xD3, 0), tft.readcommand8(0xD3, 1),
                tft.readcommand8(0xD3, 2), tft.readcommand8(0xD3, 3));
  draw_frame(kFrames[0]);
}

void loop() {
  static size_t frame_index = 0;
  static uint32_t next_frame_ms = millis() + 2000;
  if (static_cast<int32_t>(millis() - next_frame_ms) >= 0) {
    frame_index = (frame_index + 1) % (sizeof(kFrames) / sizeof(kFrames[0]));
    draw_frame(kFrames[frame_index]);
    next_frame_ms = millis() + 2000;
  }
  delay(5);
}
