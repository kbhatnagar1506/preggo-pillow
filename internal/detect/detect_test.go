package detect

import (
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/kbhatnagar1506/lull/internal/sensor"
)

const hz = 100

// rig feeds synthetic samples and collects detections.
type rig struct {
	d     *Detector
	t     time.Time
	found []Detection
}

func newRig(cfg Config) *rig {
	return &rig{d: New(cfg), t: time.Now()}
}

// advance pushes `secs` of data. kickAmp lands only on the abdominal nodes;
// matAmp lands on every node including the reference, so a correct detector
// must reject it.
func (r *rig) advance(secs float64, kickAmp, matAmp func(el float64) float64) {
	n := int(secs * hz)
	for i := 0; i < n; i++ {
		el := float64(i) / hz
		now := r.t.Add(time.Duration(el * float64(time.Second)))
		k, m := 0.0, 0.0
		if kickAmp != nil {
			k = kickAmp(el)
		}
		if matAmp != nil {
			m = matAmp(el)
		}
		noise := func() float64 { return (rand.Float64() - 0.5) * 0.012 }

		for _, node := range []sensor.Node{sensor.NodeAbdoA, sensor.NodeAbdoB, sensor.NodeRef} {
			v := m
			if node != sensor.NodeRef {
				v += k
			}
			r.d.Feed(sensor.Reading{
				T: now, Node: node,
				AX: v*0.6 + noise(),
				AY: -1.0 + noise(),
				AZ: v*0.8 + noise(),
			})
		}
		// acoustic at 50 Hz; the mic hears kicks strongly, the mother weakly
		if i%2 == 0 {
			r.d.FeedAcoustic(sensor.Acoustic{T: now, RMS: 2.0 + k*90 + m*8})
		}
		r.drain()
	}
	r.t = r.t.Add(time.Duration(secs * float64(time.Second)))
}

func (r *rig) drain() {
	for {
		select {
		case d := <-r.d.Out:
			r.found = append(r.found, d)
		default:
			return
		}
	}
}

// pulse is a half-sine of the given amplitude, starting at `at`, 180ms long.
func pulse(at, amp float64) func(float64) float64 {
	const dur = 0.18
	return func(el float64) float64 {
		if el < at || el > at+dur {
			return 0
		}
		return amp * math.Sin((el-at)/dur*math.Pi)
	}
}

func TestDetectsKicksAtEachStrength(t *testing.T) {
	for _, c := range []struct {
		name string
		amp  float64
	}{
		{"weak", 0.16}, {"medium", 0.30}, {"strong", 0.52},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(DefaultConfig())
			r.advance(3, nil, nil) // fill the high-pass window
			r.advance(2, pulse(0.5, c.amp), nil)
			if len(r.found) != 1 {
				t.Fatalf("%s kick: got %d detections, want 1", c.name, len(r.found))
			}
		})
	}
}

// This is the core claim of the whole project. Maternal movement lands on every
// node, so the reference subtraction must cancel it. If this test fails, the
// device is counting the mother as the baby.
func TestRejectsMaternalMovement(t *testing.T) {
	r := newRig(DefaultConfig())
	r.advance(3, nil, nil)
	for i := 0; i < 8; i++ {
		r.advance(1.5, nil, pulse(0.3, 0.45)) // large, on every node
	}
	if len(r.found) != 0 {
		t.Fatalf("maternal movement produced %d false detections, want 0", len(r.found))
	}
}

// Without a reference node there is no subtraction, and without subtraction we
// would be reporting maternal movement as fetal movement. Refusing to emit is
// the correct behaviour, not a bug.
func TestRefusesToDetectWithoutReferenceNode(t *testing.T) {
	d := New(DefaultConfig())
	start := time.Now()
	for i := 0; i < 400; i++ {
		now := start.Add(time.Duration(i) * 10 * time.Millisecond)
		v := 0.0
		if i > 200 && i < 220 {
			v = 0.5
		}
		// abdominal nodes only; no reference is ever fed
		for _, n := range sensor.AbdominalNodes {
			d.Feed(sensor.Reading{T: now, Node: n, AX: v, AY: -1, AZ: v})
		}
	}
	select {
	case det := <-d.Out:
		t.Fatalf("emitted a detection with no reference node: %+v", det)
	default:
	}
}

// A single kick rings. Without a refractory period one event registers several
// times and the nightly count is meaningless.
func TestRefractorySuppressesRinging(t *testing.T) {
	r := newRig(DefaultConfig())
	r.advance(3, nil, nil)
	r.advance(1, func(el float64) float64 {
		// two pulses 150ms apart, inside the refractory window
		return pulse(0.30, 0.5)(el) + pulse(0.45, 0.5)(el)
	}, nil)
	if len(r.found) != 1 {
		t.Fatalf("two pulses inside the refractory window gave %d detections, want 1", len(r.found))
	}
}

// One abdominal node plus the reference still gives the subtraction. Averaging
// over a hard-coded two would silently halve the residual when a cable comes
// loose, which reads as "the detector got worse" rather than "a cable fell out".
func TestWorksWithOneAbdominalNode(t *testing.T) {
	d := New(DefaultConfig())
	start := time.Now()
	k := pulse(3.0, 0.30)

	for i := 0; i < 500; i++ {
		el := float64(i) / hz
		now := start.Add(time.Duration(el * float64(time.Second)))
		v := k(el)
		noise := func() float64 { return (rand.Float64() - 0.5) * 0.012 }
		// abdo_a and ref only. abdo_b never appears.
		d.Feed(sensor.Reading{T: now, Node: sensor.NodeAbdoA, AX: v*0.6 + noise(), AY: -1 + noise(), AZ: v*0.8 + noise()})
		d.Feed(sensor.Reading{T: now, Node: sensor.NodeRef, AX: noise(), AY: -1 + noise(), AZ: noise()})
		d.FeedAcoustic(sensor.Acoustic{T: now, RMS: 2.0 + v*90})
	}

	var n int
	for {
		select {
		case <-d.Out:
			n++
			continue
		default:
		}
		break
	}
	if n != 1 {
		t.Fatalf("one abdominal node + reference: got %d detections, want 1", n)
	}
}
