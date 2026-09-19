package sensor

// Replay plays a recorded trace back as a live sensor source.
//
// This exists for one reason: hardware fails in front of judges. A Grove cable
// works loose, a HAT stops answering, a board does not come back from a
// reboot — all of which happened while building this. A demo that dies at the
// table cannot be talked around, but a demo that keeps running on last night's
// real recording can.
//
// It is NOT a simulator. The simulator invents a signal from a model; this
// replays accelerometer samples that were actually measured, through the same
// detector, at the same rate. The numbers on screen are numbers that really
// happened — which is the difference between a fallback and a fake.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"sync"
	"time"
)

// Trace is a recorded run.
type Trace struct {
	Readings  []Reading
	Acoustics []Acoustic
}

// LoadTrace reads a JSONL trace: one JSON object per line, each tagged with a
// kind so readings and acoustics can share a file and stay interleaved in the
// order they were captured.
func LoadTrace(path string) (*Trace, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseTrace(f)
}

type traceLine struct {
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data"`
}

// ParseTrace reads a trace from any reader, so tests need no files.
func ParseTrace(r io.Reader) (*Trace, error) {
	t := &Trace{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	line := 0
	for sc.Scan() {
		line++
		b := sc.Bytes()
		if len(b) == 0 {
			continue
		}
		var tl traceLine
		if err := json.Unmarshal(b, &tl); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		switch tl.Kind {
		case "reading":
			var rd Reading
			if err := json.Unmarshal(tl.Data, &rd); err != nil {
				return nil, fmt.Errorf("line %d: %w", line, err)
			}
			t.Readings = append(t.Readings, rd)
		case "acoustic":
			var ac Acoustic
			if err := json.Unmarshal(tl.Data, &ac); err != nil {
				return nil, fmt.Errorf("line %d: %w", line, err)
			}
			t.Acoustics = append(t.Acoustics, ac)
		default:
			return nil, fmt.Errorf("line %d: unknown kind %q", line, tl.Kind)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(t.Readings) == 0 {
		return nil, fmt.Errorf("trace contains no readings")
	}
	// Timestamps must be ordered for the replay clock to make sense; a trace
	// written from concurrent nodes can arrive slightly out of order.
	sort.SliceStable(t.Readings, func(i, j int) bool { return t.Readings[i].T.Before(t.Readings[j].T) })
	sort.SliceStable(t.Acoustics, func(i, j int) bool { return t.Acoustics[i].T.Before(t.Acoustics[j].T) })
	return t, nil
}

// Duration is the wall time the trace covers.
func (t *Trace) Duration() time.Duration {
	if len(t.Readings) < 2 {
		return 0
	}
	return t.Readings[len(t.Readings)-1].T.Sub(t.Readings[0].T)
}

// Nodes lists which body positions the trace contains, so a trace recorded
// without a reference node can be rejected before it silently produces
// detections that include maternal movement.
func (t *Trace) Nodes() []Node {
	seen := map[Node]bool{}
	var out []Node
	for _, r := range t.Readings {
		if !seen[r.Node] {
			seen[r.Node] = true
			out = append(out, r.Node)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// ReplaySource emits a trace as if it were happening now.
type ReplaySource struct {
	readings chan Reading
	acoustic chan Acoustic
	stop     chan struct{}
	once     sync.Once
	wg       sync.WaitGroup
}

// NewReplaySource starts replaying. speed scales playback: 1 is real time, 60
// compresses an hour into a minute. loop restarts at the end, so a demo can run
// unattended for as long as anyone wants to look at it.
//
// Timestamps are rewritten to now. The detector high-passes against a rolling
// mean and the store buckets by wall clock, so replaying yesterday's raw
// timestamps would put every detection in yesterday's night and show nothing
// on the dashboard.
func NewReplaySource(t *Trace, speed float64, loop bool) *ReplaySource {
	if speed <= 0 {
		speed = 1
	}
	s := &ReplaySource{
		readings: make(chan Reading, 4096),
		acoustic: make(chan Acoustic, 512),
		stop:     make(chan struct{}),
	}
	s.wg.Add(1)
	go s.run(t, speed, loop)
	return s
}

func (s *ReplaySource) Readings() <-chan Reading   { return s.readings }
func (s *ReplaySource) Acoustics() <-chan Acoustic { return s.acoustic }

// Close is idempotent: everything runs inside the Once, because closing an
// already-closed channel panics and main defers Close on a source that other
// paths may also close.
func (s *ReplaySource) Close() error {
	s.once.Do(func() {
		close(s.stop)
		s.wg.Wait()
		close(s.readings)
		close(s.acoustic)
	})
	return nil
}

func (s *ReplaySource) run(t *Trace, speed float64, loop bool) {
	defer s.wg.Done()
	for {
		if !s.playOnce(t, speed) {
			return
		}
		if !loop {
			return
		}
	}
}

// playOnce returns false if it was stopped partway.
func (s *ReplaySource) playOnce(t *Trace, speed float64) bool {
	base := t.Readings[0].T
	wallStart := time.Now()
	ai := 0

	for _, r := range t.Readings {
		offset := time.Duration(float64(r.T.Sub(base)) / speed)
		due := wallStart.Add(offset)

		if d := time.Until(due); d > 0 {
			select {
			case <-s.stop:
				return false
			case <-time.After(d):
			}
		} else {
			select {
			case <-s.stop:
				return false
			default:
			}
		}

		// Any acoustic samples that belong before this reading go first, so the
		// detector's acoustic gate sees them in the order they were recorded.
		for ai < len(t.Acoustics) && !t.Acoustics[ai].T.After(r.T) {
			a := t.Acoustics[ai]
			a.T = wallStart.Add(time.Duration(float64(a.T.Sub(base)) / speed))
			select {
			case s.acoustic <- a:
			default:
			}
			ai++
		}

		out := r
		out.T = due
		select {
		case s.readings <- out:
		default: // never block the clock on a slow consumer
		}
	}
	return true
}

// TraceWriter records a live source to a JSONL file so a real night can become
// tomorrow's fallback.
type TraceWriter struct {
	mu sync.Mutex
	f  *os.File
	w  *bufio.Writer
}

func NewTraceWriter(path string) (*TraceWriter, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &TraceWriter{f: f, w: bufio.NewWriterSize(f, 128*1024)}, nil
}

func (tw *TraceWriter) WriteReading(r Reading) error   { return tw.write("reading", r) }
func (tw *TraceWriter) WriteAcoustic(a Acoustic) error { return tw.write("acoustic", a) }

func (tw *TraceWriter) write(kind string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tw.mu.Lock()
	defer tw.mu.Unlock()
	_, err = fmt.Fprintf(tw.w, `{"kind":%q,"data":%s}`+"\n", kind, data)
	return err
}

func (tw *TraceWriter) Close() error {
	tw.mu.Lock()
	defer tw.mu.Unlock()
	if err := tw.w.Flush(); err != nil {
		_ = tw.f.Close()
		return err
	}
	return tw.f.Close()
}

// Recorder tees a live source to a trace file while passing it through
// untouched. Wrapping the Source rather than teaching each source to record
// means sim, serial, phone and i2c all get it from one flag.
type Recorder struct {
	inner    Source
	tw       *TraceWriter
	readings chan Reading
	acoustic chan Acoustic
	once     sync.Once
	wg       sync.WaitGroup
}

func NewRecorder(inner Source, tw *TraceWriter) *Recorder {
	r := &Recorder{
		inner:    inner,
		tw:       tw,
		readings: make(chan Reading, 4096),
		acoustic: make(chan Acoustic, 512),
	}
	r.wg.Add(2)
	go func() {
		defer r.wg.Done()
		for v := range inner.Readings() {
			_ = tw.WriteReading(v)
			select {
			case r.readings <- v:
			default:
			}
		}
		close(r.readings)
	}()
	go func() {
		defer r.wg.Done()
		for v := range inner.Acoustics() {
			_ = tw.WriteAcoustic(v)
			select {
			case r.acoustic <- v:
			default:
			}
		}
		close(r.acoustic)
	}()
	return r
}

func (r *Recorder) Readings() <-chan Reading   { return r.readings }
func (r *Recorder) Acoustics() <-chan Acoustic { return r.acoustic }

func (r *Recorder) Close() error {
	var err error
	r.once.Do(func() {
		err = r.inner.Close() // closing the inner source ends both pumps
		r.wg.Wait()
	})
	return err
}
