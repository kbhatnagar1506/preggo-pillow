# Lull

Passive fetal movement monitoring, measured against **her own baby's baseline**.

> The first sign a baby is in trouble is that he moves less, and the only
> instrument we have ever given her to catch it is her memory of yesterday.
> She doesn't have one, because yesterday she wasn't sure either.

HealthHER track. Built on the MLH hardware lab.

---

## Run it right now, no hardware

```bash
make run
```

Then open <http://localhost:8080>. Everything works against a simulated sensor
source: the trend, the override, the blind test, the whole demo.

**This is the insurance policy.** Build the entire product against `-source=sim`
first. Record real sensor traces at hour 8. After that the software never has to
depend on live hardware again, so a dead cable at hour 30 costs you nothing.

---

## What it does

Three accelerometers and a contact microphone. Two of the accelerometers sit on
the abdomen, the third sits on the back as a **reference**.

That reference is the whole trick. When *she* moves, all three sensors spike
together and the subtraction cancels it. When the fetus moves, only the
abdominal pair spikes and the residual survives.

Published work puts a single abdominal accelerometer at roughly 50% detection,
corrupted by maternal breathing and coughing, and finds that adding a reference
sensor away from the abdomen is what fixes it.

### Measured on the simulator

| Kick strength | Fired | Caught |
|---|---|---|
| weak | 10 | 9 |
| medium | 10 | 10 |
| strong | 10 | 10 |
| **total** | **30** | **29 (96.7%)** |

False positives from 30 kicks: **0**.
False positives from 20 simulated maternal movements: **0**.

---

## The one thing to keep straight

The count on screen comes from **detections**, never from the servo firing.

| Table | Written when | Used for |
|---|---|---|
| `commands` | the servo fires | ground truth **only** |
| `detections` | the sensors decide a kick happened | **the count** |
| `presses` | a human says "I felt one" | the blind test, and labels in the real product |

A judge will ask "does the count come from the sensor, or from the thing that
made the kick?" Keeping these separate is why the answer is good, and it is also
what lets the system score itself live.

---

## Layout

```
cmd/lull/           main: wiring, seeding, HTTP
internal/sensor/    data model, simulator, (serial ingest goes here)
internal/detect/    per-axis high-pass, reference subtraction, threshold
internal/kicker/    the phantom's servo scheduler. DEMO RIG, not the product.
internal/store/     SQLite. The blind-test score is one query.
internal/api/       SSE stream + JSON endpoints
web/static/         single-file dashboard, no dependencies
arduino/accel_node/ one sketch per accelerometer board
scripts/            Pi setup, hotspot
```

---

## Hardware

### The I2C collision, read this first

**Three identical Grove accelerometers cannot share one I2C bus.** Every Grove
I2C port on the Pi's Base HAT is the *same* bus, just physically duplicated, so
three identical chips have three identical addresses and collide. You would see
one sensor or garbage and assume your wiring was bad.

One accelerometer per board:

| Board | Sensor |
|---|---|
| Raspberry Pi | accel #1 (abdominal) + sound + button + LEDs + servo |
| Arduino Uno R3 | accel #2 (abdominal) → USB serial → Pi |
| Arduino Uno R4 | accel #3 (reference, back) → USB serial → Pi |

### Which accelerometer do you have?

```bash
i2cdetect -y 1
```

| Address | Part | Branch |
|---|---|---|
| `0x53` | ADXL345, 10-13 bit | **A** — accelerometer primary |
| `0x19` / `0x18` | LIS3DHTR, 8-12 bit | **A** — accelerometer primary |
| `0x4C` | MMA7660FC, **6-bit** | **B** — acoustic becomes primary |

`arduino/accel_node.ino` autodetects all three. Commit to a branch at hour 2 so
nobody argues about it at hour 14.

### Grab list

4× accelerometer · 2× Pi + Grove Base Shield · 2× servo · 2× sound sensor ·
1× motor/servo driver · 2× push button · 4× LED socket · 6× LED ·
1× rotary angle · 2× microSD · 2× USB supply · 12× Grove cable · tape

Buy: a foam pad or gel pack. That's the whole shopping list.

---

## Deploy to the Pi

Go cross-compiles with no cgo, so it is one static binary and nothing to install
on the Pi.

```bash
make pi                                    # build for arm64
make deploy PI=pi@raspberrypi.local        # scp it over
```

On the Pi, once:

```bash
./scripts/setup-pi.sh      # enables I2C, installs i2c-tools, runs i2cdetect
./scripts/hotspot-pi.sh    # Pi serves its own WiFi
```

**Nothing in the live demo may depend on venue WiFi.** The Pi runs its own
hotspot, the laptop joins it, the dashboard is served from the Pi.

---

## The demo

1. **Warm-up.** A judge fires one kick. Count moves. Contact-mic audio ON.
2. **Blind test.** Randomized schedule, some kicks deliberately weak. Judge
   presses the button when they think they felt one. **Audio OFF** — otherwise
   you are handing them the answers.
3. **Reveal.** Their presses against the detections. Audio back on, replay the
   ones they missed.
4. **Try to fool it.** Shake it, cough, bump the table. The count must not move.
5. **The flip.** Every maternal panel green, fetal down 41%, screen goes red.

**Run the blind test second, not last.** Teams save their best moment for the
end, by which time judges have decided.

---

## Honest limits, say these before a judge does

- **Reduced fetal movement is a late sign.** It can already mean irreversible
  compromise. It is also the only sign she has access to, and the majority of
  stillbirths are preceded by 3-4 days of it.
- **Kick counting has never been shown to reduce stillbirth.** The signal is
  real; the instrument is the failure.
- **The phantom is a test rig, not the product.** A real fetus replaces the servo.
- **The 14-night baseline is simulated** and is labelled as such on screen.
- **Not a medical device.** It reports a deviation from her own baseline and
  tells her to contact her provider. It never says the baby is fine — that is
  the home-Doppler failure mode, and we designed against it deliberately.
