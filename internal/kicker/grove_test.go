package kicker

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeBus records every write so the exact bytes on the wire can be asserted.
// The driver's firmware silently ignores commands it does not understand, so a
// wrong byte produces no error anywhere — only a phantom that never moves.
// These tests are the only thing standing between that and a dead demo.
type fakeBus struct {
	mu     sync.Mutex
	writes [][]byte
	addrs  []uint8
	err    error
	failOn int // 1-based index of the write to fail; 0 = never
	n      int
}

func (f *fakeBus) Write(addr uint8, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	if f.failOn == f.n {
		return f.err
	}
	cp := append([]byte(nil), data...)
	f.writes = append(f.writes, cp)
	f.addrs = append(f.addrs, addr)
	return nil
}

func (f *fakeBus) all() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.writes...)
}

func eq(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The exact bytes Seeed's firmware expects. Every command is three bytes and
// the third is padding on the ones that take a single argument — omit it and
// the board ignores the command entirely.
func TestEncodingMatchesSeeedProtocol(t *testing.T) {
	cases := []struct {
		name string
		got  []byte
		want []byte
	}{
		{"speed", EncodeSpeed(160, 0), []byte{0x82, 160, 0}},
		{"speed both", EncodeSpeed(255, 128), []byte{0x82, 255, 128}},
		{"direction cw", EncodeDirection(dirBothClockWise), []byte{0xAA, 0x0A, 0x01}},
		{"direction acw", EncodeDirection(dirBothAntiClockWise), []byte{0xAA, 0x05, 0x01}},
		{"frequency", EncodeFrequency(freq31372Hz), []byte{0x84, 0x01, 0x01}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !eq(c.got, c.want) {
				t.Errorf("got % #x, want % #x", c.got, c.want)
			}
			if len(c.got) != 3 {
				t.Errorf("every command must be exactly 3 bytes, got %d", len(c.got))
			}
		})
	}
}

func TestDefaultAddressIsTheFactoryOne(t *testing.T) {
	g := NewGroveServo(&fakeBus{}, 0)
	if g.Addr != 0x0F {
		t.Errorf("addr = 0x%02X, want 0x0F", g.Addr)
	}
	g2 := NewGroveServo(&fakeBus{}, 0x0A)
	if g2.Addr != 0x0A {
		t.Errorf("explicit addr not honoured: 0x%02X", g2.Addr)
	}
}

// The driver powers up with an undefined duty cycle. If Init does not park it
// stopped, the phantom can start running the moment the program opens the bus
// and pollute the recording before anyone has pressed anything.
func TestInitParksTheMotorStopped(t *testing.T) {
	f := &fakeBus{}
	g := NewGroveServo(f, 0)
	if err := g.Init(); err != nil {
		t.Fatal(err)
	}
	w := f.all()
	if len(w) != 3 {
		t.Fatalf("expected frequency, direction, stop — got %d writes", len(w))
	}
	if !eq(w[0], []byte{0x84, 0x01, 0x01}) {
		t.Errorf("first write should set frequency, got % #x", w[0])
	}
	if !eq(w[2], []byte{0x82, 0, 0}) {
		t.Errorf("init must end stopped, got % #x", w[2])
	}
}

// A kick must always end with the motor stopped. This is the one failure in
// the whole program with a physical consequence: a motor left driving keeps
// hammering the phantom, which both ruins the recording and eventually breaks
// something.
func TestKickAlwaysStopsTheMotor(t *testing.T) {
	for _, strength := range []string{Weak, Medium, Strong, "nonsense"} {
		t.Run(strength, func(t *testing.T) {
			f := &fakeBus{}
			g := NewGroveServo(f, 0)
			if err := g.Kick(strength, 0.3); err != nil {
				t.Fatal(err)
			}
			w := f.all()
			last := w[len(w)-1]
			if !eq(last, []byte{0x82, 0, 0}) {
				t.Errorf("last write must be stop, got % #x", last)
			}
		})
	}
}

func TestKickDrivesThenStops(t *testing.T) {
	f := &fakeBus{}
	g := NewGroveServo(f, 0)
	if err := g.Init(); err != nil {
		t.Fatal(err)
	}
	before := len(f.all())
	if err := g.Kick(Strong, 0); err != nil {
		t.Fatal(err)
	}
	w := f.all()[before:]
	if len(w) != 2 {
		t.Fatalf("a kick is start+stop, got %d writes", len(w))
	}
	if !eq(w[0], []byte{0x82, 255, 0}) {
		t.Errorf("strong should drive at full duty, got % #x", w[0])
	}
	if !eq(w[1], []byte{0x82, 0, 0}) {
		t.Errorf("should stop, got % #x", w[1])
	}
}

// Weak must be meaningfully weaker than strong in BOTH duty and duration. A DC
// motor needs a few ms to spin up, so shortening the pulse is what actually
// makes weak faint; dropping the duty alone does much less than you would
// expect. If weak is obvious to a person, the blind test proves nothing.
func TestWeakIsGenuinelyWeakerInBothDimensions(t *testing.T) {
	w, m, s := PulseFor(Weak), PulseFor(Medium), PulseFor(Strong)
	if !(w.Speed < m.Speed && m.Speed < s.Speed) {
		t.Errorf("duty should increase weak<medium<strong: %d %d %d", w.Speed, m.Speed, s.Speed)
	}
	if !(w.For < m.For && m.For < s.For) {
		t.Errorf("duration should increase weak<medium<strong: %v %v %v", w.For, m.For, s.For)
	}
}

func TestKickBlocksForThePulseDuration(t *testing.T) {
	f := &fakeBus{}
	g := NewGroveServo(f, 0)
	_ = g.Init()
	start := time.Now()
	if err := g.Kick(Strong, 0); err != nil {
		t.Fatal(err)
	}
	d := time.Since(start)
	want := PulseFor(Strong).For
	if d < want {
		t.Errorf("Kick returned in %v, before the %v pulse finished", d, want)
	}
}

func TestKickInitialisesOnFirstUse(t *testing.T) {
	f := &fakeBus{}
	g := NewGroveServo(f, 0)
	if err := g.Kick(Medium, 0); err != nil {
		t.Fatal(err)
	}
	// frequency, direction, stop (init) then start, stop (kick)
	if n := len(f.all()); n != 5 {
		t.Errorf("expected init to run implicitly (5 writes), got %d", n)
	}
}

func TestKickReportsBusErrors(t *testing.T) {
	f := &fakeBus{err: errors.New("i2c broke"), failOn: 1}
	g := NewGroveServo(f, 0)
	if err := g.Kick(Medium, 0); err == nil {
		t.Error("a failing bus must surface an error, not be swallowed")
	}
}

func TestNilBusIsAnErrorNotAPanic(t *testing.T) {
	g := NewGroveServo(nil, 0)
	if err := g.Kick(Medium, 0); err == nil {
		t.Error("expected an error from a nil bus")
	}
	if err := g.Stop(); err != nil {
		t.Errorf("Stop on a nil bus should be a no-op, got %v", err)
	}
}

func TestGroveServoSatisfiesServo(t *testing.T) {
	var _ Servo = (*GroveServo)(nil)
}

func TestWritesGoToTheConfiguredAddress(t *testing.T) {
	f := &fakeBus{}
	g := NewGroveServo(f, 0x0F)
	_ = g.Kick(Weak, 0)
	for i, a := range f.addrs {
		if a != 0x0F {
			t.Errorf("write %d went to 0x%02X, want 0x0F", i, a)
		}
	}
}

// Overlapping pulses would stack: two goroutines could both start the motor and
// the first stop would end the second kick early, producing a kick shorter than
// any named strength and a ground truth that lies.
func TestConcurrentKicksDoNotInterleave(t *testing.T) {
	f := &fakeBus{}
	g := NewGroveServo(f, 0)
	_ = g.Init()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = g.Kick(Weak, 0) }()
	}
	wg.Wait()
	w := f.all()[3:] // skip init
	if len(w)%2 != 0 {
		t.Fatalf("expected start/stop pairs, got %d writes", len(w))
	}
	for i := 0; i < len(w); i += 2 {
		if w[i][1] == 0 {
			t.Errorf("write %d should be a start (non-zero duty), got % #x", i, w[i])
		}
		if w[i+1][1] != 0 {
			t.Errorf("write %d should be a stop, got % #x", i+1, w[i+1])
		}
	}
}
