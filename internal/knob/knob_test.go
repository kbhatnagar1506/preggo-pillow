package knob

import (
	"testing"
	"time"
)

// feed replays a series of readings at a fixed rate and counts events.
func feed(d *Detector, hz int, vals []int) int {
	t := time.Unix(0, 0)
	step := time.Second / time.Duration(hz)
	n := 0
	for _, v := range vals {
		if d.Feed(t, v) {
			n++
		}
		t = t.Add(step)
	}
	return n
}

func hold(v, samples int) []int {
	out := make([]int, samples)
	for i := range out {
		out[i] = v
	}
	return out
}

func ramp(from, to, samples int) []int {
	out := make([]int, samples)
	for i := range out {
		out[i] = from + (to-from)*i/(samples-1)
	}
	return out
}

// The failure this whole design exists to prevent: one human gesture produces
// hundreds of samples, and counting each one turns a single turn into fifty
// presses. That would make the three-way comparison meaningless.
func TestOneTurnIsExactlyOneEvent(t *testing.T) {
	d := New()
	var v []int
	v = append(v, hold(1000, 40)...)       // resting
	v = append(v, ramp(1000, 1800, 60)...) // a turn, ~600ms at 100Hz
	v = append(v, hold(1800, 60)...)       // stopped
	if n := feed(d, 100, v); n != 1 {
		t.Errorf("one turn should give one event, got %d", n)
	}
}

func TestThreeTurnsGiveThreeEvents(t *testing.T) {
	d := New()
	var v []int
	v = append(v, hold(500, 40)...)
	for _, to := range []int{1000, 1500, 2000} {
		from := v[len(v)-1]
		v = append(v, ramp(from, to, 50)...)
		v = append(v, hold(to, 50)...)
	}
	if n := feed(d, 100, v); n != 3 {
		t.Errorf("three turns should give three events, got %d", n)
	}
}

// A hand resting on the dial, or ADC noise, must not register. If it did, the
// human count would drift upward on its own and beat the detector by cheating.
func TestNoiseDoesNotCount(t *testing.T) {
	d := New()
	v := make([]int, 600)
	for i := range v {
		v[i] = 1500 + (i%7)*5 - 15 // +/- 15 mV wobble
	}
	if n := feed(d, 100, v); n != 0 {
		t.Errorf("noise produced %d events, want 0", n)
	}
}

// A nudge below MinDelta is not a deliberate mark.
func TestSmallNudgeDoesNotCount(t *testing.T) {
	d := New()
	var v []int
	v = append(v, hold(1500, 40)...)
	v = append(v, ramp(1500, 1650, 30)...) // 150 mV, under the 250 threshold
	v = append(v, hold(1650, 60)...)
	if n := feed(d, 100, v); n != 0 {
		t.Errorf("a %d mV nudge should not count, got %d events", 150, n)
	}
}

// Turning back the other way is still a mark: the dial is a tally, not a
// direction control, and a person running out of travel will reverse.
func TestTurningBackAlsoCounts(t *testing.T) {
	d := New()
	var v []int
	v = append(v, hold(2000, 40)...)
	v = append(v, ramp(2000, 1200, 50)...)
	v = append(v, hold(1200, 60)...)
	if n := feed(d, 100, v); n != 1 {
		t.Errorf("a turn downward should count, got %d", n)
	}
}

// The event must fire when the gesture STOPS, not while it is still moving —
// otherwise a slow turn across the full range fires repeatedly on the way.
func TestSlowFullSweepIsStillOneEvent(t *testing.T) {
	d := New()
	var v []int
	v = append(v, hold(300, 40)...)
	v = append(v, ramp(300, 3000, 400)...) // 4 seconds of continuous turning
	v = append(v, hold(3000, 60)...)
	if n := feed(d, 100, v); n != 1 {
		t.Errorf("one slow sweep should be one event, got %d", n)
	}
}

func TestSettledTracksTheDial(t *testing.T) {
	d := New()
	var v []int
	v = append(v, hold(800, 40)...)
	v = append(v, ramp(800, 2200, 50)...)
	v = append(v, hold(2200, 60)...)
	feed(d, 100, v)
	if s := d.Settled(); s < 2100 || s > 2300 {
		t.Errorf("Settled() = %d, want about 2200", s)
	}
}

// The very first reading establishes a resting position; it is not a turn.
func TestFirstReadingIsNotAnEvent(t *testing.T) {
	d := New()
	if d.Feed(time.Unix(0, 0), 2500) {
		t.Error("the first sample must not fire an event")
	}
}

// A dial parked at an extreme still works — nothing assumes it starts centred.
func TestWorksFromEitherEndOfTravel(t *testing.T) {
	for _, start := range []int{0, 3300} {
		d := New()
		target := 1600
		var v []int
		v = append(v, hold(start, 40)...)
		v = append(v, ramp(start, target, 50)...)
		v = append(v, hold(target, 60)...)
		if n := feed(d, 100, v); n != 1 {
			t.Errorf("starting at %d gave %d events, want 1", start, n)
		}
	}
}
