# Running the demo

Five minutes, three moments. Each one sets up the next.

## Before you start

```bash
go run ./cmd/lull -source sim -owner krishnabhatnagar
```

Find your laptop's address so the phone can reach it:

```bash
ipconfig getifaddr en0
```

| Screen | URL |
|---|---|
| Laptop, facing the judges | `http://localhost:8080/dashboard` |
| Your phone | `http://<that-address>:8080/krishnabhatnagar` |

Check before you walk up: tap one kick on the phone and watch the laptop count
move. If it doesn't, you are on different networks.

---

## 0:00 – 0:45 · The problem

Do not open with statistics. Open with the trap.

> "When a baby's movements drop, that's the warning sign. So women are told to
> count kicks. But the guideline itself says there is no number — no threshold
> that means anything, because every baby has its own pattern. So she's being
> asked to notice a change against a baseline nobody ever gave her, at 3am, half
> asleep."

---

## 0:45 – 1:30 · The object

Hold up the pod. It goes on at night, comes off in the morning, there's no app to
open. Two sensors at the abdomen, one against her back.

---

## 1:30 – 3:00 · Hand them the phone

**"Tap it. Whenever you like, don't tell me when."**
→ the count moves on the laptop.

**"Now hold both sensors and shake them as hard as you want."**
→ *nothing happens.*

Then explain why, and this is the whole pitch in two sentences:

> "The back sensor sees everything she does. Subtract it, and what's left
> happened at the abdomen only. That's the difference between a movement counter
> and a fetal movement counter."

**This is the moment that wins it.** Every smart device demo counts motion. Yours
has a judge shaking the hardware as hard as they like while the counter sits
still. Fifteen seconds, and no slide can do it.

---

## 3:00 – 4:00 · The gap

Give them the dial — or the **I felt that** button on the phone.

> "Turn it every time you feel a movement."

Run a minute. Then show the three numbers:

```
fired      12
you felt    4
Lull found 11
```

> "You were awake, in a quiet room, paying attention, with one job. She's asleep."

If they ask whether you're just counting your own button presses: `commands`,
`detections` and `presses` are three separate tables. The dashboard reads
`detections` only. The detector is handed accelerometer samples and nothing else.
**Hand them the pod and let them tap it** — nothing is written to `commands` and
the count still moves.

---

## 4:00 – 4:40 · The report

Open `/report`. Fourteen nights of baseline, last night down 41%.

Point at the citation under the note:

> "It doesn't invent a rule. It implements RCOG Green-top Guideline 57. And it
> never tells her the baby is fine — it can't clear anyone. Reassurance is the
> documented way home monitoring causes harm, so the system is only allowed to
> say *this is different, go in.*"

If you have the phone call wired, arm it and let them hear it ring.

---

## 4:40 – 5:00 · Close

> "We can't tell her the baby is fine. Nothing can. We can tell her that tonight
> was different from her baby's own normal — and that's the sentence that gets
> her seen in time."

---

## When it breaks

It has broken four times in development. Have this ready.

**The hardware dies mid-demo** — switch to a recorded night. Same dashboard, same
detector, real measured samples:

```bash
lull -source replay -replay nights/demo.jsonl -replay-speed 60
```

Say what it is. *"That's a recording of a real night — the hardware dropped out,
so this is the same detector running the same samples."* Judges forgive that.
They do not forgive a blank screen, and they especially do not forgive being told
something is live when it isn't.

**The Pi won't boot** — it's a Pi 5. After a clean shutdown it sits in standby
until you **press the power button**. It does not auto-boot like a Pi 4.

**Sensors vanished from the bus** — scan the whole range, not the default:
`sudo i2cdetect -y -a 1`. Everything below `0x08` is skipped otherwise, and the
HAT's own ADC lives at `0x04`.

**Nothing on the phone** — you're on a different network from the laptop. Tether
one to the other.

---

## The questions you will get

**"How do you know it's not just counting your taps?"**
Three separate tables; the count reads detections only. Hand them the pod.

**"How accurate is it?"**
29 of 30 on a mechanical phantom, zero false positives across 20 deliberate
maternal movements. Say it's bench data, not a clinical trial — because it is.

**"Why not measure the heartbeat? That's what they do in hospital."**
Home fetal dopplers are actively discouraged: a woman hears *a* heartbeat, feels
reassured, and delays going in. Sometimes it's her own. Movement is the signal
women are actually told to act on. Measuring it rather than heart rate is the
clinically correct choice, not a limitation.

**"Is this a medical device?"**
No, and it doesn't claim to be. It's a wellness monitor that says one thing:
tonight differs from your baseline, be seen. Assessment is a CTG and a scan, and
that belongs to a clinician.
