package sensor

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPhoneBaseURLDefaultsAndTrimming(t *testing.T) {
	cases := []struct{ in, want string }{
		{"192.168.1.23", "http://192.168.1.23"},
		{"192.168.1.23:8080", "http://192.168.1.23:8080"},
		{"http://192.168.1.23/", "http://192.168.1.23"},
		{"https://10.0.0.5:8080/", "http://10.0.0.5:8080"},
	}
	for _, c := range cases {
		if got := (Phone{Host: c.in}).BaseURL(); got != c.want {
			t.Errorf("BaseURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParsePhyphoxHappyPath(t *testing.T) {
	body := []byte(`{"buffer":{
		"accX":{"buffer":[0.1,0.2]},
		"accY":{"buffer":[1.0,1.1]},
		"accZ":{"buffer":[9.8,9.7]},
		"acc_time":{"buffer":[1.00,1.01]}},
		"status":{"measuring":true}}`)
	xs, ys, zs, ts, measuring, err := parsePhyphox(body)
	if err != nil {
		t.Fatal(err)
	}
	if !measuring {
		t.Error("measuring should be true")
	}
	if len(xs) != 2 || len(ys) != 2 || len(zs) != 2 || len(ts) != 2 {
		t.Fatalf("lengths: %d %d %d %d", len(xs), len(ys), len(zs), len(ts))
	}
	if xs[1] != 0.2 || zs[0] != 9.8 || ts[1] != 1.01 {
		t.Errorf("wrong values: %v %v %v", xs, zs, ts)
	}
}

// A poll landing mid-write can return one more sample on some buffers than
// others. Zipping those without truncating pairs the wrong axes together, which
// would corrupt every reading rather than dropping one.
func TestParsePhyphoxTruncatesToShortestBuffer(t *testing.T) {
	body := []byte(`{"buffer":{
		"accX":{"buffer":[0.1,0.2,0.3]},
		"accY":{"buffer":[1.0,1.1]},
		"accZ":{"buffer":[9.8,9.7,9.6]},
		"acc_time":{"buffer":[1.00,1.01,1.02]}},
		"status":{"measuring":true}}`)
	xs, ys, zs, ts, _, err := parsePhyphox(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(xs) != 2 || len(ys) != 2 || len(zs) != 2 || len(ts) != 2 {
		t.Fatalf("expected all buffers truncated to 2, got %d %d %d %d",
			len(xs), len(ys), len(zs), len(ts))
	}
}

// phyphox emits null for gaps. A null must be dropped, never read as 0.0 —
// a spurious zero in an axis is a 1g step change and the detector would call it
// a kick.
func TestParsePhyphoxDropsNullsRatherThanZeroing(t *testing.T) {
	body := []byte(`{"buffer":{
		"accX":{"buffer":[0.1,null,0.3]},
		"accY":{"buffer":[1.0,1.1,1.2]},
		"accZ":{"buffer":[9.8,9.7,9.6]},
		"acc_time":{"buffer":[1.00,1.01,1.02]}},
		"status":{"measuring":true}}`)
	xs, _, _, _, _, err := parsePhyphox(body)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range xs {
		if v == 0 {
			t.Fatalf("a null became a zero: %v", xs)
		}
	}
	if len(xs) != 2 {
		t.Fatalf("expected the null dropped, got %v", xs)
	}
}

func TestParsePhyphoxReportsPaused(t *testing.T) {
	body := []byte(`{"buffer":{},"status":{"measuring":false}}`)
	_, _, _, _, measuring, err := parsePhyphox(body)
	if err != nil {
		t.Fatal(err)
	}
	if measuring {
		t.Error("measuring should be false")
	}
}

func TestParsePhyphoxRejectsGarbage(t *testing.T) {
	if _, _, _, _, _, err := parsePhyphox([]byte("<html>404</html>")); err == nil {
		t.Error("expected an error on non-JSON")
	}
}

func TestParsePhyphoxMissingBuffers(t *testing.T) {
	body := []byte(`{"buffer":{"accX":{"buffer":[0.1]}},"status":{"measuring":true}}`)
	xs, ys, zs, ts, _, err := parsePhyphox(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(xs)+len(ys)+len(zs)+len(ts) != 0 {
		t.Errorf("a response missing accY/accZ/acc_time must yield nothing, got %d %d %d %d",
			len(xs), len(ys), len(zs), len(ts))
	}
}

// phyphox reports m/s^2; everything downstream is in g. Getting this wrong
// scales every threshold in the detector by 9.8.
func TestPhoneSourceConvertsToG(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"buffer":{
			"accX":{"buffer":[0]},
			"accY":{"buffer":[0]},
			"accZ":{"buffer":[9.80665]},
			"acc_time":{"buffer":[1.0]}},
			"status":{"measuring":true}}`)
	}))
	defer srv.Close()

	s := NewPhoneSource([]Phone{{Node: NodeAbdoA, Host: strings.TrimPrefix(srv.URL, "http://")}}, 50)
	defer s.Close()

	select {
	case r := <-s.Readings():
		if math.Abs(r.AZ-1.0) > 1e-6 {
			t.Errorf("9.80665 m/s^2 should be 1.0 g, got %v", r.AZ)
		}
		if r.Node != NodeAbdoA {
			t.Errorf("node = %q", r.Node)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no reading arrived")
	}
}

// The threshold query is what makes polling incremental. If lastT never
// advances, every poll re-reads the whole buffer and every sample is counted
// again — the kick count would climb on its own with nobody touching it.
func TestPhoneSourceAdvancesTheTimeThreshold(t *testing.T) {
	var seen atomic.Value
	seen.Store([]string{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.RawQuery
		cur := seen.Load().([]string)
		seen.Store(append(append([]string{}, cur...), q))
		fmt.Fprint(w, `{"buffer":{
			"accX":{"buffer":[0.1]},
			"accY":{"buffer":[0.1]},
			"accZ":{"buffer":[9.8]},
			"acc_time":{"buffer":[42.5]}},
			"status":{"measuring":true}}`)
	}))
	defer srv.Close()

	s := NewPhoneSource([]Phone{{Node: NodeRef, Host: strings.TrimPrefix(srv.URL, "http://")}}, 50)
	defer s.Close()
	<-s.Readings()
	time.Sleep(200 * time.Millisecond)

	qs := seen.Load().([]string)
	if len(qs) < 2 {
		t.Fatalf("expected repeated polls, got %d", len(qs))
	}
	if !strings.Contains(qs[0], "acc_time=0.000000|acc_time") {
		t.Errorf("first poll should start at 0, got %q", qs[0])
	}
	last := qs[len(qs)-1]
	if !strings.Contains(last, "42.500000|acc_time") {
		t.Errorf("later polls should ask for samples after 42.5, got %q", last)
	}
	if strings.Contains(last, "%7C") {
		t.Errorf("the pipe must not be percent-escaped: %q", last)
	}
}

// A phone dropping off Wi-Fi must be visible, not silent. Silently reading
// fewer nodes is the failure that makes a low kick count look like a sick baby.
func TestPhoneSourceSurfacesErrors(t *testing.T) {
	s := NewPhoneSource([]Phone{{Node: NodeAbdoB, Host: "127.0.0.1:1"}}, 50)
	defer s.Close()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if msg, ok := s.Errors()[NodeAbdoB]; ok && msg != "" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("an unreachable phone produced no error")
}

func TestPhoneSourceReportsPausedPhyphox(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"buffer":{},"status":{"measuring":false}}`)
	}))
	defer srv.Close()

	s := NewPhoneSource([]Phone{{Node: NodeAbdoA, Host: strings.TrimPrefix(srv.URL, "http://")}}, 50)
	defer s.Close()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if msg := s.Errors()[NodeAbdoA]; strings.Contains(msg, "paused") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("a paused phyphox produced no 'paused' message")
}

// Sample spacing inside one batch has to survive. The detector high-passes, so
// collapsing a 100 Hz batch onto a single instant would flatten real motion.
func TestPhoneSourcePreservesIntraBatchSpacing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"buffer":{
			"accX":{"buffer":[0,0,0]},
			"accY":{"buffer":[0,0,0]},
			"accZ":{"buffer":[9.8,9.8,9.8]},
			"acc_time":{"buffer":[10.00,10.01,10.02]}},
			"status":{"measuring":true}}`)
	}))
	defer srv.Close()

	s := NewPhoneSource([]Phone{{Node: NodeAbdoA, Host: strings.TrimPrefix(srv.URL, "http://")}}, 20)
	defer s.Close()

	var got []time.Time
	for len(got) < 3 {
		select {
		case r := <-s.Readings():
			got = append(got, r.T)
		case <-time.After(3 * time.Second):
			t.Fatal("not enough readings")
		}
	}
	d1 := got[1].Sub(got[0])
	d2 := got[2].Sub(got[1])
	for _, d := range []time.Duration{d1, d2} {
		if d < 8*time.Millisecond || d > 12*time.Millisecond {
			t.Errorf("expected ~10ms spacing, got %v and %v", d1, d2)
		}
	}
}

func TestPhoneSourceSatisfiesSourceInterface(t *testing.T) {
	var _ Source = (*PhoneSource)(nil)
}

func TestPhyphoxResponseShapeMatchesDocumentedAPI(t *testing.T) {
	// The exact example from phyphox's REST docs must decode.
	doc := `{"buffer":{"accX":{"size":100,"updateMode":"single","buffer":[9.81]},
		"accY":{"size":100,"updateMode":"single","buffer":[0.15]},
		"accZ":{"size":100,"updateMode":"full","buffer":[0.02,0.03,0.04]},
		"acc_time":{"size":100,"updateMode":"partial","buffer":[1.0,2.0,3.0]}},
		"status":{"session":"abcdef123","measuring":true,"timedRun":false,"countDown":0}}`
	var r phyphoxResponse
	if err := json.Unmarshal([]byte(doc), &r); err != nil {
		t.Fatalf("the documented response shape must decode: %v", err)
	}
	if !r.Status.Measuring || len(r.Buffer["accZ"].Buffer) != 3 {
		t.Error("decoded the documented example incorrectly")
	}
}
