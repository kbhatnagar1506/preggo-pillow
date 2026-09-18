// Package maternal computes the mother's metrics from the reference node.
//
// These exist because the override needs them to be REAL. The whole argument
// is "every maternal number is normal and the baby still moved 41% less",
// which only lands if those numbers came from a sensor. A hardcoded panel is a
// liability: the first judge who asks "is that computed?" gets a bad answer and
// starts doubting the fetal number too.
//
// Everything here reads the reference accelerometer (worn on the back) and the
// contact mic, so it costs no extra hardware.
//
// MOUNTING CONVENTION. Tape the reference node to the middle of her back with:
//
//	X  across the body, toward her left
//	Y  along the body, toward her head
//	Z  out of her back, away from the spine
//
// At rest an accelerometer reads +1g on whichever axis points up, so:
//
//	supine  (on her back)  ->  Z ~= -1     back is down
//	prone   (face down)    ->  Z ~= +1
//	lateral (on her side)  ->  X ~= +/-1
//	upright (sitting)      ->  Y ~= +/-1
//
// Respiration does NOT assume an axis: it is measured on whichever axis
// actually carries the oscillation, because the sensor will be taped on by
// hand at 3am and it will not be square.
package maternal

import (
	"math"
	"sync"
	"time"

	"github.com/kbhatnagar1506/lull/internal/sensor"
)

// Stats is the maternal panel.
type Stats struct {
	// Orientation, from the gravity vector on the back-worn reference node.
	Posture       string  `json:"posture"` // supine | lateral | upright | unknown
	SupineMinutes float64 `json:"supine_minutes"`

	// Respiration, from the slow component of the reference node. Breathing
	// moves the torso at roughly 0.15-0.5 Hz, which is 9-30 breaths a minute.
	RespirationRPM float64 `json:"respiration_rpm"`

	// Fragmentation: movement bursts big enough to look like a wake.
	WakeEvents int `json:"wake_events"`

	// Snore burden: share of acoustic samples above the rolling floor.
	SnorePercent float64 `json:"snore_percent"`

	// Whether each metric has enough data to be worth showing.
	Ready bool `json:"ready"`
}

const (
	// cos(45 degrees): an axis must dominate by more than this to name a posture.
	axisDominance = 0.707
	wakeThreshold = 0.22 // g of dynamic acceleration that reads as a real move
	wakeRefactory = 20 * time.Second
)

// Tracker accumulates maternal metrics. Safe for concurrent use.
type Tracker struct {
	mu sync.Mutex

	start time.Time

	// gravity: slow-moving mean per axis, which IS the gravity vector
	gx, gy, gz float64
	gInit      bool

	posture       string
	supineSince   time.Time
	supineTotal   time.Duration
	lastPostureAt time.Time

	// respiration: one window per axis. We do not know which way the sensor
	// was taped on, so we measure all three and trust the one that is
	// actually oscillating.
	respX, respY, respZ []float64
	respStamps          []time.Time
	lastRespRPM         float64

	// fragmentation
	wakeEvents int
	lastWake   time.Time

	// snore
	acousticWindow []float64
	acousticSum    float64
	snoreSamples   int
	totalSamples   int

	samples int
}

func NewTracker() *Tracker {
	return &Tracker{start: time.Now(), posture: "unknown"}
}

// Feed consumes a reading. Only the reference node is used: it is on her back,
// so it sees her and not the fetus, which is exactly what we want here.
func (t *Tracker) Feed(r sensor.Reading) {
	if r.Node != sensor.NodeRef {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	t.samples++

	// Exponential moving average IS the gravity vector plus posture, because
	// gravity is the only sustained acceleration a body in a bed experiences.
	const alpha = 0.002
	if !t.gInit {
		t.gx, t.gy, t.gz = r.AX, r.AY, r.AZ
		t.gInit = true
		t.lastPostureAt = r.T
	} else {
		t.gx += alpha * (r.AX - t.gx)
		t.gy += alpha * (r.AY - t.gy)
		t.gz += alpha * (r.AZ - t.gz)
	}

	t.updatePosture(r.T)

	// Dynamic acceleration: what is left once gravity is removed.
	dx, dy, dz := r.AX-t.gx, r.AY-t.gy, r.AZ-t.gz
	dyn := math.Sqrt(dx*dx + dy*dy + dz*dz)

	if dyn > wakeThreshold && r.T.Sub(t.lastWake) > wakeRefactory {
		t.wakeEvents++
		t.lastWake = r.T
	}

	t.feedRespiration(dx, dy, dz, r.T)
}

// updatePosture classifies orientation from the dominant gravity axis and
// accumulates time on her back. Supine going-to-sleep position in late
// pregnancy carries roughly 2.6x the odds of late stillbirth, so this number is
// not decoration.
func (t *Tracker) updatePosture(now time.Time) {
	mag := math.Sqrt(t.gx*t.gx + t.gy*t.gy + t.gz*t.gz)
	if mag < 0.5 {
		return // freefall or a dead sensor; do not guess
	}
	x, y, z := t.gx/mag, t.gy/mag, t.gz/mag

	p := "lateral"
	switch {
	case math.Abs(y) > axisDominance:
		p = "upright"
	case z < -axisDominance:
		p = "supine"
	case z > axisDominance:
		p = "prone"
	case math.Abs(x) > axisDominance:
		p = "lateral"
	}

	if !t.lastPostureAt.IsZero() && t.posture == "supine" {
		t.supineTotal += now.Sub(t.lastPostureAt)
	}
	t.lastPostureAt = now
	t.posture = p
}

// feedRespiration counts zero crossings of the slow vertical component, with
// hysteresis.
//
// Hysteresis is the whole thing. A plain zero-crossing counter on a noisy
// signal reports hundreds of breaths a minute, because noise crosses zero
// constantly. Requiring the signal to travel past +A before it can register a
// down-crossing (and vice versa) makes it count actual oscillations.
//
// Anything outside a plausible human range is reported as unknown rather than
// as a number. A made-up vital sign on the panel is worse than a blank.
func (t *Tracker) feedRespiration(dx, dy, dz float64, now time.Time) {
	t.respX = append(t.respX, dx)
	t.respY = append(t.respY, dy)
	t.respZ = append(t.respZ, dz)
	t.respStamps = append(t.respStamps, now)

	cutoff := now.Add(-30 * time.Second)
	for len(t.respStamps) > 0 && t.respStamps[0].Before(cutoff) {
		t.respX, t.respY, t.respZ = t.respX[1:], t.respY[1:], t.respZ[1:]
		t.respStamps = t.respStamps[1:]
	}
	if len(t.respStamps) < 100 {
		return
	}

	span := t.respStamps[len(t.respStamps)-1].Sub(t.respStamps[0]).Seconds()
	if span <= 1 {
		return
	}
	hz := float64(len(t.respStamps)) / span

	// Whichever axis is oscillating most is the one breathing shows up on.
	var bestAmp, bestRPM float64
	for _, win := range [][]float64{t.respX, t.respY, t.respZ} {
		amp, rpm := estimateOscillation(win, hz, span)
		if amp > bestAmp {
			bestAmp, bestRPM = amp, rpm
		}
	}

	if bestAmp < 0.004 || bestRPM < 6 || bestRPM > 40 {
		t.lastRespRPM = 0 // nothing oscillating, or implausible for a human
		return
	}
	t.lastRespRPM = bestRPM
}

// estimateOscillation returns the amplitude and rate of the slow oscillation in
// a window, counting cycles with hysteresis so that noise near zero does not
// register as breaths.
func estimateOscillation(win []float64, hz, span float64) (amp, rpm float64) {
	// Smooth hard. Breathing is under ~0.6 Hz; everything above it is noise.
	k := int(hz / 1.2)
	if k < 3 {
		k = 3
	}
	sm := movingAverage(win, k)

	lo, hi := sm[0], sm[0]
	for _, v := range sm {
		lo = math.Min(lo, v)
		hi = math.Max(hi, v)
	}
	amp = (hi - lo) / 2
	if amp <= 0 {
		return 0, 0
	}

	mid := (hi + lo) / 2
	gate := amp * 0.5
	state, cycles := 0, 0
	for _, v := range sm {
		d := v - mid
		switch {
		case d > gate && state <= 0:
			if state == -1 {
				cycles++
			}
			state = 1
		case d < -gate && state >= 0:
			state = -1
		}
	}
	return amp, float64(cycles) / span * 60
}

func movingAverage(in []float64, k int) []float64 {
	out := make([]float64, len(in))
	var run float64
	for i, v := range in {
		run += v
		if i >= k {
			run -= in[i-k]
		}
		n := math.Min(float64(i+1), float64(k))
		out[i] = run / n
	}
	return out
}

// FeedAcoustic accumulates snore burden.
func (t *Tracker) FeedAcoustic(a sensor.Acoustic) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.acousticWindow = append(t.acousticWindow, a.RMS)
	t.acousticSum += a.RMS
	if len(t.acousticWindow) > 3000 {
		t.acousticSum -= t.acousticWindow[0]
		t.acousticWindow = t.acousticWindow[1:]
	}
	if len(t.acousticWindow) < 50 {
		return
	}
	floor := t.acousticSum / float64(len(t.acousticWindow))
	t.totalSamples++
	if a.RMS > floor*1.6 {
		t.snoreSamples++
	}
}

// Stats returns a snapshot.
func (t *Tracker) Stats() Stats {
	t.mu.Lock()
	defer t.mu.Unlock()

	s := Stats{
		Posture:        t.posture,
		SupineMinutes:  t.supineTotal.Minutes(),
		RespirationRPM: t.lastRespRPM,
		WakeEvents:     t.wakeEvents,
		Ready:          t.samples > 500,
	}
	if t.totalSamples > 0 {
		s.SnorePercent = float64(t.snoreSamples) / float64(t.totalSamples) * 100
	}
	return s
}
