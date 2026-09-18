package sensor

import (
	"testing"
	"time"
)

// collect drains readings for d, grouped by node.
func collect(s *Sim, d time.Duration) map[Node][]Reading {
	out := map[Node][]Reading{}
	deadline := time.After(d)
	for {
		select {
		case <-deadline:
			return out
		case r, ok := <-s.Readings():
			if !ok {
				return out
			}
			out[r.Node] = append(out[r.Node], r)
		}
	}
}

func TestSimProducesAllThreeNodes(t *testing.T) {
	s := NewSim(200)
	defer s.Close()
	got := collect(s, 400*time.Millisecond)
	for _, n := range []Node{NodeAbdoA, NodeAbdoB, NodeRef} {
		if len(got[n]) == 0 {
			t.Errorf("node %q produced no readings", n)
		}
	}
}

// The simulator has to model the one thing the detector depends on: a kick
// reaches the abdominal nodes and NOT the reference. If it leaked onto the
// reference, the detector would look broken when it is correct.
func TestSimKickReachesAbdominalNodesOnly(t *testing.T) {
	s := NewSim(200)
	defer s.Close()
	collect(s, 150*time.Millisecond) // settle

	s.InjectKick(0.5)
	got := collect(s, 250*time.Millisecond)

	// Gravity sits on X and breathing rides on Z, so Y is the only axis that
	// carries kick energy alone.
	peak := func(rs []Reading) float64 {
		var m float64
		for _, r := range rs {
			if d := abs(r.AY); d > m {
				m = d
			}
		}
		return m
	}
	abdo := peak(got[NodeAbdoA])
	ref := peak(got[NodeRef])
	if abdo <= ref*4 {
		t.Errorf("kick energy abdo=%.4f ref=%.4f: a kick must be far stronger at the abdomen", abdo, ref)
	}
}

// Maternal movement must land on EVERY node, including the reference, so the
// subtraction has something to cancel.
func TestSimMaternalMovementReachesEveryNode(t *testing.T) {
	s := NewSim(200)
	defer s.Close()
	collect(s, 150*time.Millisecond)

	s.InjectMaternal(0.6)
	got := collect(s, 400*time.Millisecond)

	for _, n := range []Node{NodeAbdoA, NodeAbdoB, NodeRef} {
		var m float64
		for _, r := range got[n] {
			if d := abs(r.AY); d > m {
				m = d
			}
		}
		if m < 0.05 {
			t.Errorf("node %q saw almost nothing (%.4f) from maternal movement", n, m)
		}
	}
}

func TestSimBreathes(t *testing.T) {
	s := NewSim(200)
	defer s.Close()
	got := collect(s, 3*time.Second)

	rs := got[NodeRef]
	if len(rs) < 100 {
		t.Fatalf("only %d reference readings", len(rs))
	}
	lo, hi := rs[0].AZ, rs[0].AZ
	for _, r := range rs {
		if r.AZ < lo {
			lo = r.AZ
		}
		if r.AZ > hi {
			hi = r.AZ
		}
	}
	// Breathing amplitude is 0.035g, noise is ±0.006g.
	if hi-lo < 0.03 {
		t.Errorf("Z range %.4f: respiration is not present, so the maternal panel would measure noise", hi-lo)
	}
}

func TestSimCloseIsIdempotentAndClosesChannels(t *testing.T) {
	s := NewSim(100)
	s.Close()
	s.Close() // must not panic on a second close

	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("readings channel never closed after Close")
		case _, ok := <-s.Readings():
			if !ok {
				return
			}
		}
	}
}

func TestSimDefaultsSampleRate(t *testing.T) {
	s := NewSim(0)
	defer s.Close()
	if s.SampleHz != 100 {
		t.Errorf("SampleHz %d with a zero argument, want the 100 Hz default", s.SampleHz)
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func TestDiscoverPortsFiltersNoise(t *testing.T) {
	// Not asserting on this machine's devices; asserting the filter's contract.
	got, err := DiscoverPorts()
	if err != nil {
		t.Skipf("no serial enumeration here: %v", err)
	}
	for _, p := range got {
		if len(p) >= 9 && p[:9] == "/dev/tty." {
			t.Errorf("returned %q: on macOS the cu.* node is the one that does not block on carrier detect", p)
		}
	}
}
