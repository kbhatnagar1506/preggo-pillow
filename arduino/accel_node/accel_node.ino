// accel_node — one Arduino, one accelerometer, streaming to the Pi.
//
// WHY THIS EXISTS
// ---------------
// Three identical Grove accelerometers cannot share one I2C bus. Every Grove
// I2C port on the Pi's Base HAT is the SAME bus, just physically duplicated, so
// three identical chips means three identical addresses and they collide. You
// would see one sensor or garbage, and you would spend hours assuming your
// wiring was bad.
//
// So each accelerometer gets its own board:
//
//   Raspberry Pi   -> accel #1 (abdominal)  + sound + button + LEDs + servo
//   Arduino Uno R3 -> accel #2 (abdominal)  -> USB serial -> Pi
//   Arduino Uno R4 -> accel #3 (reference)  -> USB serial -> Pi
//
// FLASH ONE PER BOARD, changing NODE_ID below.
//
// TIMESTAMPS
// ----------
// This sketch sends NO timestamp. That is deliberate. millis() on two boards
// and the Pi clock drift apart, and drifting clocks make the blind-test overlay
// look broken when the detector is fine. The Pi stamps everything on arrival.

#include <Wire.h>

// ---- CHANGE THIS PER BOARD ----------------------------------------------
#define NODE_ID "abdo_b"   // "abdo_a" | "abdo_b" | "ref"
// -------------------------------------------------------------------------

#define SAMPLE_HZ 100

// Run i2cdetect on the Pi (or the scan below) and set this to what you find.
//   0x53 -> ADXL345     (good: 10-13 bit)
//   0x19 -> LIS3DHTR    (good: 8-12 bit)
//   0x18 -> LIS3DHTR    (alternate address)
//   0x4C -> MMA7660FC   (6-bit: acoustic channel becomes primary, see docs)
uint8_t ADDR = 0x00;

enum ChipKind { CHIP_UNKNOWN, CHIP_ADXL345, CHIP_LIS3DH, CHIP_MMA7660 };
ChipKind chip = CHIP_UNKNOWN;

unsigned long lastSample = 0;
const unsigned long samplePeriodUs = 1000000UL / SAMPLE_HZ;

void wr(uint8_t reg, uint8_t val) {
  Wire.beginTransmission(ADDR);
  Wire.write(reg);
  Wire.write(val);
  Wire.endTransmission();
}

bool present(uint8_t addr) {
  Wire.beginTransmission(addr);
  return Wire.endTransmission() == 0;
}

void setup() {
  Serial.begin(115200);
  while (!Serial) { ; }
  Wire.begin();

  // Autodetect, so you do not have to know which Grove variant you got.
  if (present(0x53))      { ADDR = 0x53; chip = CHIP_ADXL345; }
  else if (present(0x19)) { ADDR = 0x19; chip = CHIP_LIS3DH;  }
  else if (present(0x18)) { ADDR = 0x18; chip = CHIP_LIS3DH;  }
  else if (present(0x4C)) { ADDR = 0x4C; chip = CHIP_MMA7660; }

  switch (chip) {
    case CHIP_ADXL345:
      wr(0x2D, 0x08);       // POWER_CTL: measure mode
      wr(0x31, 0x0B);       // DATA_FORMAT: full res, +/-16g
      wr(0x2C, 0x0C);       // BW_RATE: 400 Hz output
      break;
    case CHIP_LIS3DH:
      wr(0x20, 0x77);       // CTRL_REG1: 400 Hz, all axes enabled
      wr(0x23, 0x88);       // CTRL_REG4: high res, block data update
      break;
    case CHIP_MMA7660:
      wr(0x07, 0x00);       // standby so MODE is writable
      wr(0x08, 0x01);       // SR: 120 samples/sec
      wr(0x07, 0x01);       // active
      break;
    default:
      break;
  }

  Serial.print("{\"hello\":\""); Serial.print(NODE_ID);
  Serial.print("\",\"chip\":\"");
  switch (chip) {
    case CHIP_ADXL345: Serial.print("adxl345"); break;
    case CHIP_LIS3DH:  Serial.print("lis3dh");  break;
    case CHIP_MMA7660: Serial.print("mma7660"); break;
    default:           Serial.print("none");    break;
  }
  Serial.print("\",\"addr\":"); Serial.print(ADDR);
  Serial.println("}");
}

bool readAxes(float &x, float &y, float &z) {
  switch (chip) {
    case CHIP_ADXL345: {
      Wire.beginTransmission(ADDR); Wire.write(0x32); Wire.endTransmission(false);
      Wire.requestFrom(ADDR, (uint8_t)6);
      if (Wire.available() < 6) return false;
      int16_t rx = Wire.read() | (Wire.read() << 8);
      int16_t ry = Wire.read() | (Wire.read() << 8);
      int16_t rz = Wire.read() | (Wire.read() << 8);
      const float s = 0.0039f;        // 3.9 mg/LSB in full-res mode
      x = rx * s; y = ry * s; z = rz * s;
      return true;
    }
    case CHIP_LIS3DH: {
      Wire.beginTransmission(ADDR); Wire.write(0x28 | 0x80); Wire.endTransmission(false);
      Wire.requestFrom(ADDR, (uint8_t)6);
      if (Wire.available() < 6) return false;
      int16_t rx = Wire.read() | (Wire.read() << 8);
      int16_t ry = Wire.read() | (Wire.read() << 8);
      int16_t rz = Wire.read() | (Wire.read() << 8);
      const float s = 0.001f;         // approx, +/-2g high-res
      x = (rx >> 4) * s; y = (ry >> 4) * s; z = (rz >> 4) * s;
      return true;
    }
    case CHIP_MMA7660: {
      Wire.beginTransmission(ADDR); Wire.write(0x00); Wire.endTransmission(false);
      Wire.requestFrom(ADDR, (uint8_t)3);
      if (Wire.available() < 3) return false;
      int8_t v[3];
      for (int i = 0; i < 3; i++) {
        uint8_t b = Wire.read() & 0x3F;
        v[i] = (b > 31) ? (int8_t)(b - 64) : (int8_t)b;
      }
      const float s = 1.5f / 32.0f;   // 6-bit over +/-1.5g
      x = v[0] * s; y = v[1] * s; z = v[2] * s;
      return true;
    }
    default:
      return false;
  }
}

void loop() {
  unsigned long now = micros();
  if (now - lastSample < samplePeriodUs) return;
  lastSample = now;

  float x, y, z;
  if (!readAxes(x, y, z)) return;

  // Compact JSON lines. The Pi parses these and stamps the time on arrival.
  Serial.print("{\"n\":\""); Serial.print(NODE_ID);
  Serial.print("\",\"x\":"); Serial.print(x, 4);
  Serial.print(",\"y\":");   Serial.print(y, 4);
  Serial.print(",\"z\":");   Serial.print(z, 4);
  Serial.println("}");
}
