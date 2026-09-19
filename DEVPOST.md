# Preggo Pillow — Devpost copy

Paste each block into the matching field.

---

## Inspiration

The advice a pregnant woman gets is to count kicks. The guideline her midwife actually follows says something else entirely.

RCOG Green-top Guideline No. 57: "There is no uniform threshold of fetal movements above which perinatal morbidity increases." Ten in two hours is not a rule it endorses. What matters is a change from *her* baby's own pattern, and a change that happens on more than one occasion, because around 70% of single quiet episodes turn out to be nothing.

So the real task is this. Notice a gradual decline, against a baseline nobody ever gave you, while half asleep, at three in the morning. Then convince a stranger in a clinic that you noticed it.

That second half is what we kept coming back to. "He's been quieter lately" is not a measurement. It is something you have to be believed about.

## What it does

Preggo Pillow is a pregnancy pillow with three accelerometers in it. You sleep on it. In the morning there is a number, and that number is compared to the median of your own previous nights, not to anyone else's baby.

When tonight drops 25% or more below that baseline on two consecutive nights, it raises an alert, generates a one page record you can hand to a midwife, and can place a real phone call to an emergency contact telling them to take you to be seen today.

It also runs a blind test live, which is the part we actually care about. A servo behind the foam fires a known kick. The person holding the pillow presses a button when they feel one. The accelerometers decide separately. Then all three numbers go on screen together. At our table it reads: pillow 3 of 3, person 1 of 3, zero false positives. That gap is the entire clinical premise, demonstrated instead of asserted.

## The sensors, and the one idea that makes it work

A single accelerometer on the abdomen cannot tell a kick from the mother rolling over. Both are just acceleration. This is why "put a phone on your belly" does not work.

So there are three. Two on the abdomen, one on her back.

```
       abdo_a ─┐
       abdo_b ─┼─→  residual = abdominal − reference  →  detector
       ref    ─┘
```

Anything *she* does, breathing, turning, getting up, reaches all three at once. Subtract the reference node and what is left happened at the abdomen only. On the bench that gave us 29 of 30 movements detected with zero false positives across 20 deliberate maternal movements. Running live it sat at 0.335 g residual with five maternal movements producing zero detections.

Two implementation details were not obvious and cost real hours.

High-pass each axis independently, and only then take the magnitude. We did it the other way round first, which hides any motion perpendicular to gravity. A 0.3 g kick was reading as 0.044 g. The detector sat stuck at 33% and looked like a threshold problem for most of a night.

Average only the abdominal nodes that are actually reporting, and refuse to emit anything at all without a reference node. A missing reference is not a degraded mode. It is a mode that silently counts the mother as the baby, which is worse than returning nothing.

## Hardware

Raspberry Pi 5, Grove Base HAT, three Grove 3-axis accelerometers (MMA7660), a 9 g micro servo driving a card paddle behind foam as the phantom, and a Grove rotary angle sensor as the "I felt that" dial so the human tally is a physical thing you turn in the dark rather than a button you hunt for.

Three hardware facts cost us a night each, so here they are for whoever reads this next.

A Pi 5 has one bi-colour LED, not two. Red means powered but not booting. And after a clean shutdown it sits in standby until you physically press the power button. It does not auto-boot like a Pi 4. We diagnosed a Pi 5 as a Pi 4 for hours.

`i2cdetect` skips every address below `0x08` by default. The Base HAT's own ADC lives at `0x04`, so it looks completely dead until you scan with `-a`. We declared it broken. It was fine.

All three sockets stencilled `I2C` on that HAT are one physical bus. Two identical accelerometers both answer at `0x4C`, both drive the line, and reads come back as the bitwise AND of the two chips. A sensor sitting perfectly still reported 0.000 g and 1.447 g on alternate samples. The fix is one bit-banged bus per sensor:

| Grove socket | device | config.txt |
|---|---|---|
| any `I2C` | `/dev/i2c-1` | `dtparam=i2c_arm=on` |
| `D22` | `/dev/i2c-3` | `dtoverlay=i2c-gpio,bus=3,i2c_gpio_scl=22,i2c_gpio_sda=23` |
| `D24` | `/dev/i2c-4` | `dtoverlay=i2c-gpio,bus=4,i2c_gpio_scl=24,i2c_gpio_sda=25` |

The GPIO pair has to lie inside one socket. Our first attempt straddled two, enumerated perfectly, and no cable could physically reach it.

## How we built it

One Go binary, no cgo, running at 100 Hz. Pure-Go SQLite, pure-Go Postgres, I2C through a raw `ioctl`, which means it cross-compiles from a MacBook to the Pi with no toolchain and ships as a single file you scp across. The same binary runs in a distroless container in the cloud.

On the device, three tables stay deliberately separate. `commands` is what the servo was told to do. `detections` is what the detector found. `presses` is what a human said they felt. The count on screen reads detections only and never touches commands, and the detector is handed accelerometer samples with no knowledge that a command was ever issued. That separation is the answer to the question every judge asks, which is how do we know it isn't just counting your button presses. Hand them the pod and let them tap it with a finger. Nothing writes to `commands`. The count still moves.

TigerData holds the long record. The nightly baseline is a Timescale continuous aggregate rather than a job we run, so it is always current. Its buckets run noon to noon, not midnight to midnight, because sleep crosses midnight and a calendar-day bucket splits one night into two rows and halves both, turning a normal night into two apparently reduced ones and firing a false alert at a pregnant woman.

Gemini on Vertex writes the clinician's note, but it does not invent a rule. Thirteen tests pin the individual clauses of GTG-57 and one guards against phrasing that could license reassurance. Backboard holds the narrative memory across nights and appointments. Presage SmartSpectra reads the mother's pulse and breathing contactlessly from a camera, which is the control arm of the whole argument: her numbers are normal *and* the baby moved 41% less. Vapi with an ElevenLabs voice places the call. Auth0 handles identity over OIDC with PKCE, and the app keeps its own sessions so idle logout does not wait on somebody else's token.

304 tests, all under `-race`.

## Challenges we ran into

The honest one is the heartbeat.

We spent hours trying to pull a foetal heart tone off a contact microphone. We did it properly, following the phonocardiography literature: bandpass 40 to 500 Hz, average Shannon energy `E = −x²·log(x²)`, Hann windowing, margin off the band edges. And three times we got a clean, convincing, periodic detection.

All three were artefacts. The first was a biquad's startup transient becoming the normalisation divisor. The second was a lobe of our own moving average. The third was mains hum sitting right at a band edge. Each one looked like a heartbeat and was our own code looking at itself.

The limit is the sensor, not the algorithm. A Grove microphone cannot resolve a foetal heart tone through an abdominal wall. So we cut it, and the dashboard now carries a "Baby heart rate" tile that shows an em dash, a badge reading `no sensor`, and a sentence saying fetal heart rate needs Doppler ultrasound. A test stops anyone deleting that disclaimer.

Three false positives that we caught ourselves is the reason we trust the 29 of 30 that we did not.

Also: SQLite's default busy timeout is zero. On a cold database our first night's seeding raced the auth store's migration, the loser got `SQLITE_BUSY`, and the loser happened to be authentication, which then disabled itself and served the whole medical record to anyone who could reach the port with a single log line as the warning. It now refuses to start instead.

## Accomplishments we're proud of

That we are the team that says what it cannot measure.

There is no fetal heart rate, and the tile says so rather than showing a plausible number. There is never reassurance, because false comfort is the documented failure mode of home fetal monitoring: it delays women from seeking care. The escalation call never names a medication or a diagnosis, because a phone call can be overheard and whoever picks up may not be who she would have chosen to tell. Deleting a medication keeps the doses already recorded, so removing the plan cannot quietly improve the history.

Every one of those is pinned by a test, because those are exactly what a well-meaning edit at hour thirty breaks.

## What we learned

That the hard part of a sensor product is proving the sensor did it. Everything upstream of the blind test is a claim. The blind test is evidence, and it took longer to build than the detector did.

And that a device like this is defined by its refusals more than its features. Anyone can print a number. Deciding which numbers you are not entitled to print is the actual engineering.

## What's next

Recording a real overnight trace so the replay fallback stops being hypothetical. Validation against actual CTG data, which is the only thing that would make any of this clinically meaningful. And a respiration estimator we trust at the low end, because right now a borderline reading is reported as "not measured" rather than as a number, and a gap on a clinician's page is better than a wrong vital but it is still a gap.

## Try it

Landing page: https://preggo-pillow.vercel.app
Live app: https://35.232.223.194.nip.io
Code: https://github.com/kbhatnagar1506/preggo-pillow
Web app: https://github.com/aaditisinghal/Preggo-Pillow

## Built with

go, raspberry-pi, i2c, accelerometer, sqlite, timescaledb, tigerdata, google-cloud, vertex-ai, gemini, backboard, vapi, elevenlabs, presage-smartspectra, auth0, next.js, vercel, server-sent-events, docker
