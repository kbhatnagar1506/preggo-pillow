package kicker

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// fakeSysfs builds the directory shape the kernel exposes, so the driver can be
// exercised with no Pi and no servo.
func fakeSysfs(t *testing.T, preExported bool) *PWMServo {
	t.Helper()
	root := t.TempDir()
	chip := filepath.Join(root, "pwmchip0")
	if err := os.MkdirAll(chip, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chip, "export"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if preExported {
		ch := filepath.Join(chip, "pwm0")
		if err := os.MkdirAll(ch, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, f := range []string{"period", "duty_cycle", "enable"} {
			if err := os.WriteFile(filepath.Join(ch, f), []byte("0"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	s := NewPWMServo("pwmchip0", 0)
	s.Root = root
	s.Hold = 5 * time.Millisecond // keep tests quick
	return s
}

func readInt(t *testing.T, path string) int64 {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	v, err := strconv.ParseInt(string(b), 10, 64)
	if err != nil {
		t.Fatalf("parse %q from %s: %v", b, path, err)
	}
	return v
}

// The three anchor points of the SG90 spec. Getting this mapping wrong either
// does nothing visible or drives the servo into its endstop.
func TestPulseForAngleMatchesServoSpec(t *testing.T) {
	s := NewPWMServo("pwmchip0", 0)
	cases := []struct {
		deg  int
		want time.Duration
	}{
		{0, 500 * time.Microsecond},
		{90, 1450 * time.Microsecond},
		{180, 2400 * time.Microsecond},
	}
	for _, c := range cases {
		got := s.PulseForAngle(c.deg)
		if d := got - c.want; d > 10*time.Microsecond || d < -10*time.Microsecond {
			t.Errorf("angle %d -> %v, want about %v", c.deg, got, c.want)
		}
	}
}

// An out-of-range angle must clamp, not pass through. Grinding a servo against
// its endstop for the length of a demo burns the gears out.
func TestPulseForAngleClamps(t *testing.T) {
	s := NewPWMServo("pwmchip0", 0)
	if got := s.PulseForAngle(-40); got != s.MinPulse {
		t.Errorf("negative angle should clamp to MinPulse, got %v", got)
	}
	if got := s.PulseForAngle(9999); got != s.MaxPulse {
		t.Errorf("huge angle should clamp to MaxPulse, got %v", got)
	}
}

// The pulse must never exceed the frame period, or the servo sees a constant
// high line and no frame at all.
func TestPulseAlwaysFitsInsideThePeriod(t *testing.T) {
	s := NewPWMServo("pwmchip0", 0)
	for deg := 0; deg <= 180; deg += 10 {
		if p := s.PulseForAngle(deg); p >= s.Period {
			t.Fatalf("angle %d gives pulse %v, which does not fit in period %v", deg, p, s.Period)
		}
	}
}

func TestExportSetsPeriodBeforeDutyAndEnables(t *testing.T) {
	s := fakeSysfs(t, true)
	if err := s.Export(); err != nil {
		t.Fatal(err)
	}
	d := s.pwmDir()
	if got := readInt(t, filepath.Join(d, "period")); got != s.Period.Nanoseconds() {
		t.Errorf("period = %d, want %d", got, s.Period.Nanoseconds())
	}
	if got := readInt(t, filepath.Join(d, "enable")); got != 1 {
		t.Errorf("enable = %d, want 1", got)
	}
	// Must come up parked at rest, not at whatever the previous run left.
	want := s.PulseForAngle(RestAngle).Nanoseconds()
	if got := readInt(t, filepath.Join(d, "duty_cycle")); got != want {
		t.Errorf("duty at export = %d, want rest %d", got, want)
	}
}

// A paddle left pressed into the foam damps the pod and silently kills
// detection for the rest of the night. Every kick must come home.
func TestKickReturnsToRest(t *testing.T) {
	for _, strength := range []string{Weak, Medium, Strong, "nonsense"} {
		t.Run(strength, func(t *testing.T) {
			s := fakeSysfs(t, true)
			if err := s.Kick(strength, 0.3); err != nil {
				t.Fatal(err)
			}
			want := s.PulseForAngle(RestAngle).Nanoseconds()
			got := readInt(t, filepath.Join(s.pwmDir(), "duty_cycle"))
			if got != want {
				t.Errorf("ended at duty %d, want rest %d", got, want)
			}
		})
	}
}

func TestKickHoldsBeforeReturning(t *testing.T) {
	s := fakeSysfs(t, true)
	s.Hold = 80 * time.Millisecond
	start := time.Now()
	if err := s.Kick(Medium, 0); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d < s.Hold {
		t.Errorf("Kick returned in %v, before the %v hold elapsed", d, s.Hold)
	}
}

func TestStrongerKicksSwingFurther(t *testing.T) {
	s := NewPWMServo("pwmchip0", 0)
	w := s.PulseForAngle(angles[Weak])
	m := s.PulseForAngle(angles[Medium])
	st := s.PulseForAngle(angles[Strong])
	if !(w < m && m < st) {
		t.Errorf("pulse should grow weak<medium<strong: %v %v %v", w, m, st)
	}
	if w <= s.PulseForAngle(RestAngle) {
		t.Errorf("even a weak kick must swing past rest: weak=%v rest=%v", w, s.PulseForAngle(RestAngle))
	}
}

func TestSetAngleWritesTheRightDuty(t *testing.T) {
	s := fakeSysfs(t, true)
	if err := s.SetAngle(90); err != nil {
		t.Fatal(err)
	}
	want := s.PulseForAngle(90).Nanoseconds()
	if got := readInt(t, filepath.Join(s.pwmDir(), "duty_cycle")); got != want {
		t.Errorf("duty = %d, want %d", got, want)
	}
}

func TestMissingChipGivesAnActionableError(t *testing.T) {
	s := NewPWMServo("pwmchip0", 0)
	s.Root = t.TempDir() // empty: no chip
	err := s.Export()
	if err == nil {
		t.Fatal("expected an error with no pwmchip")
	}
	if !contains(err.Error(), "dtoverlay=pwm-2chan") {
		t.Errorf("the error should say how to fix it, got: %v", err)
	}
}

// A channel the kernel has not created yet must be exported first.
func TestExportCreatesTheChannelWhenAbsent(t *testing.T) {
	s := fakeSysfs(t, false)
	// Simulate the kernel reacting to the export write.
	go func() {
		time.Sleep(20 * time.Millisecond)
		ch := s.pwmDir()
		_ = os.MkdirAll(ch, 0o755)
		for _, f := range []string{"period", "duty_cycle", "enable"} {
			_ = os.WriteFile(filepath.Join(ch, f), []byte("0"), 0o644)
		}
	}()
	if err := s.Export(); err != nil {
		t.Fatalf("should wait for the kernel to create the channel: %v", err)
	}
}

func TestCloseParksAndDisables(t *testing.T) {
	s := fakeSysfs(t, true)
	if err := s.Export(); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAngle(120); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	d := s.pwmDir()
	if got := readInt(t, filepath.Join(d, "enable")); got != 0 {
		t.Errorf("enable = %d after Close, want 0", got)
	}
	want := s.PulseForAngle(RestAngle).Nanoseconds()
	if got := readInt(t, filepath.Join(d, "duty_cycle")); got != want {
		t.Errorf("should park at rest before disabling: duty %d, want %d", got, want)
	}
}

func TestCloseOnUnexportedIsANoOp(t *testing.T) {
	s := fakeSysfs(t, true)
	if err := s.Close(); err != nil {
		t.Errorf("Close before Export should be harmless, got %v", err)
	}
}

func TestPWMServoSatisfiesServo(t *testing.T) {
	var _ Servo = (*PWMServo)(nil)
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
