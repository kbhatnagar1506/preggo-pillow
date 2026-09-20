# Contributing

```bash
go run ./cmd/lull -source sim      # the whole product, no hardware, no keys
go test ./... -race                # 304 tests
```

Nothing external is required to develop on this. `-source sim` runs a simulated
pregnancy end to end.

## The one rule

**Do not make this thing reassure anyone.**

False reassurance is the documented failure mode of home fetal monitoring: it
delays women from seeking care. The system is permitted to say *this is
different, be seen today*. It is never permitted to say the baby is fine, and it
never diagnoses.

Several tests exist only to protect that, and they will look strange if you do
not know why they are there:

- the fetal heart-rate disclaimer cannot be deleted
- the clinical prompt still forbids reassurance, clause by clause
- removing a medication keeps the doses already recorded
- no user-facing surface may say anything but "Preggo Pillow"

If one of those fails, the fix is almost never to change the test.

## Things that will bite you

**The count reads `detections` only.** `commands` is what the servo was told to
do and `presses` is what a human said they felt. Keeping them apart is what lets
the system score itself, and it is the answer to "how do I know it isn't
counting your button presses". Do not merge them.

**High-pass each axis before taking the magnitude.** The other way round hides
motion perpendicular to gravity; a 0.3 g kick reads as 0.044 g.

**Nightly buckets run noon to noon.** Sleep crosses midnight, and a calendar-day
bucket turns one normal night into two apparently reduced ones.

**A missing reference node is not a degraded mode.** It is a mode that silently
counts the mother as the baby, so the detector refuses to emit anything.

## Before opening a PR

```bash
gofmt -l .        # must be empty
go vet ./...
go test ./... -race
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o /dev/null ./cmd/lull
```

CI runs all four. The cross-compile is there because the product ships as one
binary to a Raspberry Pi, and that breaks silently otherwise.

Comments explain *why*, not *what*. Most of the comments in this repository
exist because something cost hours; if you fix something subtle, leave the
reason behind for whoever hits it next.
