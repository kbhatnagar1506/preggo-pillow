package vitals

import (
	"testing"
	"time"
)

// A camera that loses the face, or a bridge that drops a field, yields zeros.
// A zero pulse written beside "the baby moved less" reads as a catastrophe
// rather than a dropout, so it must never be stored.
func TestImplausibleReadingsRejected(t *testing.T) {
	cases := []struct {
		name string
		r    Reading
		ok   bool
	}{
		{"normal", Reading{PulseBPM: 72, BreathingRPM: 14}, true},
		{"pulse only", Reading{PulseBPM: 68}, true},
		{"breathing only", Reading{BreathingRPM: 16}, true},
		{"both zero", Reading{}, false},
		{"pulse too low", Reading{PulseBPM: 12, BreathingRPM: 14}, false},
		{"pulse too high", Reading{PulseBPM: 260, BreathingRPM: 14}, false},
		{"breathing too low", Reading{PulseBPM: 70, BreathingRPM: 1}, false},
		{"breathing too high", Reading{PulseBPM: 70, BreathingRPM: 90}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.r.Plausible()
			if c.ok && err != nil {
				t.Errorf("should be accepted: %v", err)
			}
			if !c.ok && err == nil {
				t.Error("should have been rejected")
			}
		})
	}
}

func TestStoreRejectsImplausible(t *testing.T) {
	s := NewStore(10)
	if err := s.Add(Reading{}); err == nil {
		t.Error("an empty reading should be refused")
	}
	if _, ok := s.Latest(); ok {
		t.Error("a refused reading must not be stored")
	}
}

// Pregnancy raises resting heart rate, so the ceiling is above a textbook 100.
func TestNormalRangeAllowsPregnancy(t *testing.T) {
	if !(Reading{PulseBPM: 104, BreathingRPM: 16}).Normal() {
		t.Error("104 bpm is unremarkable in pregnancy and must not be flagged")
	}
	if (Reading{PulseBPM: 140, BreathingRPM: 16}).Normal() {
		t.Error("140 bpm should not read as normal")
	}
	if (Reading{PulseBPM: 72, BreathingRPM: 30}).Normal() {
		t.Error("30 breaths a minute should not read as normal")
	}
}

func TestClearedOnlyForPresage(t *testing.T) {
	if !SourcePresage.Cleared() {
		t.Error("Presage is FDA cleared")
	}
	if SourceAccel.Cleared() || SourceManual.Cleared() {
		t.Error("only the cleared instrument may claim clearance")
	}
	if !contains(SourcePresage.Accuracy(), "1.32") {
		t.Errorf("accuracy should quote the published RMSE: %s", SourcePresage.Accuracy())
	}
	if !contains(SourceAccel.Accuracy(), "20%") {
		t.Errorf("the accelerometer must carry its caveat: %s", SourceAccel.Accuracy())
	}
}

// Averaging an FDA-cleared measurement together with an accelerometer estimate
// would launder the caveat off the weaker one. The best source present wins
// outright.
func TestSummariseDoesNotMixSources(t *testing.T) {
	s := NewStore(50)
	now := time.Now()
	_ = s.Add(Reading{At: now, PulseBPM: 60, BreathingRPM: 12, Source: SourceAccel})
	_ = s.Add(Reading{At: now, PulseBPM: 80, BreathingRPM: 18, Source: SourcePresage})
	_ = s.Add(Reading{At: now, PulseBPM: 82, BreathingRPM: 18, Source: SourcePresage})

	sum, ok := s.Summarise(time.Hour)
	if !ok {
		t.Fatal("expected a summary")
	}
	if sum.Source != SourcePresage {
		t.Errorf("source = %s, want presage", sum.Source)
	}
	if sum.Count != 2 {
		t.Errorf("count = %d; the accelerometer reading should be excluded", sum.Count)
	}
	if sum.PulseBPM < 80 || sum.PulseBPM > 82 {
		t.Errorf("pulse = %.1f; averaging pulled in the 60 bpm accelerometer reading", sum.PulseBPM)
	}
	if !sum.Cleared {
		t.Error("a presage summary should be marked cleared")
	}
}

func TestSummariseFallsBackToAccelerometer(t *testing.T) {
	s := NewStore(50)
	_ = s.Add(Reading{At: time.Now(), PulseBPM: 70, BreathingRPM: 15, Source: SourceAccel})
	sum, ok := s.Summarise(time.Hour)
	if !ok || sum.Source != SourceAccel {
		t.Fatalf("summary = %+v", sum)
	}
	if sum.Cleared {
		t.Error("an accelerometer summary must not claim clearance")
	}
}

func TestSummariseHonoursTheWindow(t *testing.T) {
	s := NewStore(50)
	_ = s.Add(Reading{At: time.Now().Add(-2 * time.Hour), PulseBPM: 70, Source: SourcePresage})
	if _, ok := s.Summarise(30 * time.Minute); ok {
		t.Error("readings outside the window should not be summarised")
	}
}

func TestStoreIsBounded(t *testing.T) {
	s := NewStore(5)
	for i := 0; i < 40; i++ {
		_ = s.Add(Reading{PulseBPM: float64(60 + i%20), Source: SourcePresage})
	}
	s.mu.RLock()
	n := len(s.buf)
	s.mu.RUnlock()
	if n > 5 {
		t.Errorf("buffer grew to %d; an overnight run would leak memory", n)
	}
}

func TestAddDefaultsTimestampAndSource(t *testing.T) {
	s := NewStore(5)
	if err := s.Add(Reading{PulseBPM: 70}); err != nil {
		t.Fatal(err)
	}
	r, _ := s.Latest()
	if r.At.IsZero() {
		t.Error("timestamp should default to now")
	}
	if r.Source != SourceManual {
		t.Errorf("source defaulted to %q, want manual", r.Source)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
