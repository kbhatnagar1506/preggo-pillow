package maternal

import (
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/kbhatnagar1506/lull/internal/sensor"
)

// feed synthesises a stretch of reference-node data: gravity on one axis, a
// breathing oscillation, and noise.
func feed(t *testing.T, tr *Tracker, seconds int, hz int, breathsPerMin float64, gx, gy, gz float64) {
	t.Helper()
	start := time.Now()
	breathHz := breathsPerMin / 60
	for i := 0; i < seconds*hz; i++ {
		el := float64(i) / float64(hz)
		breath := 0.035 * math.Sin(2*math.Pi*breathHz*el)
		n := func() float64 { return (rand.Float64() - 0.5) * 0.012 }
		// Breathing expands the torso, so it rides on Z (out of the back).
		tr.Feed(sensor.Reading{
			T:    start.Add(time.Duration(el * float64(time.Second))),
			Node: sensor.NodeRef,
			AX:   gx + n(),
			AY:   gy + n(),
			AZ:   gz + breath + n(),
		})
	}
}

// The bug this catches: a plain zero-crossing counter on a noisy signal
// reported 212 breaths per minute. A human does 12-20.
func TestRespirationIsPlausible(t *testing.T) {
	for _, want := range []float64{12, 15, 20} {
		tr := NewTracker()
		feed(t, tr, 40, 100, want, -1.0, 0, 0) // on her side
		got := tr.Stats().RespirationRPM
		if got < want-4 || got > want+4 {
			t.Errorf("breathing at %.0f rpm: got %.1f, want within 4", want, got)
		}
	}
}

// With no oscillation there is nothing to measure, and reporting a made-up
// vital sign is worse than reporting none.
func TestRespirationReportsUnknownOnNoiseOnly(t *testing.T) {
	tr := NewTracker()
	feed(t, tr, 40, 100, 0, -1.0, 0, 0)
	if got := tr.Stats().RespirationRPM; got != 0 {
		t.Errorf("noise only: got %.1f rpm, want 0 (unknown)", got)
	}
}

// Posture drives the supine-minutes number, and supine going-to-sleep position
// in late pregnancy carries roughly 2.6x the odds of late stillbirth, so this
// classification is not decoration.
func TestPostureClassification(t *testing.T) {
	cases := []struct {
		name       string
		gx, gy, gz float64
		want       string
	}{
		{"on her back", 0, 0, -1.0, "supine"},
		{"face down", 0, 0, 1.0, "prone"},
		{"on her side", -1.0, 0, 0, "lateral"},
		{"sitting up", 0, 1.0, 0, "upright"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr := NewTracker()
			feed(t, tr, 12, 100, 15, c.gx, c.gy, c.gz)
			if got := tr.Stats().Posture; got != c.want {
				t.Errorf("got posture %q, want %q", got, c.want)
			}
		})
	}
}

func TestSupineMinutesAccumulate(t *testing.T) {
	tr := NewTracker()
	feed(t, tr, 30, 100, 15, 0, 0, -1.0) // 30 seconds on her back
	got := tr.Stats().SupineMinutes
	if got < 0.3 || got > 0.6 {
		t.Errorf("30s supine: got %.2f min, want ~0.5", got)
	}
}

func TestNotReadyBeforeEnoughSamples(t *testing.T) {
	tr := NewTracker()
	feed(t, tr, 1, 100, 15, -1.0, 0, 0)
	if tr.Stats().Ready {
		t.Error("tracker claims ready after 100 samples; the panel would show noise")
	}
}

// ---- respiration: the floor, and what is allowed past it --------------------

// feedAmp is feed() with the breathing depth under the test's control, plus
// optional movement bursts. Bursts matter because the plain feed() helper
// models a signal the device will never actually see: a woman who breathes for
// forty seconds and never moves.
func feedAmp(t *testing.T, tr *Tracker, seconds, hz int, breathsPerMin, amp float64, bursts []float64, gx, gy, gz float64) {
	t.Helper()
	start := time.Now()
	breathHz := breathsPerMin / 60
	for i := 0; i < seconds*hz; i++ {
		el := float64(i) / float64(hz)
		v := amp * math.Sin(2*math.Pi*breathHz*el)
		// A half-sine burst of the shape and duration sensor.Sim injects for
		// maternal movement, landing inside the live 30-second window.
		for bi, ba := range bursts {
			at := float64(seconds) - 15 + float64(bi)*7
			if d := el - at; d >= 0 && d <= 0.6 {
				v += ba * math.Sin(d/0.6*math.Pi)
			}
		}
		n := func() float64 { return (rand.Float64() - 0.5) * 0.012 }
		tr.Feed(sensor.Reading{
			T:    start.Add(time.Duration(el * float64(time.Second))),
			Node: sensor.NodeRef,
			AX:   gx + n(), AY: gy + n(), AZ: gz + v + n(),
		})
	}
}

// The floor is 8, not 6.
//
// A sleeping adult at 6 breaths a minute is a medical emergency, and 6 was
// reachable: the old gate admitted it exactly. Worse, 6 is the figure this
// counter produces when it loses cycles, so the single rate it was most likely
// to invent was also the most alarming one to read back off the record.
func TestRespirationFloorRejectsBradypnoea(t *testing.T) {
	if err := PlausibleRespiration(6); err == nil {
		t.Error("6 rpm is accepted as a plausible sleeping adult; it is an emergency, and it is the estimator's favourite wrong answer")
	}
	for _, rpm := range []float64{0, 1, 4, 5, 6, 7, 7.9} {
		if err := PlausibleRespiration(rpm); err == nil {
			t.Errorf("%.1f rpm accepted, want rejected below the %d rpm floor", rpm, respFloorRPM)
		}
	}
	for _, rpm := range []float64{8, 10, 14, 20, 40} {
		if err := PlausibleRespiration(rpm); err != nil {
			t.Errorf("%.1f rpm rejected (%v), want accepted", rpm, err)
		}
	}
	if err := PlausibleRespiration(41); err == nil {
		t.Errorf("41 rpm accepted, want rejected above the %d rpm ceiling", respCeilingRPM)
	}
}

// End to end: a body breathing below the floor reports nothing, not a number.
func TestRespirationBelowTheFloorReportsNotMeasured(t *testing.T) {
	for _, bpm := range []float64{3, 4, 5, 6} {
		tr := NewTracker()
		feed(t, tr, 40, 100, bpm, -1.0, 0, 0)
		s := tr.Stats()
		if s.RespirationRPM != 0 {
			t.Errorf("breathing at %.0f rpm: reported %.1f, want 0 (not measured)", bpm, s.RespirationRPM)
		}
		if s.RespirationQuality != RespUnmeasured {
			t.Errorf("breathing at %.0f rpm: quality %q, want %q", bpm, s.RespirationQuality, RespUnmeasured)
		}
		if _, ok := s.RespirationForRecord(); ok {
			t.Errorf("breathing at %.0f rpm reached the record", bpm)
		}
	}
}

// The measurement the floor is chosen from. Referenced by the comment on
// respFloorRPM, and here so that a change to the smoothing or the window
// changes this test rather than silently invalidating that reasoning.
//
// Two properties: the counter is quantised to one cycle per window, which at 30
// seconds is 2 rpm; and it reads about one cycle low. Together they are why the
// floor is 8 and not a textbook 12 — a woman breathing an ordinary 12 reads
// back as 10, and a floor of 12 would throw her away.
func TestRespirationEstimatorIsQuantisedAndRunsLow(t *testing.T) {
	cases := []struct{ true_, want float64 }{
		{12, 10}, {15, 14}, {18, 16}, {20, 20},
	}
	for _, c := range cases {
		tr := NewTracker()
		feed(t, tr, 40, 100, c.true_, -1.0, 0, 0)
		got := tr.Stats().RespirationRPM
		if got != c.want {
			t.Errorf("true %.0f rpm reads %.1f, want %.0f: the floor and the guard band are chosen from this bias, re-derive them if it moves",
				c.true_, got, c.want)
		}
		if math.Mod(got, respQuantumRPM) != 0 {
			t.Errorf("true %.0f rpm produced %.1f, which is not a multiple of the %d rpm quantum the guard band assumes",
				c.true_, got, respQuantumRPM)
		}
	}
}

// The other measurement behind the constants: what a still sensor produces on
// its own noise. respMinAmp has to sit above this, or the panel reports the
// accelerometer's dither as a woman's breathing.
func TestRespirationRejectsTheSensorNoiseFloor(t *testing.T) {
	for seed := int64(0); seed < 40; seed++ {
		rand.Seed(seed + 1)
		tr := NewTracker()
		feed(t, tr, 35, 50, 0, -1.0, 0, 0)
		s := tr.Stats()
		if s.RespirationRPM != 0 || s.RespirationQuality != RespUnmeasured {
			t.Fatalf("seed %d: noise alone produced %.1f rpm (%q); respMinAmp=%v is below the sensor's own noise floor",
				seed, s.RespirationRPM, s.RespirationQuality, respMinAmp)
		}
	}
}

// A rate one lost cycle away from being rejected outright is not a measurement.
// The same window could as easily have reported nothing, so it is reported and
// labelled, never asserted.
func TestRespirationNearTheFloorIsProvisional(t *testing.T) {
	var inBand int
	for _, bpm := range []float64{9, 10, 11, 12} {
		tr := NewTracker()
		feed(t, tr, 40, 100, bpm, -1.0, 0, 0)
		s := tr.Stats()
		if s.RespirationRPM > respFloorRPM+respQuantumRPM {
			continue // it cleared the guard band honestly
		}
		inBand++
		if s.RespirationQuality == RespMeasured {
			t.Errorf("true %.0f rpm read %.1f and was called %q; anything within %d rpm of the %d rpm floor is one lost cycle from rejection",
				bpm, s.RespirationRPM, RespMeasured, respQuantumRPM, respFloorRPM)
		}
		if _, ok := s.RespirationForRecord(); ok {
			t.Errorf("true %.0f rpm read %.1f and reached the record", bpm, s.RespirationRPM)
		}
	}
	if inBand == 0 {
		t.Fatal("no case landed in the guard band, so this test asserted nothing: pick rates that read back at or under the floor plus one quantum")
	}
}

// Breathing barely above the sensor's noise gives a cycle count taken off a
// signal that is mostly not signal. The rate may be right; it is not evidence.
func TestRespirationOnAShallowSignalIsProvisional(t *testing.T) {
	tr := NewTracker()
	feedAmp(t, tr, 40, 100, 15, 0.006, nil, -1.0, 0, 0)
	s := tr.Stats()
	if s.RespirationQuality == RespMeasured {
		t.Errorf("a 0.006g swing was called %q; respFirmAmp is %v and the sensor's own noise reaches ~0.004g",
			RespMeasured, respFirmAmp)
	}
	// Deep, clean breathing at the same rate is the control: it must pass.
	tr2 := NewTracker()
	feedAmp(t, tr2, 40, 100, 15, 0.035, nil, -1.0, 0, 0)
	if q := tr2.Stats().RespirationQuality; q != RespMeasured {
		t.Errorf("clean 15 rpm at 0.035g was called %q, want %q: the guard band has swallowed normal breathing", q, RespMeasured)
	}
}

// The regression that started this.
//
// A single movement burst inside the window sets the peak-to-peak amplitude,
// which raises the hysteresis gate above the breathing swing, so breathing
// stops being counted and one or two stray cycles survive. The estimator then
// reports 2 to 7 rpm off a woman who is breathing a perfectly normal 15, with a
// large and confident-looking amplitude behind it. sensor.Sim injects exactly
// this every 8 to 28 seconds, so it is the common case and not an edge one.
//
// Whatever else is true, a figure produced this way must not reach the record.
func TestMovementBurstNeverProducesAConfidentRate(t *testing.T) {
	for _, burst := range []float64{0.06, 0.10, 0.15, 0.20, 0.33} {
		for seed := int64(1); seed <= 3; seed++ {
			rand.Seed(seed)
			tr := NewTracker()
			feedAmp(t, tr, 40, 100, 15, 0.035, []float64{burst}, -1.0, 0, 0)
			s := tr.Stats()
			if rpm, ok := s.RespirationForRecord(); ok {
				t.Errorf("a %.2fg movement burst (seed %d) put %.1f rpm into the record as a measurement; she was breathing 15",
					burst, seed, rpm)
			}
			if err := PlausibleRespiration(s.RespirationRPM); s.RespirationRPM != 0 && err != nil {
				t.Errorf("a %.2fg burst (seed %d) left an implausible %.1f rpm on Stats (%v)", burst, seed, s.RespirationRPM, err)
			}
		}
	}
}

// Nothing but a measurement leaves by the record-writing door.
func TestRespirationForRecordWithholdsAnythingButAMeasurement(t *testing.T) {
	for _, q := range []RespirationQuality{RespUnmeasured, RespProvisional, ""} {
		s := Stats{RespirationRPM: 6, RespirationQuality: q}
		if rpm, ok := s.RespirationForRecord(); ok {
			t.Errorf("quality %q released %.0f rpm to the record", q, rpm)
		}
		if q.Reportable() {
			t.Errorf("quality %q reports itself Reportable", q)
		}
	}
	s := Stats{RespirationRPM: 14, RespirationQuality: RespMeasured}
	if rpm, ok := s.RespirationForRecord(); !ok || rpm != 14 {
		t.Errorf("a measurement was withheld: got %.0f, %v", rpm, ok)
	}
}

// A tracker that has seen nothing says so, rather than leaving the field empty
// for a consumer to guess at.
func TestFreshTrackerReportsUnmeasured(t *testing.T) {
	if q := NewTracker().Stats().RespirationQuality; q != RespUnmeasured {
		t.Errorf("fresh tracker reports quality %q, want %q", q, RespUnmeasured)
	}
}
