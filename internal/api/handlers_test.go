package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kbhatnagar1506/lull/internal/store"
)

// syncRecorder is a ResponseWriter the test can read while the handler writes.
// httptest.ResponseRecorder is not safe for concurrent access, and an SSE
// handler by definition keeps writing after the test starts looking.
type syncRecorder struct {
	mu   sync.Mutex
	hdr  http.Header
	body strings.Builder
	code int
}

func newSyncRecorder() *syncRecorder {
	return &syncRecorder{hdr: http.Header{}, code: http.StatusOK}
}
func (r *syncRecorder) Header() http.Header { return r.hdr }
func (r *syncRecorder) WriteHeader(c int)   { r.mu.Lock(); r.code = c; r.mu.Unlock() }
func (r *syncRecorder) Write(b []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body.Write(b)
}
func (r *syncRecorder) Flush() {}
func (r *syncRecorder) text() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body.String()
}

// The SSE stream is the dashboard's lifeline: if it does not set the right
// headers, or does not flush, the page sits at zero all night while the
// detector works perfectly.
//
// The handler is called directly rather than through httptest.Server, because
// Close() there waits for the handler to return and the handler only notices a
// vanished client at its next 20-second keepalive. That is correct in
// production and a 20-second test otherwise.
func TestStreamSetsSSEHeadersAndFlushesEvents(t *testing.T) {
	s := &Server{Hub: NewHub()}
	rec := newSyncRecorder()
	if _, ok := any(rec).(http.Flusher); !ok {
		t.Fatal("recorder cannot flush; this test would prove nothing")
	}

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/stream", nil).WithContext(ctx)

	done := make(chan struct{})
	go func() { s.handleStream(rec, req); close(done) }()

	// Publish until something lands: a single broadcast can race subscription.
	deadline := time.After(4 * time.Second)
	delivered := false
	for !delivered {
		s.Hub.Broadcast(Event{Kind: "detection", Data: map[string]any{"residual": 0.42}})
		time.Sleep(25 * time.Millisecond)
		if strings.Contains(rec.text(), "detection") {
			delivered = true
			break
		}
		select {
		case <-deadline:
			cancel()
			<-done
			t.Fatal("no event reached the stream")
		default:
		}
	}
	cancel()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("handler ignored context cancellation; every closed tab would leak a goroutine")
	}

	body := rec.text()
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Errorf("content-type = %q; browsers will not treat that as SSE", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
		t.Errorf("cache-control = %q; a cached stream never delivers anything", cc)
	}
	if !strings.Contains(body, "data: ") {
		t.Errorf("events are not in SSE frame format: %.120s", body)
	}
	if !strings.Contains(body, "0.42") {
		t.Errorf("event lost its payload: %.120s", body)
	}
}

// Fool injects maternal movement on every node. With no injector wired it must
// answer cleanly rather than 500 — on real hardware there is nothing to fake.
func TestFoolWithoutAnInjector(t *testing.T) {
	s := &Server{Hub: NewHub()}
	rr := httptest.NewRecorder()
	s.Routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/fool", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("/api/fool -> %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"ok":true`) {
		t.Errorf("body = %s", rr.Body.String())
	}
}

func TestFoolCallsTheInjector(t *testing.T) {
	var called atomic.Int32
	s := &Server{Hub: NewHub(), Fool: func() { called.Add(1) }}
	rr := httptest.NewRecorder()
	s.Routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/fool", nil))
	if called.Load() != 1 {
		t.Errorf("injector called %d times, want 1", called.Load())
	}
}

func TestProfileAndAppointmentAcceptInput(t *testing.T) {
	s := &Server{Hub: NewHub()}
	for _, c := range []struct{ path, body string }{
		{"/api/profile", `{"name":"Test","weeks":31,"due_date":"2026-11-20"}`},
		{"/api/appointment", `{"when":"Friday 09:30","what":"midwife"}`},
	} {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, c.path, strings.NewReader(c.body))
		req.Header.Set("Content-Type", "application/json")
		s.Routes().ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("%s -> %d: %s", c.path, rr.Code, rr.Body.String())
		}
	}
}

// Both take free text from the page; malformed JSON must not take the server
// down mid-demo.
func TestProfileAndAppointmentSurviveGarbage(t *testing.T) {
	s := &Server{Hub: NewHub()}
	for _, path := range []string{"/api/profile", "/api/appointment"} {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("}{not json"))
		s.Routes().ServeHTTP(rr, req)
		if rr.Code >= 500 {
			t.Errorf("%s -> %d on garbage; it should degrade, not fail", path, rr.Code)
		}
	}
}

// str and f64 unwrap values that arrive as `any` from the maternal map. A wrong
// type must yield a zero, not a panic, because the report renders from them.
func TestAnyCoercionIsTotal(t *testing.T) {
	if got := str("hello"); got != "hello" {
		t.Errorf("str(string) = %q", got)
	}
	for _, v := range []any{nil, 42, 3.5, true, []string{"x"}} {
		if got := str(v); got != "" {
			t.Errorf("str(%v) = %q, want empty", v, got)
		}
	}
	if got := f64(3.5); got != 3.5 {
		t.Errorf("f64(float64) = %v", got)
	}
	if got := f64(7); got != 7 {
		t.Errorf("f64(int) = %v", got)
	}
	for _, v := range []any{nil, "nope", true} {
		if got := f64(v); got != 0 {
			t.Errorf("f64(%v) = %v, want 0", v, got)
		}
	}
}

func TestNightsEndpointShape(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := &Server{Hub: NewHub(), Store: st}
	rr := httptest.NewRecorder()
	s.Routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/nights", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("/api/nights -> %d: %s", rr.Code, rr.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if _, ok := out["nights"]; !ok {
		t.Errorf("payload has no nights: %v", out)
	}
}
