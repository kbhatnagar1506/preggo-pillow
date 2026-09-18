// accel_node — one Arduino, one accelerometer, streaming to the Pi.
//
// WHY ONE PER BOARD
// -----------------
// Three identical Grove accelerometers cannot share one I2C bus. Every Grove
// I2C port on the Pi's Base HAT (and the Arduino Base Shield) is the SAME bus,
// just physically duplicated, so identical chips have identical addresses and
// collide. You would see one sensor or garbage and spend hours assuming your
// wiring was bad.
//
// FLASH ONE PER BOARD, changing NODE_ID:
//
//   Arduino #1 -> NODE_ID "abdo_a", HAS_SERVO 1, HAS_SOUND 1   (abdomen + phantom)
//   Arduino #2 -> NODE_ID "ref",    HAS_SERVO 0, HAS_SOUND 0   (worn on the back)
//
// Two accelerometers is enough: one abdominal plus the reference gives you the
// subtraction, which is the part that matters. A third abdominal node ("abdo_b")
// on the Pi's own I2C improves sensitivity and is optional.
//
// WHY THE SERVO LIVES HERE AND NOT ON THE PI
// ------------------------------------------
// The Pi's software PWM jitters. Jittery kicks mean inconsistent ground truth,
// and the whole accuracy number rests on knowing exactly when you kicked.
// Arduino has hardware PWM and the Servo library. The Pi sends "K<angle>\n".
//
// TIMESTAMPS
// ----------
// This sketch sends NO timestamp, deliberately. millis() on two boards plus the
// Pi clock drift apart, and drifting clocks make the blind-test overlay look
// broken when the detector is fine. The Pi stamps on arrival.

#include <Wire.h>

// ---- CHANGE THESE PER BOARD ---------------------------------------------
#define NODE_ID    "abdo_a"   // "abdo_a" | "abdo_b" | "ref"
#define HAS_SERVO  1          // 1 on the board wired to the phantom servo
#define HAS_SOUND  1          // 1 on the board with the contact mic
// -------------------------------------------------------------------------

#define SERVO_PIN  9          // hardware PWM pin
#define SOUND_PIN  A0         // Grove sound sensor (analog envelope out)
#define SAMPLE_HZ  100

#if HAS_SERVO
  #include <Servo.h>
  Servo kickServo;
  const int REST_ANGLE = 10;
  unsigned long kickUntil = 0;
#endif

uint8_t ADDR = 0x00;
enum ChipKind { CHIP_UNKNOWN, CHIP_ADXL345, CHIP_LIS3DH, CHIP_MMA7660 };
ChipKind chip = CHIP_UNKNOWN;

unsigned long lastSample = 0;
const unsigned long samplePeriodUs = 1000000UL / SAMPLE_HZ;

char cmd[16];
uint8_t cmdLen = 0;

void wr(uint8_t reg, uint8_t val) {
  Wire.beginTransmission(ADDR);
  Wire.write(reg); Wire.write(val);
  Wire.endTransmission();
}

bool present(uint8_t addr) {
  Wire.beginTransmission(addr);
  return Wire.endTransmission() == 0;
}

const char* chipName() {
  switch (chip) {
    case CHIP_ADXL345: return "adxl345";
    case CHIP_LIS3DH:  return "lis3dh";
    case CHIP_MMA7660: return "mma7660";
    default:           return "none";
  }
}

void setup() {
  Serial.begin(115200);
  while (!Serial) { ; }
  Wire.begin();

#if HAS_SERVO
  kickServo.attach(SERVO_PIN);
  kickServo.write(REST_ANGLE);
#endif

  // Autodetect, so you do not need to know which Grove variant you got.
  if      (present(0x53)) { ADDR = 0x53; chip = CHIP_ADXL345; }
  else if (present(0x19)) { ADDR = 0x19; chip = CHIP_LIS3DH;  }
  else if (present(0x18)) { ADDR = 0x18; chip = CHIP_LIS3DH;  }
  else if (present(0x4C)) { ADDR = 0x4C; chip = CHIP_MMA7660; }

  switch (chip) {
    case CHIP_ADXL345:
      wr(0x2D, 0x08);   // POWER_CTL: measure
      wr(0x31, 0x0B);   // DATA_FORMAT: full res, +/-16g
      wr(0x2C, 0x0C);   // BW_RATE: 400 Hz
      break;
    case CHIP_LIS3DH:
      wr(0x20, 0x77);   // CTRL_REG1: 400 Hz, XYZ enabled
      wr(0x23, 0x88);   // CTRL_REG4: high res, block data update
      break;
    case CHIP_MMA7660:
      wr(0x07, 0x00);   // standby so MODE is writable
      wr(0x08, 0x01);   // 120 samples/sec
      wr(0x07, 0x01);   // active
      break;
    default: break;
  }

  // The hello line is how the Pi learns which node this is and which chip it
  // found. It is also how you confirm Branch A vs Branch B without running
  // i2cdetect separately.
  Serial.print("{\"hello\":\""); Serial.print(NODE_ID);
  Serial.print("\",\"chip\":\"");  Serial.print(chipName());
  Serial.print("\",\"addr\":");    Serial.print(ADDR);
  Serial.print(",\"servo\":");     Serial.print(HAS_SERVO);
  Serial.print(",\"sound\":");     Serial.print(HAS_SOUND);
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
      const float s = 0.0039f;           // 3.9 mg/LSB, full-res
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
      const float s = 0.001f;            // approx, +/-2g high-res
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
      const float s = 1.5f / 32.0f;      // 6-bit over +/-1.5g
      x = v[0] * s; y = v[1] * s; z = v[2] * s;
      return true;
    }
    default: return false;
  }
}

// handleCommands parses "K<angle>\n" from the Pi and fires the servo.
void handleCommands() {
  while (Serial.available()) {
    char c = Serial.read();
    if (c == '\n' || c == '\r') {
      cmd[cmdLen] = '\0';
      if (cmdLen > 1 && cmd[0] == 'K') {
#if HAS_SERVO
        int angle = atoi(cmd + 1);
        if (angle < REST_ANGLE) angle = REST_ANGLE;
        if (angle > 120) angle = 120;
        kickServo.write(angle);
        kickUntil = millis() + 120;   // hold, then snap back in loop()
#endif
      }
      cmdLen = 0;
    } else if (cmdLen < sizeof(cmd) - 1) {
      cmd[cmdLen++] = c;
    }
  }
}

void loop() {
  handleCommands();

#if HAS_SERVO
  if (kickUntil && millis() > kickUntil) {
    kickServo.write(REST_ANGLE);
    kickUntil = 0;
  }
#endif

  unsigned long now = micros();
  if (now - lastSample < samplePeriodUs) return;
  lastSample = now;

  float x, y, z;
  if (!readAxes(x, y, z)) return;

  Serial.print("{\"n\":\""); Serial.print(NODE_ID);
  Serial.print("\",\"x\":");  Serial.print(x, 4);
  Serial.print(",\"y\":");    Serial.print(y, 4);
  Serial.print(",\"z\":");    Serial.print(z, 4);
#if HAS_SOUND
  // The Grove sound sensor outputs an analog envelope, not a waveform, so
  // there is no spectrum to take. Envelope RMS is all you get, and it is
  // enough for "did something thud".
  Serial.print(",\"s\":");    Serial.print(analogRead(SOUND_PIN));
#endif
  Serial.println("}");
}
