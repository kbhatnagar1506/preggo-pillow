package sensor

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func traceJSONL(lines ...string) string { return strings.Join(lines, "\n") + "\n" }

func readingLine(ms int, node Node, x, y, z float64) string {
	t := time.Unix(0, int64(ms)*int64(time.Millisecond)).UTC().Format(time.RFC3339Nano)
	return `{"kind":"reading","data":{"t":"` + t + `","node":"` + string(node) + `",` +
		`"ax":` + ftoa(x) + `,"ay":` + ftoa(y) + `,"az":` + ftoa(z) + `}}`
}

func ftoa(f float64) string {
	switch f {
	case 0:
		return "0"
	case 1:
		return "1"
	case 0.5:
		return "0.5"
	}
	return "0.25"
}

func TestParseTraceReadsReadingsAndAcoustics(t *testing.T) {
	src := traceJSONL(
		readingLine(0, NodeAbdoA, 0, 0, 1),
		`{"kind":"acoustic","data":{"t":"1970-01-01T00:00:00.010Z","rms":12.5}}`,
		readingLine(20, NodeRef, 0, 0, 1),
	)
	tr, err := ParseTrace(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Readings) != 2 || len(tr.Acoustics) != 1 {
		t.Fatalf("got %d readings, %d acoustics", len(tr.Readings), len(tr.Acoustics))
	}
	if tr.Acoustics[0].RMS != 12.5 {
		t.Errorf("rms = %v", tr.Acoustics[0].RMS)
	}
}

// Out-of-order timestamps are normal when two nodes are written concurrently.
// The replay clock computes delays from the first sample, so an unsorted trace
// would produce negative offsets and fire a burst of samples at once.
func TestParseTraceSortsByTime(t *testing.T) {
	src := traceJSONL(
		readingLine(50, NodeAbdoA, 0, 0, 1),
		readingLine(10, NodeRef, 0, 0, 1),
		readingLine(30, NodeAbdoA, 0, 0, 1),
	)
	tr, err := ParseTrace(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(tr.Readings); i++ {
		if tr.Readings[i].T.Before(tr.Readings[i-1].T) {
			t.Fatalf("not sorted at %d", i)
		}
	}
}

func TestParseTraceRejectsEmptyAndGarbage(t *testing.T) {
	if _, err := ParseTrace(strings.NewReader("")); err == nil {
		t.Error("an empty trace should be an error, not a source that emits nothing")
	}
	if _, err := ParseTrace(strings.NewReader("not json\n")); err == nil {
		t.Error("expected a parse error")
	}
	if _, err := ParseTrace(strings.NewReader(`{"kind":"mystery","data":{}}` + "\n")); err == nil {
		t.Error("an unknown kind should be an error, not silently dropped")
	}
}

// A trace without a reference node cannot support reference subtraction, so it
// would silently count maternal movement as fetal. Nodes() is what lets the
// caller refuse it.
func TestNodesReportsWhatIsInTheTrace(t *testing.T) {
	src := traceJSONL(
		readingLine(0, NodeAbdoA, 0, 0, 1),
		readingLine(10, NodeRef, 0, 0, 1),
		readingLine(20, NodeAbdoA, 0, 0, 1),
	)
	tr, _ := ParseTrace(strings.NewReader(src))
	got := tr.Nodes()
	if len(got) != 2 {
		t.Fatalf("want 2 distinct nodes, got %v", got)
	}
}

func TestDuration(t *testing.T) {
	src := traceJSONL(
		readingLine(0, NodeAbdoA, 0, 0, 1),
		readingLine(500, NodeAbdoA, 0, 0, 1),
	)
	tr, _ := ParseTrace(strings.NewReader(src))
	if d := tr.Duration(); d != 500*time.Millisecond {
		t.Errorf("duration = %v", d)
	}
}

// Timestamps must be rewritten to now. The detector high-passes against a
// rolling mean and the store buckets by wall clock, so replaying yesterday's
// raw timestamps files every detection under yesterday and the dashboard shows
// an empty night.
func TestReplayRestampsToNow(t *testing.T) {
	src := traceJSONL(
		readingLine(0, NodeAbdoA, 0, 0, 1),
		readingLine(10, NodeRef, 0, 0, 1),
	)
	tr, _ := ParseTrace(strings.NewReader(src))
	s := NewReplaySource(tr, 50, false)
	defer s.Close()

	select {
	case r := <-s.Readings():
		if time.Since(r.T) > 5*time.Second {
			t.Errorf("reading kept its 1970 timestamp: %v", r.T)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no reading")
	}
}

func TestReplayPreservesOrderAndNodes(t *testing.T) {
	src := traceJSONL(
		readingLine(0, NodeAbdoA, 0, 0, 1),
		readingLine(10, NodeRef, 0, 0, 1),
		readingLine(20, NodeAbdoB, 0, 0, 1),
	)
	tr, _ := ParseTrace(strings.NewReader(src))
	s := NewReplaySource(tr, 100, false)
	defer s.Close()

	want := []Node{NodeAbdoA, NodeRef, NodeAbdoB}
	for i, w := range want {
		select {
		case r := <-s.Readings():
			if r.Node != w {
				t.Errorf("sample %d: node %q, want %q", i, r.Node, w)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("sample %d never arrived", i)
		}
	}
}

// Speed must actually compress time, or a 60-second trace takes 60 seconds to
// show anything and is useless as a fallback mid-pitch.
func TestReplaySpeedCompressesTime(t *testing.T) {
	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines, readingLine(i*100, NodeAbdoA, 0, 0, 1))
	}
	tr, _ := ParseTrace(strings.NewReader(traceJSONL(lines...)))
	if d := tr.Duration(); d != 1900*time.Millisecond {
		t.Fatalf("setup: duration %v", d)
	}
	start := time.Now()
	s := NewReplaySource(tr, 20, false) // 1.9s of trace in ~95ms
	defer s.Close()
	for i := 0; i < 20; i++ {
		select {
		case <-s.Readings():
		case <-time.After(3 * time.Second):
			t.Fatalf("only got %d samples", i)
		}
	}
	if el := time.Since(start); el > time.Second {
		t.Errorf("20x speed took %v, expected well under a second", el)
	}
}

func TestReplayLoops(t *testing.T) {
	src := traceJSONL(
		readingLine(0, NodeAbdoA, 0, 0, 1),
		readingLine(10, NodeRef, 0, 0, 1),
	)
	tr, _ := ParseTrace(strings.NewReader(src))
	s := NewReplaySource(tr, 100, true)
	defer s.Close()
	for i := 0; i < 6; i++ { // three times round
		select {
		case <-s.Readings():
		case <-time.After(3 * time.Second):
			t.Fatalf("looping stopped after %d samples", i)
		}
	}
}

func TestReplayCloseIsPromptAndIdempotent(t *testing.T) {
	var lines []string
	for i := 0; i < 500; i++ {
		lines = append(lines, readingLine(i*100, NodeAbdoA, 0, 0, 1))
	}
	tr, _ := ParseTrace(strings.NewReader(traceJSONL(lines...)))
	s := NewReplaySource(tr, 1, true) // 50 seconds of trace, real time
	<-s.Readings()
	done := make(chan struct{})
	go func() { _ = s.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Close blocked waiting for the trace to finish")
	}
	_ = s.Close() // must not panic on a closed channel
}

func TestReplaySourceSatisfiesSource(t *testing.T) {
	var _ Source = (*ReplaySource)(nil)
}

// Round trip: what the writer records must be what the loader reads back,
// or tonight's recording is not tomorrow's fallback.
func TestTraceWriterRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	tw, err := NewTraceWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := tw.WriteReading(Reading{T: now, Node: NodeAbdoA, AX: 0.25, AY: 0.5, AZ: 1}); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteAcoustic(Acoustic{T: now, RMS: 7.5}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	tr, err := LoadTrace(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Readings) != 1 || len(tr.Acoustics) != 1 {
		t.Fatalf("round trip lost data: %d readings, %d acoustics", len(tr.Readings), len(tr.Acoustics))
	}
	r := tr.Readings[0]
	if r.Node != NodeAbdoA || r.AX != 0.25 || r.AZ != 1 {
		t.Errorf("reading changed: %+v", r)
	}
	if !r.T.Equal(now) {
		t.Errorf("timestamp changed: %v vs %v", r.T, now)
	}
}

// The recorder must pass readings through unchanged AND write them, or you
// discover at the demo that the fallback file is empty.
func TestRecorderTeesAndPassesThrough(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rec.jsonl")
	tw, err := NewTraceWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	src := traceJSONL(
		readingLine(0, NodeAbdoA, 0, 0, 1),
		readingLine(10, NodeRef, 0, 0, 1),
		readingLine(20, NodeAbdoB, 0.5, 0, 1),
	)
	tr, _ := ParseTrace(strings.NewReader(src))
	rec := NewRecorder(NewReplaySource(tr, 100, false), tw)

	var got []Node
	deadline := time.After(3 * time.Second)
	for len(got) < 3 {
		select {
		case r, ok := <-rec.Readings():
			if !ok {
				t.Fatalf("channel closed after %d", len(got))
			}
			got = append(got, r.Node)
		case <-deadline:
			t.Fatalf("only %d readings came through", len(got))
		}
	}
	_ = rec.Close()
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	back, err := LoadTrace(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Readings) != 3 {
		t.Fatalf("recorded %d readings, want 3", len(back.Readings))
	}
	if len(back.Nodes()) != 3 {
		t.Errorf("nodes lost in recording: %v", back.Nodes())
	}
}

func TestRecorderCloseIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rec2.jsonl")
	tw, _ := NewTraceWriter(path)
	tr, _ := ParseTrace(strings.NewReader(traceJSONL(readingLine(0, NodeRef, 0, 0, 1))))
	rec := NewRecorder(NewReplaySource(tr, 100, false), tw)
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}
	if err := rec.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	_ = tw.Close()
}

func TestRecorderSatisfiesSource(t *testing.T) {
	var _ Source = (*Recorder)(nil)
}
