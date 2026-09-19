# Lull — hardware state, and the traps that cost a night

Written 2026-09-18, after a long debugging session. Read this before touching
the Pi again.

## The five things that actually wasted the night

1. **It is a Raspberry Pi 5, not a Pi 4.** Everything was diagnosed as a Pi 4
   for hours. A Pi 4 has two LEDs, with red as a voltage monitor that switches
   off below 4.63V. A **Pi 5 has one bi-colour LED**: red = powered but not
   booting, green = booting or running. "No red light" on a Pi 5 means nothing
   is wrong.

2. **A Pi 5 does not auto-boot after a clean shutdown.** It sits in standby
   showing red until you **press the physical power button** on the corner of
   the board. This was the entire fault. The board was power-cycled a dozen
   times and the button was never pressed.

3. **Power was never the problem.** `vcgencmd get_throttled` returns `0x0` —
   no undervoltage or throttle event has ever been recorded. The brownout
   theory was wrong.

4. **The SSH username is `lull2`, not `lull`.** The hostname went into the
   username field in Raspberry Pi Imager. A wrong username produces a password
   prompt that always fails, which looks exactly like a wrong password.

5. **`i2cdetect` hangs on the bit-banged buses.** Scanning `/dev/i2c-3` or
   `/dev/i2c-4` with nothing attached times out on all 128 addresses. Scan
   `/dev/i2c-1` only, or wrap it in `timeout`.

## Current state — all of this persists across reboots

| | |
|---|---|
| Board | Raspberry Pi 5 Model B Rev 1.1, 2GB, rev `b04171` |
| OS | Raspberry Pi OS Lite Trixie, kernel 6.18.50, arm64 |
| Boots from | USB, `/dev/sda`, 114.6 GB — **no microSD inserted** |
| Hostname / user | `lull2` / `lull2` |
| Reached at | `192.168.3.2` over ethernet, ~0.76ms |
| SSH | key installed, passwordless sudo via `/etc/sudoers.d/010-lull2` |
| Health | `throttled=0x0`, 48.8°C, reboots in 30s |

### config.txt additions

```
dtparam=i2c_arm=on
dtoverlay=i2c-gpio,bus=3,i2c_gpio_sda=23,i2c_gpio_scl=24
dtoverlay=i2c-gpio,bus=4,i2c_gpio_sda=27,i2c_gpio_scl=22
dtoverlay=pwm-2chan,pin=12,func=4,pin2=13,func2=4
usb_max_current_enable=1
```

Three I2C buses because three identical accelerometers all answer on the same
address and the Grove HAT wires its three I2C sockets to one physical bus.

### Scan the WHOLE bus, or you will miss things

`i2cdetect` and `i2cget` skip everything below `0x08` by default. The Grove
Base HAT's ADC lives at `0x04`, so it looked absent for hours. Use
`i2cdetect -y -a 1`, and bind to it with `I2C_SLAVE_FORCE` (`0x0706`) rather
than `I2C_SLAVE`.

### What is on the I2C bus (`/dev/i2c-1`)

| addr | device |
|---|---|
| `0x0F` | Grove I2C Motor Driver — responds, but it is an H-bridge for DC motors and **the actuator turned out to be a servo**, so it is unused |
| `0x3E` | unidentified; ACKs its address, refuses register reads |
| `0x4C` | MMA7660FC accelerometer — live, reads 1.098g at rest |
| `0x04` | Grove Base HAT ADC — **working**, device id `0x0004`, firmware v2. An earlier note here claimed it was absent on a Pi 5; that was wrong, it was simply never being scanned. |

## The measurement that decided the architecture

The MMA7660 is 6-bit over ±1.5g: **0.047g per count**, with ±1 count of
observed noise. Fetal movement at the abdominal wall is roughly 0.05–0.3g, so
the weakest movements fall **below one count**. The sensor is not noisy about
them — it cannot represent them at all.

A phone accelerometer is 16-bit over ±2g, noise-limited around 0.001–0.003g:
**20–50× finer**.

So: **phones do the sensing, the Pi drives the phantom.** That is an instrument
choice a measurement pointed at, not a workaround.

## Blocked, and on what

The phantom's actuator is a **9g micro servo (SG90 class)**, wires
white=signal, red=+5V, black=ground, ending in a 3-pin **female** connector.

PWM is configured on **GPIO12**, which is the **`PWM` socket on the Grove Base
HAT**, so the HAT does not have to come off.

**Missing: one cable.** Either a Grove-to-jumper conversion cable (Grove plug
one end, four loose male pins the other) or three male-to-female jumper wires.
Male-to-male jumpers cannot work: both ends are male and so are the GPIO pins.

Wiring once the cable exists:

| Grove wire | servo wire |
|---|---|
| yellow (GPIO12, signal) | white |
| red (5V) | red |
| black (ground) | black |
| white (GPIO13) | unused |

## Gotchas for next session

- **Press the power button** after any full power-off.
- `192.168.3.2` only exists while **macOS Internet Sharing** is on, sharing
  Wi-Fi to the USB LAN adapter. Without it there is no DHCP on that cable.
- The Pi reaches the network through a **USB ethernet adapter** (`eth1`,
  MAC `00:e0:7c:…`), not its built-in port. `eth0` shows `unavailable`.
- Wi-Fi is present but **disconnected** — worth configuring as a second path so
  a knocked cable cannot kill a demo.
- Rotate the Backboard key and Tiger password; both were pasted into a chat
  transcript. The Pi's password too.

## Software state

133 tests passing under `-race`; arm64 cross-compile clean. Binary deployed to
`/home/lull2/lull`.

New since the hardware work started:

- `internal/sensor/phone.go` — phyphox HTTP source. **m/s² not g**; iOS serves
  on port 80 and Android on 8080; the `<t>|acc_time` threshold query is what
  makes polling incremental, and without it the kick count climbs on its own.
- `internal/kicker/grove.go` — Grove I2C motor driver, three-byte commands.
  Unused now, but correct and tested.
- `internal/kicker/pwm.go` — sysfs servo driver. Sysfs root is injectable, so
  it is fully tested against a temp directory with no Pi and no servo.
- `cmd/lull/source_linux.go` — `-source i2c`, one bus per accelerometer,
  refuses to run without a reference node.


## The rotary dial — working, and it earns its place

The Grove rotary angle sensor reads on **channel A3** (365–3070 mV of travel),
even though it is in the socket marked `A2`. **The socket label does not match
the channel.** `sensor.FindSwingingChannel` locates it by asking which input
moves, so nobody has to remember that.

Its role is the human half of the blind test: one notch per movement she
noticed, written to `presses` — the same table the on-screen button uses. The
demo is the three-way tally:

```
Phantom fired      12
She felt            4     <- the dial
Lull detected      11
```

That is the clinical premise demonstrated rather than asserted: people
under-perceive fetal movement, which is why "count the kicks yourself" fails as
advice.

Run it with `-knob` (add `-knob-channel 3`, or leave it off and turn the dial
when asked).

### Two bugs worth remembering

**Stillness measured sample-to-sample missed every gesture.** A deliberate turn
moves only ~14 mV between samples at 100 Hz, below the jitter threshold, so a
smooth sweep read as perfectly still. Stillness is now measured against the last
*significantly different* reading.

**The first sample must not be taken as "rest".** Doing so made the ADC's own
startup drift fire two marks before the dial was touched. A phantom mark
inflates the human count — the one number the comparison rests on. The detector
now waits for the dial to hold still once before counting anything.

## Not working, and not worth fixing

The Grove LCD. Its text controller answers at `0x3E` but nothing appears, and
no backlight controller responds at any of `0x62`, `0x6B`, `0x30`, `0x2D`,
`0x60`, `0x63`. Dead backlight chip or a supply-rail problem; neither is
fixable in software, and it is a decoration.


## Multiple accelerometers — the socket-to-bus map

**All three Grove sockets marked `I2C` are the same physical wire.** Two
identical chips plugged into any of them both answer at `0x4C`, both drive the
bus, and reads return the bitwise AND of the two answers. Measured: a resting
sensor reported |a| of 0.000, 0.105, 0.281, 1.447 g across twelve reads. A
sensor at rest must read 1 g, so that output is not noise — it is two chips
fighting.

The fix is one independent bus per sensor, bit-banged onto GPIO pairs that
match real Grove sockets:

| Grove socket | device node | config.txt |
|---|---|---|
| any `I2C` | `/dev/i2c-1` | `dtparam=i2c_arm=on` |
| `D22` | `/dev/i2c-3` | `dtoverlay=i2c-gpio,bus=3,i2c_gpio_scl=22,i2c_gpio_sda=23` |
| `D24` | `/dev/i2c-4` | `dtoverlay=i2c-gpio,bus=4,i2c_gpio_scl=24,i2c_gpio_sda=25` |

**The pin pairs must lie within ONE socket.** The first attempt used
`sda=23,scl=24` and `sda=27,scl=22`, which straddle two sockets each — no
single Grove cable can reach them, so they were unusable in practice even
though they enumerated fine. Grove pin 1 is SCL and pin 2 is SDA, and the
sockets carry consecutive GPIOs: `D22`→22,23; `D24`→24,25; `D26`→26,27.
`D18` is taken by the servo PWM.

Verified: two MMA7660s, one per bus, both reading clean gravity 12/12 in
different orientations (#1 gravity on +Z, #2 on −X).

## Confirmed working end to end on real sensors

```
lull -source i2c -buses abdo_a=1,ref=3 -hz 60
```

Both chips autodetected, the Branch B warning raised itself, the motor driver
was found at `0x0F`, the acoustic gate disabled itself with no mic present, and
the detector produced real detections from live data with reference subtraction
across two buses: residuals of 0.112 g and 0.335 g against a 0.110 g threshold.

Still true, and still the reason to prefer phones for sensing: the MMA7660
resolves 0.047 g per count, and the weakest fetal movements fall below one
count. Finger taps clear it easily; a real fetal movement at the abdominal wall
may not.

## The contact microphone does not work through this ADC

Measured on channel A4 with matched 30-sample windows:

| | median | p90 | max |
|---|---|---|---|
| silence | 582 | 720 | 1020 mV |
| tapping | 620 | 767 | 1419 mV |
| ratio | ×1.07 | ×1.07 | ×1.39 |

A 7% separation between silence and tapping is not a detector. The sensor is
connected and alive (1103 mV of activity against 41 mV on a floating channel) —
the problem is that a plain Grove sound sensor outputs **raw audio**, and
reading it over I2C gives roughly 700 samples/sec, so the waveform aliases into
constant-amplitude hash regardless of sound.

No algorithm recovers this; the information is destroyed at sampling time.
What would work is a **Grove Loudness Sensor**, which rectifies and low-passes
in hardware and outputs a slow DC level. Or a phone microphone, which phyphox
exposes already envelope-detected.

**Methodological note:** the first attempt compared a 6-second max−min against
30-sample windows and declared a false negative. A longer window always catches
wider extremes. Compare like with like.

## The clinical note is grounded in a published guideline

The Gemini prompt implements **RCOG Green-top Guideline No. 57 (Reduced Fetal
Movements)** rather than rules of our own, and the report cites it beside the
note. The guideline supports the design directly: there is "no uniform
threshold of fetal movements above which perinatal morbidity increases", and
women should "be aware of their baby's individual pattern of movements" — which
is exactly what the per-baby Timescale baseline computes. Reduced movement on
"two or more occasions" carries elevated risk, so consecutive nights lead the
note; and around 70% of single episodes are uncomplicated, so one quiet night
prompts contact without alarm.

Thirteen tests pin the individual rules, one guards against phrasing that could
license reassurance, and a wire test asserts the rules actually reach the model.
