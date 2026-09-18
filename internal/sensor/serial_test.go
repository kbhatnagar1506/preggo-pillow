package sensor

import (
	"strings"
	"testing"
	"time"
)

// newTestSerial builds a Serial with no ports open, so pumpReader can be driven
// directly from a string. The line protocol is where a half-reset Arduino would
// corrupt a night's data, so it is worth exercising without hardware.
func newTestSerial() *Serial {
	return &Serial{
		readings:  make(chan Reading, 256),
		acoustics: make(chan Acoustic, 256),
		ports:     map[string]serialPort{},
		stop:      make(chan struct{}),
	}
}

func drainReadings(s *Serial) []Reading {
	var out []Reading
	for {
		select {
		case r := <-s.readings:
			out = append(out, r)
		default:
			return out
		}
	}
}

func drainAcoustics(s *Serial) []Acoustic {
	var out []Acoustic
	for {
		select {
		case a := <-s.acoustics:
			out = append(out, a)
		default:
			return out
		}
	}
}

// The hello line is how a board announces which node it is. Data before hello
// must be dropped rather than attributed to the wrong node, because a reading
// filed under the wrong sensor breaks reference subtraction silently.
func TestSerialIgnoresDataBeforeHello(t *testing.T) {
	s := newTestSerial()
	s.pumpReader("test", strings.NewReader(
		`{"x":0.1,"y":-1.0,"z":0.2}`+"\n"+
			`{"x":0.2,"y":-1.0,"z":0.3}`+"\n"))

	if got := drainReadings(s); len(got) != 0 {
		t.Errorf("accepted %d readings before the board identified itself", len(got))
	}
}

func TestSerialAttributesReadingsToTheNodeFromHello(t *testing.T) {
	s := newTestSerial()
	s.pumpReader("test", strings.NewReader(
		`{"hello":"ref","chip":"adxl345","addr":83}`+"\n"+
			`{"x":0.01,"y":-0.99,"z":0.02}`+"\n"+
			`{"x":0.02,"y":-0.98,"z":0.03}`+"\n"))

	got := drainReadings(s)
	if len(got) != 2 {
		t.Fatalf("got %d readings, want 2", len(got))
	}
	for _, r := range got {
		if r.Node != NodeRef {
			t.Errorf("reading attributed to %q, want %q", r.Node, NodeRef)
		}
		if r.T.IsZero() {
			t.Error("reading has no timestamp; the Pi must stamp on arrival")
		}
	}
	if got[0].AX != 0.01 || got[0].AY != -0.99 || got[0].AZ != 0.02 {
		t.Errorf("axes did not round-trip: %+v", got[0])
	}
}

// An explicit "n" on each line must win, so boards can be unplugged and
// replugged in any order without the Pi mis-filing their data.
func TestSerialPerLineNodeOverridesHello(t *testing.T) {
	s := newTestSerial()
	s.pumpReader("test", strings.NewReader(
		`{"hello":"ref"}`+"\n"+
			`{"n":"abdo_a","x":0.5,"y":-1,"z":0.5}`+"\n"))

	got := drainReadings(s)
	if len(got) != 1 {
		t.Fatalf("got %d readings, want 1", len(got))
	}
	if got[0].Node != NodeAbdoA {
		t.Errorf("node %q, want %q", got[0].Node, NodeAbdoA)
	}
}

// Boot noise, partial frames after a reconnect, and non-JSON must be skipped
// without killing the stream.
func TestSerialSkipsGarbageWithoutStopping(t *testing.T) {
	s := newTestSerial()
	s.pumpReader("test", strings.NewReader(
		`{"hello":"abdo_a"}`+"\n"+
			"\n"+
			"garbage not json\n"+
			`{"x":0.1,"y":`+"\n"+ // truncated frame
			`{"x":0.3,"y":-1,"z":0.4}`+"\n"))

	got := drainReadings(s)
	if len(got) != 1 {
		t.Fatalf("got %d readings, want 1 good one after the garbage", len(got))
	}
	if got[0].AX != 0.3 {
		t.Errorf("wrong reading survived: %+v", got[0])
	}
}

// Only the board with the contact mic sends "s". A line without it must not
// emit a zero-valued acoustic sample, or the snore floor collapses.
func TestSerialEmitsAcousticOnlyWhenPresent(t *testing.T) {
	s := newTestSerial()
	s.pumpReader("test", strings.NewReader(
		`{"hello":"abdo_a"}`+"\n"+
			`{"x":0.1,"y":-1,"z":0.1}`+"\n"+
			`{"x":0.1,"y":-1,"z":0.1,"s":512}`+"\n"))

	ac := drainAcoustics(s)
	if len(ac) != 1 {
		t.Fatalf("got %d acoustic samples, want 1", len(ac))
	}
	if ac[0].RMS != 512 {
		t.Errorf("acoustic RMS %v, want 512", ac[0].RMS)
	}
}

// A zero is a real reading from a silent room and must be kept; only an absent
// field means "this board has no microphone".
func TestSerialKeepsZeroAcoustic(t *testing.T) {
	s := newTestSerial()
	s.pumpReader("test", strings.NewReader(
		`{"hello":"abdo_a"}`+"\n"+
			`{"x":0,"y":-1,"z":0,"s":0}`+"\n"))

	if ac := drainAcoustics(s); len(ac) != 1 {
		t.Fatalf("got %d acoustic samples, want 1: zero is a real measurement", len(ac))
	}
}

func TestSerialTimestampsAreMonotonicOnArrival(t *testing.T) {
	s := newTestSerial()
	var b strings.Builder
	b.WriteString(`{"hello":"ref"}` + "\n")
	for i := 0; i < 20; i++ {
		b.WriteString(`{"x":0,"y":-1,"z":0}` + "\n")
	}
	start := time.Now()
	s.pumpReader("test", strings.NewReader(b.String()))

	got := drainReadings(s)
	if len(got) != 20 {
		t.Fatalf("got %d readings, want 20", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].T.Before(got[i-1].T) {
			t.Fatal("timestamps went backwards")
		}
	}
	if got[0].T.Before(start) {
		t.Error("timestamp predates the read; it must come from the Pi clock at arrival")
	}
}

func TestMagnitude(t *testing.T) {
	r := Reading{AX: 3, AY: 4, AZ: 0}
	if got := r.Magnitude(); got != 5 {
		t.Errorf("magnitude %v, want 5", got)
	}
}
