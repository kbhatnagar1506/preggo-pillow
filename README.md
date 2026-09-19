# Preggo Pillow

**A pregnancy pillow that counts your baby's movement overnight and tells you when
tonight is different from your baby's own normal.**

Reduced fetal movement is one of the earliest warnings that something is wrong.
The advice given to pregnant women is to count kicks — but the clinical guideline
is explicit that no universal threshold exists. What matters is a change from
*her* baby's pattern.

So the real task is: notice a gradual decline, against a baseline nobody ever gave
you, while half asleep, at three in the morning.

This measures it instead.

> "There is no uniform threshold of fetal movements above which perinatal
> morbidity increases." — RCOG Green-top Guideline No. 57

---

## The one idea that makes it work

A single accelerometer on the abdomen cannot tell a kick from the mother rolling
over. Both are just acceleration.

So there are **three** sensors: two on the abdomen, one on her back. Anything
*she* does — breathing, turning, getting up — reaches all three at once.
Subtract the reference node and what remains happened at the abdomen only.

```
       abdo_a ─┐
       abdo_b ─┼─→  residual = abdominal − reference  →  detector
       ref    ─┘
```

Measured on the bench: **29 of 30 movements detected, 0 false positives across
20 deliberate maternal movements.**

Two implementation details that were not obvious and cost real debugging time:

- **High-pass each axis independently, then take the magnitude.** Doing it the
  other way round hides any motion perpendicular to gravity — a 0.3 g kick reads
  as 0.044 g, and the detector sat at 33% until this was fixed.
- **Average only the abdominal nodes that are actually reporting**, and refuse to
  emit anything at all without a reference node. A missing reference is not a
  degraded mode; it is a mode that silently counts the mother as the baby.

---

## Quick start

```bash
go run ./cmd/lull -source sim
```

Then open:

| URL | What it is |
|---|---|
| `http://localhost:8080/` | the landing page |
| `http://localhost:8080/dashboard` | the live operator dashboard |
| `http://localhost:8080/report` | the clinician's record |
| `http://localhost:8080/krishnabhatnagar` | the phone remote |

Nothing external is required — `-source sim` runs the whole product against a
simulated pregnancy. Sponsors (Backboard, TigerData, Gemini, Vapi) activate only
if their keys are present in `.env`, and the app runs fine without any of them.

---

## Running the demo

The demo is three moments, in this order, because each sets up the next.

**1. It counts what you do.** Open the phone remote, hand the phone to someone,
let them tap `weak` / `medium` / `strong`. The count moves on the big screen.

**2. It ignores what you do to the whole thing.** Hit `Maternal movement` — that
injects motion on *every* node at once, exactly like the mother turning over.
**The count does not move.** Fifteen seconds, and it proves the architecture in a
way no slide can.

**3. They miss what it catches.** Tap `I felt that` for each movement they notice.
Then compare:

```
fired      12
she felt    4
detected   11
```

That gap is the entire clinical premise, demonstrated rather than asserted.

Verified end to end: 3 commands → 3 detections; 5 maternal movements → **zero**
additional detections.

### When the hardware dies

It will. Record a real night and replay it:

```bash
lull -source i2c -buses abdo_a=4,ref=3 -record nights/demo.jsonl   # capture
lull -source replay -replay nights/demo.jsonl -replay-speed 60     # fall back
```

This is **not** the simulator. Replay plays back accelerometer samples that were
actually measured, through the same detector, at the same rate. The numbers on
screen really happened. A trace with no reference node is refused outright.

---

## Sensor sources

| `-source` | What it reads | Notes |
|---|---|---|
| `sim` | a synthetic pregnancy | breathing, maternal movement, kicks. No hardware. |
| `i2c` | Grove accelerometers on a Pi | one bus per node — identical chips share an address |
| `phone` | phone accelerometers via [phyphox](https://phyphox.org) | 16-bit, 20–50× finer than the Grove part |
| `replay` | a recorded trace | the demo fallback |
| `serial` | Arduino nodes over USB | the original path |

### Why phones are not a fallback

The Grove MMA7660 resolves **0.047 g per count** — 6-bit over ±1.5 g. Fetal
movement at the abdominal wall is roughly 0.05–0.3 g, so the weakest movements
fall *below a single count*. Not faint: unrepresentable.

A phone accelerometer is 16-bit over ±2 g, noise-limited around 0.001–0.003 g.
The code detects the chip and warns when it is on the coarse one.

---

## Repository map

```
cmd/lull            the binary: flags, wiring, lifecycle
internal/
  detect            reference subtraction and the detector
  sensor            every input source + trace record/replay
  maternal          posture, respiration, wake events
  kicker            the phantom: Grove I2C motor driver, sysfs servo
  knob              rotary dial → "she felt it"
  store             SQLite; commands / detections / presses kept separate
  tiger             TigerData hypertables and the nightly baseline
  memory            Backboard narrative memory
  clinical          the Gemini note, implementing RCOG GTG-57
  voice             the escalation phone call (Vapi + 11Labs)
  api               HTTP, SSE, the report, the phone remote
web/static          landing page + dashboard, embedded in the binary
site/               the same landing page, standalone, for Vercel
```

---

## The three tables, and why they are separate

```sql
commands     -- what the phantom was told to do
detections   -- what the detector found
presses      -- what a human said they felt
```

The dashboard count reads **`detections` only**. It never touches `commands`.
The detector is handed accelerometer samples and nothing else — it has no idea a
command was ever issued.

This is the answer to the question every judge asks: *how do I know it isn't just
counting your button presses?* Hand them the pod and let them tap it. Nothing is
written to `commands`. The count still moves.

---

## The clinical note

Generated by Gemini, but it does not invent a rule — it implements
**RCOG Green-top Guideline No. 57**, and the report cites it beside the note.

- **Compare only to this baby's own baseline.** The guideline states there is no
  uniform threshold, and advises awareness of "their baby's individual pattern".
  "Ten kicks in two hours" is not a rule it endorses.
- **Lead with repeat episodes.** Reduced movement on "two or more occasions"
  carries increased risk of stillbirth, growth restriction and preterm birth.
- **Never reassure. Never diagnose.** False reassurance is the documented failure
  mode of home fetal monitoring: it delays women from seeking care. The system is
  only permitted to say *this is different, be seen today*.
- **Do not overstate one quiet night.** Around 70% of single episodes are
  uncomplicated.

The prompt is a string with no compiler behind it, and a well-meaning edit that
drops a line changes what this tells a pregnant woman. Thirteen tests pin the
individual rules, one guards against phrasing that could license reassurance, and
a wire test asserts the rules actually reach the model.

---

## Hardware

Raspberry Pi 5, Grove Base HAT, Grove 3-axis accelerometers, a 9 g micro servo for
the phantom.

**Three things that cost a night**, written down so they cost nobody else one:

1. **A Pi 5 has one bi-colour LED, not two.** Red means *powered but not booting*.
   And after a clean shutdown it sits in standby until you **press the power
   button** — it does not auto-boot like a Pi 4.
2. **`i2cdetect` skips everything below `0x08`.** The Base HAT's own ADC lives at
   `0x04`, so it looks absent unless you scan with `-a`.
3. **All three sockets marked `I2C` are one physical bus.** Two identical chips
   both answer at `0x4C`, both drive the line, and reads return the bitwise AND
   of the two — a resting sensor reporting 0.000 g and 1.447 g on alternate
   samples. One bit-banged bus per sensor fixes it:

   | Grove socket | device | config.txt |
   |---|---|---|
   | any `I2C` | `/dev/i2c-1` | `dtparam=i2c_arm=on` |
   | `D22` | `/dev/i2c-3` | `dtoverlay=i2c-gpio,bus=3,i2c_gpio_scl=22,i2c_gpio_sda=23` |
   | `D24` | `/dev/i2c-4` | `dtoverlay=i2c-gpio,bus=4,i2c_gpio_scl=24,i2c_gpio_sda=25` |

   The pin pair must lie **within one socket** — GPIO pairs that straddle two
   sockets enumerate perfectly and no cable can ever reach them.

See [`docs/HARDWARE.md`](docs/HARDWARE.md) for the full log.

---

## Tests

```bash
go test ./... -race
```

Over 200 tests. They are not decoration — several caught real defects:

- `Close()` guarded its stop channel with `sync.Once` but closed the data channels
  **outside** it, so a second `Close()` panicked. `main` does `defer src.Close()`.
  Three sources had the identical bug.
- `Routes()` registered `http.FileServer(s.Web)` unconditionally, so a server
  built without assets panicked inside `net/http` on the first request.
- The knob detector measured stillness sample-to-sample, but a deliberate turn
  moves ~14 mV between samples at 100 Hz — below the jitter threshold — so a
  smooth sweep read as perfectly still and no gesture was ever noticed.
- Taking the dial's first sample as "rest" made the ADC's startup drift fire two
  phantom marks before anyone touched it, inflating the human count — the one
  number the whole comparison rests on.

---

## Configuration

Everything optional. Copy `.env.example` to `.env` and fill in what you have.

```
BACKBOARD_API_KEY       narrative memory
TIGER_DATABASE_URL      TigerData / Timescale
LLM_BASE_URL/KEY/MODEL  Gemini, via an OpenAI-compatible proxy
VAPI_API_KEY            the escalation call — the PRIVATE key, not the public one
VAPI_PHONE_NUMBER_ID    which of your numbers to call from
VAPI_TO_NUMBER          who to reach
PRESAGE_API_KEY         contactless maternal vitals (SmartSpectra SDK)
```

`.env` is gitignored and should stay that way.

---

## Building for the Pi

```bash
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o lull ./cmd/lull
scp lull lull2@<pi>:~/
```

Everything is cgo-free — pure-Go SQLite, pure-Go Postgres, I2C through a raw
`ioctl` — so it cross-compiles from a Mac with no toolchain and ships as one file.
