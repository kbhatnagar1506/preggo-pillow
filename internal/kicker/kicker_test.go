package kicker

import (
	"sync"
	"testing"
	"time"
)

type fakeServo struct {
	mu    sync.Mutex
	calls []string
	angle []int
}

func (f *fakeServo) Kick(strength string, _ float64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, strength)
	return nil
}

func (f *fakeServo) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func TestFireLogsAndDrivesTheServo(t *testing.T) {
	f := &fakeServo{}
	var gotStrength string
	var gotTime time.Time
	k := New(f, func(tm time.Time, s string) { gotTime, gotStrength = tm, s })

	k.Fire(Strong)

	if f.count() != 1 {
		t.Fatalf("servo called %d times, want 1", f.count())
	}
	if gotStrength != Strong {
		t.Errorf("logged strength %q, want %q", gotStrength, Strong)
	}
	if gotTime.IsZero() {
		t.Error("no timestamp logged; the command log is our ground truth")
	}
}

// An unknown strength must still fire rather than silently doing nothing, and
// it must be logged as what actually happened.
func TestFireUnknownStrengthFallsBackToMedium(t *testing.T) {
	f := &fakeServo{}
	var logged string
	k := New(f, func(_ time.Time, s string) { logged = s })

	k.Fire("nonsense")

	if f.count() != 1 {
		t.Fatalf("servo called %d times, want 1", f.count())
	}
	if logged != Medium {
		t.Errorf("logged %q, want %q: the log must record what was really delivered", logged, Medium)
	}
}

// The weak kicks are the point of the blind test: they are what a judge misses.
// If the distribution drifts to mostly-strong the demo stops being hard.
func TestFireRandomFavoursWeakKicks(t *testing.T) {
	f := &fakeServo{}
	k := New(f, nil)
	const n = 2000
	for i := 0; i < n; i++ {
		k.FireRandom()
	}

	counts := map[string]int{}
	f.mu.Lock()
	for _, c := range f.calls {
		counts[c]++
	}
	f.mu.Unlock()

	if counts[Weak]+counts[Medium]+counts[Strong] != n {
		t.Fatalf("unexpected strengths: %v", counts)
	}
	weakPct := float64(counts[Weak]) / n
	if weakPct < 0.35 || weakPct > 0.55 {
		t.Errorf("weak kicks %.0f%%, want roughly 45%%", weakPct*100)
	}
	if counts[Strong] == 0 || counts[Medium] == 0 {
		t.Errorf("a strength never appeared: %v", counts)
	}
}

func TestStartStopIsIdempotent(t *testing.T) {
	k := New(&fakeServo{}, nil)
	if k.Running() {
		t.Fatal("running before Start")
	}
	k.Start()
	k.Start() // must not panic or double-launch
	if !k.Running() {
		t.Fatal("not running after Start")
	}
	k.Stop()
	k.Stop() // must not panic or close a closed channel
	if k.Running() {
		t.Fatal("still running after Stop")
	}
}

func TestSerialServoMapsStrengthToAngle(t *testing.T) {
	var got int
	s := SerialServo{Send: func(a int) error { got = a; return nil }}

	if err := s.Kick(Weak, 0); err != nil {
		t.Fatal(err)
	}
	weak := got
	if err := s.Kick(Strong, 0); err != nil {
		t.Fatal(err)
	}
	strong := got

	if !(weak < strong) {
		t.Errorf("weak angle %d is not less than strong %d; the blind test needs a real range", weak, strong)
	}
	if weak < 10 || strong > 120 {
		t.Errorf("angles %d/%d outside the sketch's clamp of 10-120", weak, strong)
	}
}

func TestSerialServoWithNoSendIsSafe(t *testing.T) {
	s := SerialServo{}
	if err := s.Kick(Medium, 0); err != nil {
		t.Errorf("Kick with no transport returned %v, want nil", err)
	}
}

func TestSimServoWithNoInjectIsSafe(t *testing.T) {
	s := SimServo{}
	if err := s.Kick(Medium, 0.3); err != nil {
		t.Errorf("Kick returned %v, want nil", err)
	}
}
