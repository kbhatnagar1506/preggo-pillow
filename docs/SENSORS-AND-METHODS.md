# Sensors, algorithms and sources

Everything below is read out of the code in this repository, not from memory.
Where the repository does not support a claim, it says so.

---

## 1. Sensors

### 1.1 Accelerometers — the primary sensor

Three of them. Two on the abdomen, one on the back as a reference. The driver
identifies the chip by I2C address and decodes accordingly, so three different
parts can be mixed on one rig (`internal/sensor/accel.go`).

| Chip | I2C address | Resolution | Max rate | Notes |
|---|---|---|---|---|
| ADXL345 | `0x53` | 10–13 bit | 3200 Hz | the best of the three |
| LIS3DH / LIS3DHTR | `0x19`, alt `0x18` | 8–12 bit | 5.3 kHz | needs bit 7 set to auto-increment across registers |
| MMA7660FC | `0x4C` | 6-bit over ±1.5 g | 120 Hz | the Grove part; **0.047 g per count** |

The MMA7660 number is the one that matters. Fetal movement at the abdominal
wall is roughly 0.05–0.3 g, so the weakest movements fall **below a single
count**. Not faint: unrepresentable. The code detects which chip is present and
warns when it is on the coarse one.

Three decoders exist because the formats differ: right-justified 16-bit
two's complement (ADXL345), left-justified 12-bit with the value in the high
bits (LIS3DH, `decodeLE16Shift4`), and three 6-bit two's-complement values
(MMA7660, `decode6Bit`).

### 1.2 Acoustic channel — the second opinion

A Grove sound sensor, read as an envelope (0–1023) rather than a waveform
(`internal/sensor/serial.go`, field `s`). The detector uses it as a **gate**,
not as a source: an accelerometer event must be corroborated by acoustic energy
above its own rolling floor, or it is discarded.

The code is deliberately indifferent to which microphone this is. Any part that
produces an envelope works.

### 1.3 Grove Base HAT ADC — analog inputs

Lives at **`0x04`** (`internal/sensor/adc_linux.go`). This address sits *below*
the `0x08`–`0x77` range that `i2cdetect` and `i2cget` scan by default, so the
HAT looks absent unless you scan with `-a`. We wrongly declared it dead once.

### 1.4 Rotary angle sensor — the human channel

A Grove rotary angle sensor read through that ADC, used as the "I felt that"
dial. One notch per movement she noticed. It writes to the same `presses` table
the on-screen button writes to, so the three-way comparison does not care which
input was used.

Turn detection measures stillness against the last *significantly different*
reading, not sample-to-sample: a deliberate turn moves about 14 mV between
samples at 100 Hz, which is below the jitter threshold, so a smooth sweep read
as perfectly still and no gesture was ever noticed.

### 1.5 Phone accelerometers — alternative source

Over HTTP from [phyphox](https://phyphox.org) (`internal/sensor/phone.go`).
16-bit over ±2 g, noise-limited around 0.001–0.003 g, so 20–50× finer than the
Grove part. iOS serves on port 80, Android on 8080. Values arrive in m/s² and
are converted with `gravity = 9.80665`.

### 1.6 Presage SmartSpectra — maternal vitals, contactless

Camera-based rPPG for the **mother's** pulse and breathing
(`internal/vitals/vitals.go`).

- FDA 510(k) cleared, **K254169**
- **RMSE 1.32 bpm** pulse, **1.75 brpm** breathing
- SDK only, no REST API, so `tools/presage-bridge` runs the SDK and POSTs to
  `/api/vitals`

rPPG reads surface blood flow from a face. It measures the mother. It cannot
measure a fetus, and the code never mixes the two sources in one summary.

### 1.7 Actuator, not a sensor

A 9 g micro servo (SG90 class) driving a card paddle behind foam: the phantom
that fires a known kick. Driven either through a Grove I2C motor driver
(3-byte commands, default address `0x0F`) or sysfs PWM.

### 1.8 What is deliberately absent

**There is no fetal heart rate sensor**, and no plan for one. Fetal heart rate
requires Doppler ultrasound. No accelerometer and no camera reads it through the
abdominal wall. The dashboard keeps the tile, badged `no sensor`, showing an em
dash and the reason. A test prevents that disclaimer being deleted.

---

## 2. Algorithms

### 2.1 Reference subtraction — the core idea

```
residual = mean(reporting abdominal nodes) − reference
```

A single accelerometer cannot distinguish a kick from the mother rolling over;
both are just acceleration. Anything *she* does reaches all three sensors at
once and cancels. What survives happened at the abdomen only.

Two rules that are not obvious:

**Average only the abdominal nodes actually reporting**, and **refuse to emit
anything at all without a reference node.** A missing reference is not a
degraded mode. It is a mode that silently counts the mother as the baby.

### 2.2 Per-axis high-pass, then magnitude

`internal/detect/detect.go`, `channelState`. A rolling-mean high-pass runs on
**each axis independently**, and the magnitude is taken from the high-passed
vector.

Doing it the other way round — magnitude first, then high-pass — hides any
motion perpendicular to gravity. A real 0.3 g kick read as **0.044 g**, under
any sane threshold. The detector sat at 33% recall until this was fixed, and it
presented as a threshold problem.

### 2.3 Detector constants (`DefaultConfig`)

| Parameter | Value | Why |
|---|---|---|
| `HighPassWindow` | 2 s | history the rolling mean covers |
| `Threshold` | 0.11 g | residual above which a kick is declared |
| `Refractory` | 900 ms | a single kick rings; without this one event registers several times |
| `RequireAcoustic` | true | the two-channel argument, enforced |
| `AcousticWindow` | 250 ms | how far either side the acoustic event may sit |
| `AcousticThreshold` | 10.0 | acoustic RMS above its own rolling floor |

Sample rate is **100 Hz**.

### 2.4 Baseline — median, not mean

`internal/store/store.go`, `Baseline()`. The median of the prior nights,
excluding the most recent, over a window of up to 60 nights.

Median because one restless night should not move the bar.

The alert fires only when the deviation is **≥25% below baseline on two
consecutive nights**. Fetal sleep cycles run 20–40 minutes and babies have
genuinely quiet nights, so a single-night alarm would be noise.

### 2.5 Noon-to-noon bucketing

The nightly rollup in TigerData buckets **noon to noon**, not by calendar day.
Sleep crosses midnight, so a calendar-day bucket splits one night into two rows
and halves both, turning one normal night into two apparently reduced ones and
firing a false alert.

### 2.6 Maternal metrics

`internal/maternal/maternal.go`, all derived from the reference node.

- **Respiration** is measured on **whichever axis actually carries the
  oscillation**, because the sensor gets taped on by hand and its orientation is
  not knowable in advance. Cycles are counted with hysteresis so noise near zero
  does not register as breaths. Signals above ~0.6 Hz are smoothed out as noise.
- **Respiration quality** is a first-class value: `RespUnmeasured`,
  `RespProvisional`, `RespMeasured`. A rate that only just clears the plausible
  range is reported as *not measured* rather than as a number, because a
  borderline estimate narrated as fact is worse than a gap on a page a clinician
  reads.
- **Posture** and supine minutes, from the gravity vector.
- **Sleep fragmentation**, from movement bursts large enough to look like a wake.
- **Snore burden**, the share of acoustic samples above the rolling floor.

### 2.7 Blind-test scoring

`ScoreWindow()` matches `commands` (what the servo was told to do) against
`detections` (what the sensors found) and `presses` (what a human said they
felt), inside a **900 ms** tolerance, producing machine hits, machine misses,
machine false positives and the human rate.

The count shown on screen reads `detections` **only** and never touches
`commands`. The detector is handed accelerometer samples and has no idea a
command was ever issued.

### 2.8 Abandoned: fetal heartbeat from the contact microphone

**This code is not in the repository.** It was implemented, it did not work, and
it was removed rather than shipped.

The method followed the phonocardiography literature: bandpass **40–500 Hz**,
**average Shannon energy** `E = −x²·log(x²)`, **Hann** windowing, margin off the
band edges.

It produced three clean, convincing, periodic "detections". All three were
artefacts of our own code:

1. a biquad's startup transient becoming the normalisation divisor
2. a lobe of our own moving average
3. mains hum sitting at a band edge

An earlier version also used a 20–55 Hz band, which is simply the wrong band for
heart sounds.

The limit is the sensor, not the algorithm. A Grove microphone cannot resolve a
fetal heart tone through an abdominal wall.

---

## 3. Guideline and sources

### 3.1 The clinical guideline

The only clinical authority this project implements:

> **RCOG Green-top Guideline No. 57 — Reduced Fetal Movements**
> Royal College of Obstetricians and Gynaecologists
> https://www.rcog.org.uk/guidance/browse-all-guidance/green-top-guidelines/reduced-fetal-movements-green-top-guideline-no-57/

Defined in `internal/clinical/summary.go` as `GuidelineRef` and `GuidelineURL`,
and rendered beside the note on the report. A test asserts the citation actually
reaches the page.

What the project takes from it:

- **"There is no uniform threshold of fetal movements above which perinatal
  morbidity increases."** Compare only to this baby's own established pattern.
  "Ten kicks in two hours" is not a rule the guideline endorses.
- **Repeat episodes carry the risk.** Reduced movement on two or more occasions
  is associated with increased risk of stillbirth, growth restriction and
  preterm birth.
- **Around 70% of single episodes are uncomplicated**, so one quiet night must
  not be overstated.
- **Assessment belongs to a clinician** — a CTG and a scan. The device does not
  diagnose and is never permitted to reassure.

Thirteen tests pin the individual clauses, one guards against phrasing that
could license reassurance, and a wire test asserts the rules actually reach the
model rather than only sitting in a constant.

### 3.2 Standards and specifications implemented

| Source | Where |
|---|---|
| OpenID Connect Core — Authorization Code with PKCE (RFC 7636, S256) | `internal/auth/oidc.go` |
| FDA 510(k) K254169 — Presage SmartSpectra clearance | `internal/vitals/vitals.go` |
| Datasheets: ADXL345, LIS3DH/LIS3DHTR, MMA7660FC | `internal/sensor/accel.go` |
| Linux I2C `ioctl` interface, `I2C_SLAVE_FORCE` (`0x0706`) | `internal/sensor/i2c_linux.go` |
| Device-tree `i2c-gpio` overlay, for one bit-banged bus per sensor | `docs/HARDWARE.md` |

### 3.3 Honest note on citations

Beyond RCOG GTG-57, the FDA clearance number and the component datasheets, **this
project does not cite specific academic papers.** The phonocardiography method in
§2.8 describes standard, widely published technique (bandpass, Shannon energy,
Hann windowing) that was implemented from general knowledge of the approach, not
from a particular paper tracked in this repository. It would be easy to attach a
plausible citation here. It would not be true, so there isn't one.

The physiological figures used throughout — fetal movement at the abdominal wall
being roughly 0.05–0.3 g, fetal sleep cycles of 20–40 minutes — are working
assumptions used to size the system, not measurements this project made or
verified.

---

## 4. Measurements this project actually made

| Measurement | Result |
|---|---|
| Bench detection | 29 of 30 movements detected |
| Bench false positives | 0 across 20 deliberate maternal movements |
| Live residual | 0.335 g |
| Live maternal rejection | 5 maternal movements → 0 detections |
| Blind test (this build) | pillow 3/3, person 1/3, 0 false positives |
| Test suite | 304 tests passing under `-race` |

No overnight recording from a real pregnancy has been made. Nothing here has
been validated against CTG or any clinical reference. This is a hackathon
project, not a medical device.
