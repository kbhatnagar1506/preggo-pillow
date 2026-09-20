# Preggo Pillow

[![CI](https://github.com/kbhatnagar1506/preggo-pillow/actions/workflows/ci.yml/badge.svg)](https://github.com/kbhatnagar1506/preggo-pillow/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/go-1.27-00ADD8?logo=go&logoColor=white)](go.mod)
[![Tests](https://img.shields.io/badge/tests-304%20under%20--race-success)](#tests)
[![License](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

**A pregnancy pillow that counts your baby's movement overnight and tells you when
tonight is different from your baby's own normal.**

> 🏆 **First place** and the **Presage track** at HackHers, Georgia State University, September 2026.

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

**[The one idea that makes it work](#the-one-idea-that-makes-it-work)** ·
[Two repositories](#two-repositories) ·
[Quick start](#quick-start) ·
[The app](#the-app) ·
[What it refuses to do](#what-it-refuses-to-do) ·
[Running the demo](#running-the-demo) ·
[Sensor sources](#sensor-sources) ·
[Repository map](#repository-map) ·
[The tables](#the-tables-and-why-they-are-separate) ·
[Sign-in](#sign-in-and-who-can-read-the-record) ·
[The escalation call](#the-escalation-call) ·
[Maternal vitals](#maternal-vitals) ·
[The clinical note](#the-clinical-note) ·
[Hardware](#hardware) ·
[Tests](#tests) ·
[Configuration](#configuration) ·
[Building for the Pi](#building-for-the-pi) ·
[Status](#status)

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

## Two repositories

This one is **the device**: the Go binary that runs on the Pi, reads the sensors,
counts movement, and serves the pages below. It is a single cross-compiled file
with no runtime dependencies.

The **web app** — the same design system, the same Auth0 tenant, document upload
and the assistant — lives at
[aaditisinghal/Preggo-Pillow](https://github.com/aaditisinghal/Preggo-Pillow).

Where they overlap, this repository defers. The pages here follow that app's
components so a person moving between them does not notice a seam.

---

## Quick start

```bash
go run ./cmd/lull -source sim
```

| URL | What it is |
|---|---|
| `/` | the landing page |
| `/dashboard` | the live monitor: tonight's count against the baseline |
| `/history` | the last 14 nights, night by night |
| `/healthcare` | the record you take to the appointment |
| `/medications` | reminders, stored on the device |
| `/settings` | what this pillow is actually wired to |
| `/report` | the printable clinician's record |
| `/krishnabhatnagar` | the phone remote |

Default port is `8080`. Run with `-addr :3000` if your Auth0 callback points
there — the server compares `APP_BASE_URL` against the port it is listening on
and warns at startup when the two disagree, because an hour disappears into that
mismatch otherwise.

Nothing external is required. `-source sim` runs the entire product against a
simulated pregnancy, with no hardware, no database server and no keys. Every
integration (Auth0, Backboard, TigerData, Gemini, Vapi, Presage) activates only
if its key is present, logs its absence at startup, and is never load-bearing.

---

## The app

Five pages behind one shell. `web/static/app.css` and `web/static/app.js` carry
the navigation; each page ships an identical `<nav>` block and the active link is
decided from `location.pathname`, so a copied sidebar cannot highlight the wrong
page.

**Dashboard** — tonight's movement count, the baseline, the deviation, and the
verdict. Two charts, the vitals grid, and a footer that says plainly it cannot
tell you your baby is fine.

**History** — fourteen nights as bars, then one card per night. A night is
flagged only when the night before it was also low, which is the same rule the
alert uses: fetal sleep cycles run 20–40 minutes and babies have genuinely quiet
nights, so a single-night alarm is noise.

**Healthcare** — the report you take to the appointment, the running record of
what has been written about this pregnancy, and a form to record what a provider
told you. Without that last one the next appointment starts from nothing.

**Medications** — what you take and when, in the same SQLite file as the
detections. A reminder living in one phone's `localStorage` is not a reminder; it
is a note that vanishes when she opens the page on the laptop.

**Settings** — read from the running process, never typed into the HTML: the
sensor source, which capabilities exist, session policy, and every service this
build sends data to. A settings page that claims a capability the binary does not
have is worse than none, because it is the first thing anyone tests.

`TestEveryLinkInTheSidebarResolves` reads the hrefs out of the shipped HTML and
asks the router for each one. A menu item cannot point at a route that does not
exist.

---

## What it refuses to do

Each of these is pinned by a test, because the pressure to add them is real and
arrives late at night.

**No fetal heart rate.** The tile exists on the dashboard, badged `no sensor`,
showing an em dash and the reason. Fetal heart rate needs Doppler ultrasound. No
accelerometer and no camera reads it through the abdominal wall, and a plausible
number in that box would be a fabricated vital sign on a pregnancy monitor.

**No reassurance.** The system is never permitted to say the baby is fine. False
reassurance is the documented failure mode of home fetal monitoring: it delays
women from seeking care. It may only say *this is different, be seen today*.

**No diagnosis.** Assessment of reduced fetal movement is a CTG and a scan, and
belongs to a clinician.

**No health details in the escalation call.** The call says a movement pattern
changed and asks someone to check on her. It never names a medication, a
diagnosis or a result — a phone call can be overheard, and whoever answers may
not be who she would have chosen to tell.

**Deleting a medication keeps the doses already recorded.** Removing the plan must
never quietly improve the adherence history.

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
  vitals            contactless maternal pulse and breathing
  kicker            the phantom: Grove I2C motor driver, sysfs servo
  knob              rotary dial → "she felt it"
  store             SQLite; what was commanded, detected and felt kept apart
  tiger             TigerData hypertables and the nightly baseline
  memory            Backboard narrative memory
  clinical          the Gemini note, implementing RCOG GTG-57
  voice             the escalation phone call (Vapi + ElevenLabs)
  auth              Auth0 OIDC + our own sessions, cookies, crypto
  brand             the one name the product is called in front of a person
  api               HTTP, SSE, the pages, the report, the phone remote
web/static          landing page + the five app pages, embedded in the binary
site/               the same landing page, standalone, for Vercel
tools/presage-bridge  runs the Presage SDK and POSTs /api/vitals
arduino/            the original serial sensor sketch
docs/HARDWARE.md    the full hardware log
docs/SENSORS-AND-METHODS.md  every sensor, algorithm and source, and what is absent
CONTRIBUTING.md     how to run it, and the one rule
SECURITY.md         the threat model, and how to run it safely
```

`internal/brand` holds one constant, and it exists because of a real bug. The
codebase is called `lull`; the product is **Preggo Pillow**. The two drifted, and
they drifted in exactly the three places a stranger meets first: the record a
midwife reads, the page on the phone, and the voice on the emergency call, which
introduced itself as "an automated alert from Lull". The landing page said
Preggo Pillow, so anyone comparing the two saw two products. A test now fails if
any user-facing surface drifts again.

---

## The tables, and why they are separate

```sql
commands     -- what the phantom was told to do
detections   -- what the detector found
presses      -- what a human said they felt
nights       -- one row per night: the count that became the baseline
medications  -- what she is meant to take, and when
doses        -- what she actually took
```

The dashboard count reads **`detections` only**. It never touches `commands`.
The detector is handed accelerometer samples and nothing else — it has no idea a
command was ever issued.

This is the answer to the question every judge asks: *how do I know it isn't just
counting your button presses?* Hand them the pod and let them tap it. Nothing is
written to `commands`. The count still moves.

`medications` and `doses` are split for the same reason. What was *prescribed*
and what was *swallowed* are different facts, and an adherence figure only means
something when the second is recorded independently of the first. Changing a
schedule must not silently rewrite the history of last Tuesday.

Doses are materialised lazily, for a day that has already arrived — there is no
scheduler. A pillow that has been unplugged for a week must not wake up and claim
she missed twenty-one doses it never asked her about.

---

## Sign-in, and who can read the record

Identity comes from **Auth0** over OpenID Connect: Authorization Code with PKCE
(S256), `state` and `nonce`, an encrypted single-use transaction cookie, and full
ID-token validation with `alg` pinned to RS256.

The app then keeps **its own session**, because idle logout and revocation should
not wait for someone else's token to expire. The session token is opaque; the
cookie is `<token>.<HMAC>`; the server stores only a SHA-256 hash, so a stolen
database row cannot be replayed as a login. Fifteen minutes idle, twelve hours
absolute.

With no Auth0 config, every page is open — which is what a bench demo wants and
what you must not ship. `/settings` says so in as many words when it detects it.

**Gating the pages is not enough, and getting this wrong is easy.** For a while
`/dashboard` redirected anonymous visitors while `/report` served the entire
clinical summary and `/api/memories` the whole written narrative — alerts
included — to anyone who could reach the port. The pages were never really gated.

So the split now runs by what a route *touches*, not by whether it renders HTML:

| Open, deliberately | Requires a session |
|---|---|
| `/api/stream`, `/api/kick`, `/api/press`, `/api/fool`, `/api/call` | `/report` |
| `/api/vitals` (the Presage bridge posts from another process) | `/api/nights`, `/api/maternal`, `/api/memories` |
| `/api/blind/*` (the demo controls) | `/api/meds`, `/api/dose`, `/api/settings` |
| the phone remote itself | `/api/note`, `/api/profile`, `/api/appointment`, `/api/ask` |

The left column is what the phone remote needs, and the remote has no session by
design — it is a bedside device for one pregnancy, and the path is the identity.
Nothing in that column reads or writes the record.

API refusals are **401 JSON, not a redirect**. A 302 to the login page arrives at
a `fetch()` as a chunk of HTML and dies inside `JSON.parse`, which makes an
expired session look like a syntax error.

There is also a demo session (`/auth/demo`) that skips sign-in entirely. It
creates a distinct throwaway account each time, so two people demoing at once do
not share one, and every page it renders is marked **"Demo account. Fictional
data."** — fictional data must never pass as real on a product like this.

---

## The escalation call

One tap on the phone remote places a real phone call, through **Vapi** with an
ElevenLabs voice, to the emergency contact.

The script is built from the same numbers on screen — a call that quotes a
different figure from the dashboard destroys trust in both. It states the
deviation, how many consecutive nights, and asks the person to take her to be
seen today.

Two things learned the hard way:

- `VAPI_API_KEY` must be the **private** key. The public key is for browser SDKs
  and returns 401 here; the error message says so explicitly, because that costs
  an hour otherwise.
- The button used to arm on the first tap and fire on the second, so a stray
  touch could not dial. On an iPhone that failed twice over: iOS holds every tap
  for ~300 ms to test for double-tap-to-zoom and **swallowed the second click**,
  and a button you have to hit twice fails in front of an audience anyway. It is
  one tap now, with `touch-action: manipulation`, and a re-entrancy lock so a
  jittery thumb cannot place two calls.

---

## Maternal vitals

The control arm of the whole argument. The headline claim is *the baby moved
less* — which is only interesting if *she* did not change.

**Presage SmartSpectra** (FDA 510(k) K254169, RMSE 1.32 bpm pulse / 1.75 brpm
breathing) reads her pulse and breathing contactlessly from a camera. It is
SDK-only, so `tools/presage-bridge` runs the SDK and POSTs to `/api/vitals`.

rPPG reads surface blood flow from a face. It measures **the mother**. It cannot
and does not measure the fetus, and the code never mixes the two sources in one
summary.

The pillow itself derives posture, respiration, wake events and snore burden from
the accelerometers. A respiration rate the tracker cannot stand behind is
reported as *not measured* rather than as a number — a borderline estimate
narrated as fact is worse than a gap, on a page a clinician reads.

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

**304 tests, all passing under `-race`.** They are not decoration. Every item
below is a real defect a test found, most of them invisible from the happy path:

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
- `handlePress` dereferenced a nil `Store` and `handleKick` a nil `Kicker`. Both
  sit on routes the phone remote needs open, so on a build with no local database
  — `-source phone` is exactly that — an **anonymous** request panicked the
  handler. `TestNoHandlerPanicsOnABareServer` now walks every route on a server
  with nothing wired at all.
- A biquad's startup transient was becoming the normalisation divisor in the
  acoustic path, which manufactured three convincing "heartbeat detections" out
  of nothing. Trusting them would have been the worst outcome in this repository.
- The idle-timeout calculation truncated to whole seconds, so a sub-second idle
  window rounded to zero and invalidated every session the instant it was made.

A note on what tests here are *for*. Several pin behaviour that is a product
decision rather than a correctness property: that the fetal heart-rate disclaimer
cannot be deleted, that the clinical prompt still forbids reassurance, that no
user-facing surface says anything but "Preggo Pillow", that removing a medication
keeps its recorded doses. Those are the ones most likely to be broken by a
well-meaning edit at hour thirty.

---

## Configuration

Everything is optional. Copy `.env.example` to `.env` and fill in what you have;
each missing key disables one capability, logs why at startup, and never stops
the server.

```
# sign-in — unset leaves every page open, which is a bench demo, not a product
AUTH0_DOMAIN            your tenant
AUTH0_CLIENT_ID
AUTH0_CLIENT_SECRET
APP_BASE_URL            must match the port you listen on, or the callback 404s
SESSION_SECRET          openssl rand -base64 32
DATA_ENCRYPTION_KEY     openssl rand -base64 32 — exactly 32 bytes decoded

# the escalation call
VAPI_API_KEY            the PRIVATE key, not the public one
VAPI_PHONE_NUMBER_ID    which of your numbers to call from
VAPI_TO_NUMBER          who to reach
VAPI_VOICE_ID           ElevenLabs voice

# the long record and the narrative
TIGER_DATABASE_URL      TigerData / Timescale
BACKBOARD_API_KEY       narrative memory
BACKBOARD_ASSISTANT_ID  pin it once you have a record worth keeping

# the written note
LLM_BASE_URL/KEY/MODEL  Gemini, via an OpenAI-compatible proxy
```

**Pin `BACKBOARD_ASSISTANT_ID`.** The assistant is otherwise found *by name* at
startup, so renaming the product creates a second, empty one and silently
orphans everything written so far.

`.env` is gitignored and should stay that way. `.env.example` is not — it was
caught by the same `.env.*` rule for a while, which made the one file a new
contributor needs the one file missing from the repository.

---

## Building for the Pi

```bash
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o lull ./cmd/lull
scp lull lull2@<pi>:~/
```

Everything is cgo-free — pure-Go SQLite, pure-Go Postgres, I2C through a raw
`ioctl` — so it cross-compiles from a Mac with no toolchain and ships as one file.

```bash
make deploy PI=pi@raspberrypi.local
```

`site/` holds the landing page as static files with a `vercel.json`, so the
marketing page can go up without the device binary behind it.

---

## Status

Working, verified live rather than only in tests: reference subtraction on real
hardware (0.335 g residual; five deliberate maternal movements → zero false
detections), the blind test (machine 3/3, human 1/3, no false positives), the
Auth0 round trip, a real phone call placed and answered, the Presage bridge
authenticated, and the medication and vitals round trips.

Abandoned honestly: **heartbeat detection from the contact microphone.** The
literature is clear about the method — bandpass 40–500 Hz, average Shannon energy
`E = −x²·log(x²)`, Hann windowing — and it was implemented. The Grove microphone
still cannot resolve a foetal heart tone through an abdominal wall. Three
convincing "detections" turned out to be a filter transient, a moving-average
lobe, and mains hum at a band edge. The limit is the sensor, not the algorithm,
and the right thing to do with that is write it down rather than ship it.

Not done: no real overnight trace has been recorded yet, so the replay fallback
is still hypothetical rather than proven.

---

## A note on what this is

This is a hackathon project. It is not a medical device, it has not been
validated on a single real pregnancy, and nothing it produces is advice.

What it does do is produce a record that did not exist before — so that
*"he's been quieter lately"* stops being something a woman has to be believed
about, and becomes something she can put on a table.
