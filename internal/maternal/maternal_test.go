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
