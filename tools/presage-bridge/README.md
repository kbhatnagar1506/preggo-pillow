# Presage bridge

Runs the **Presage SmartSpectra** SDK against a camera and posts the mother's
pulse and breathing rate to Lull.

## Why a bridge exists

SmartSpectra is SDK-only — iOS, Android, C++, Node/Electron. There is **no REST
endpoint** that accepts video and returns vitals, so an API key alone is not
enough: something has to run the SDK against a real camera. This is the smallest
thing that does.

## What it measures

**The mother, not the baby.** SmartSpectra uses rPPG — it reads micro-changes in
skin colour caused by surface blood flow, from a face. There is no optical path
to a fetus through the abdominal wall, and no camera-based method can measure
fetal heart rate. Anyone who knows the field will ask, so be straight about it.

Maternal is the useful role anyway. Lull's claim is *"every maternal number is
normal, and the baby still moved 41% less"* — and that only carries weight if the
maternal numbers are trustworthy. Derived from an accelerometer they need a
±20% caveat. Measured here they are FDA 510(k) cleared (K254169) at **RMSE
1.32 bpm** pulse and **1.75 brpm** breathing.

## Running it

```bash
cd tools/presage-bridge
npm install
PRESAGE_API_KEY=... node bridge.mjs --lull http://localhost:8080
```

| flag | default | meaning |
|---|---|---|
| `--lull` | `http://localhost:8080` | where Lull is listening |
| `--interval` | `5` | seconds between posts |
| `--camera` | `0` | camera device index |

macOS will ask for camera permission the first time. Sit facing the camera in
even light; it needs a few seconds to lock on, and it reports why when it can't
(`kNoFaceFound`, `kTooDark`, `kExcessiveMotion` and so on) rather than just
sitting silent.

## What Lull does with it

`POST /api/vitals` with `source: "presage"`. Readings outside human physiology
are rejected rather than stored — a camera that loses the face emits zeros, and a
zero pulse recorded beside *"the baby moved less"* reads as a catastrophe rather
than a dropped frame.

Presage readings are never averaged together with accelerometer estimates.
Mixing a cleared instrument with a ±20% one and reporting a single number would
quietly launder the caveat off the weaker source, so the best source present
wins outright.
