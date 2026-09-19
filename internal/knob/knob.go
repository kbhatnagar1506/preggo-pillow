// Package knob turns a rotary angle sensor into discrete "I felt that" events.
//
// This is the human channel of the blind test. The schema has always kept three
// separate records — what the phantom fired, what the detector found, and what a
// person felt — and this is the physical input for the third.
//
// It matters because the clinical premise of Lull is that women under-perceive
// fetal movement. Putting a person's own count beside the machine's is the
// argument, made rather than asserted.
package knob

import "time"

// Detector converts a stream of millivolt readings into turn events.
//
// The hard part is that one human gesture produces hundreds of samples. Naively
// emitting an event per sample over a threshold would turn a single turn into a
// burst of fifty presses, and the comparison the whole demo rests on would be
// meaningless.
//
// So a turn is only counted once it has STOPPED: the reading must move far
// enough from where it last settled, and then hold still. That maps one
// deliberate gesture to exactly one event, and ignores a hand resting on the
// dial.
type Detector struct {
	// MinDelta is how far the dial must move from its last settled position
	// before a turn counts, in millivolts. Below this is noise and hand tremor.
	MinDelta int

	// Quiet is how still the reading must be to count as settled.
	Quiet time.Duration

	// Jitter is how much the reading may wander while still counting as still.
	Jitter int

	settled int  // where the dial rested when the last event fired
	have    bool // settled is valid
	moving  bool

	// refVal is the last reading that differed meaningfully from its
	// predecessor, and refTime is when that happened. Stillness is measured
	// against this, NOT against the previous sample: a deliberate turn moves
	// only ~14 mV between samples at 100 Hz, which is below the jitter
	// threshold, so sample-to-sample comparison reads a smooth sweep as
	// perfectly still and never notices the gesture at all.
	refVal  int
	refTime time.Time
}

// New returns a detector tuned for a Grove rotary angle sensor read through the
// Base HAT's ADC, which spans roughly 0-3300 mV across the dial's travel.
func New() *Detector {
	return &Detector{
		MinDelta: 250,                    // ~8% of travel: a deliberate notch
		Quiet:    180 * time.Millisecond, // long enough to be a stop, short enough to feel instant
		Jitter:   40,                     // the ADC's own wobble on a driven input
	}
}

// Feed supplies one reading. It reports true exactly once per completed turn.
func (d *Detector) Feed(t time.Time, mv int) bool {
	if !d.have {
		d.settled, d.have = mv, true
		d.refVal, d.refTime = mv, t
		return false
	}

	// Has the dial drifted away from where it last sat still?
	if abs(mv-d.refVal) > d.Jitter {
		d.refVal, d.refTime = mv, t
		if abs(mv-d.settled) > d.MinDelta {
			d.moving = true
		}
	}

	// A gesture ends when the dial has travelled far enough AND then held
	// still. Firing on distance alone would emit an event on every sample for
	// the rest of a slow sweep.
	if d.moving && t.Sub(d.refTime) >= d.Quiet {
		d.moving = false
		d.settled = mv
		return true
	}
	return false
}

// Settled reports the dial's current resting reading, for display.
func (d *Detector) Settled() int { return d.settled }

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
