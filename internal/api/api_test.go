package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kbhatnagar1506/lull/internal/kicker"
	"github.com/kbhatnagar1506/lull/internal/store"
)

type nopServo struct{ n int }

func (s *nopServo) Kick(string, float64) error { s.n++; return nil }

// newServer builds the API with NO Backboard, NO Tiger and NO Gemini, which is
// exactly how Lull runs at a venue with no network. Every endpoint must still
// answer.
func newServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	srv := &Server{
		Hub:    NewHub(),
		Store:  st,
		Kicker: kicker.New(&nopServo{}, func(tm time.Time, s string) { _ = st.InsertCommand(tm, s) }),
		Web:    http.Dir(t.TempDir()),
	}
	return srv, st
}

func do(t *testing.T, srv *Server, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, httptest.NewRequest(method, path, nil))
	return w
}

func TestNightsEndpointOnEmptyDatabase(t *testing.T) {
	srv, _ := newServer(t)
	w := do(t, srv, http.MethodGet, "/api/nights")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["alert"] != false {
		t.Error("an empty database must not raise an alert")
	}
}

// The alert is the demo's whole payload. Two consecutive nights below baseline,
// never one, because fetal sleep cycles run 20-40 minutes and babies have quiet
// nights.
func TestAlertNeedsTwoConsecutiveLowNights(t *testing.T) {
	srv, st := newServer(t)
	day := func(n int) string { return time.Now().AddDate(0, 0, -n).Format("2006-01-02") }

	for i := 6; i >= 2; i-- {
		if err := st.UpsertNight(day(i), 340, true); err != nil {
			t.Fatal(err)
		}
	}
	// Only tonight is low.
	if err := st.UpsertNight(day(1), 340, true); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertNight(day(0), 198, true); err != nil {
		t.Fatal(err)
	}

	var got map[string]any
	_ = json.Unmarshal(do(t, srv, http.MethodGet, "/api/nights").Body.Bytes(), &got)
	if got["alert"] == true {
		t.Error("one low night raised an alert; it must take two consecutive")
	}

	// Now make last night low as well.
	if err := st.UpsertNight(day(1), 241, true); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(do(t, srv, http.MethodGet, "/api/nights").Body.Bytes(), &got)
	if got["alert"] != true {
		t.Errorf("two consecutive low nights did not raise an alert: %v", got)
	}
}

func TestKickEndpointDrivesTheServoAndLogsACommand(t *testing.T) {
	srv, st := newServer(t)
	if w := do(t, srv, http.MethodPost, "/api/kick?strength=weak"); w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	rows, err := st.UnsyncedDetections(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Error("firing the servo created a DETECTION; the count must come from sensors only")
	}
	ev, err := st.EventsWindow(time.Now().Add(-time.Minute), time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	var commands int
	for _, e := range ev {
		if e.Kind == "command" {
			commands++
		}
	}
	if commands != 1 {
		t.Errorf("logged %d commands, want 1", commands)
	}
}

func TestPressIsRecorded(t *testing.T) {
	srv, st := newServer(t)
	if w := do(t, srv, http.MethodPost, "/api/press?source=judge"); w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	ev, _ := st.EventsWindow(time.Now().Add(-time.Minute), time.Now().Add(time.Minute))
	var presses int
	for _, e := range ev {
		if e.Kind == "press" {
			presses++
		}
	}
	if presses != 1 {
		t.Errorf("recorded %d presses, want 1", presses)
	}
}

func TestBlindScoreBeforeAnyRunIsARequestError(t *testing.T) {
	srv, _ := newServer(t)
	if w := do(t, srv, http.MethodGet, "/api/blind/score"); w.Code != http.StatusBadRequest {
		t.Errorf("status %d, want 400", w.Code)
	}
}

func TestBlindStartStopThenScore(t *testing.T) {
	srv, _ := newServer(t)
	do(t, srv, http.MethodPost, "/api/blind/start")
	do(t, srv, http.MethodPost, "/api/kick?strength=medium")
	do(t, srv, http.MethodPost, "/api/press?source=judge")
	do(t, srv, http.MethodPost, "/api/blind/stop")

	w := do(t, srv, http.MethodGet, "/api/blind/score")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		Score  store.Score   `json:"score"`
		Events []store.Event `json:"events"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Score.Commands != 1 {
		t.Errorf("commands %d, want 1", got.Score.Commands)
	}
	if len(got.Events) == 0 {
		t.Error("no events returned; the reveal overlay would be blank")
	}
}

// Backboard is optional. With no client the endpoints must answer clearly
// rather than 500.
func TestMemoryEndpointsDegradeWithoutBackboard(t *testing.T) {
	srv, _ := newServer(t)

	w := do(t, srv, http.MethodGet, "/api/memories")
	if w.Code != http.StatusOK {
		t.Fatalf("memories status %d", w.Code)
	}
	var mem map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &mem)
	if mem["enabled"] != false {
		t.Errorf("expected enabled:false, got %v", mem)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/ask", strings.NewReader(`{"question":"is he ok"}`))
	aw := httptest.NewRecorder()
	srv.Routes().ServeHTTP(aw, req)
	if aw.Code != http.StatusOK {
		t.Fatalf("ask status %d", aw.Code)
	}
	var ask map[string]any
	_ = json.Unmarshal(aw.Body.Bytes(), &ask)
	if s, _ := ask["answer"].(string); s == "" {
		t.Error("ask returned no answer text when memory is unconfigured")
	}
}

func TestNoteRequiresText(t *testing.T) {
	srv, _ := newServer(t)
	if w := do(t, srv, http.MethodPost, "/api/note"); w.Code != http.StatusBadRequest {
		t.Errorf("status %d, want 400", w.Code)
	}
}

func TestMaternalEndpointAnswersBeforeAnySensorData(t *testing.T) {
	srv, _ := newServer(t)
	w := do(t, srv, http.MethodGet, "/api/maternal")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["ready"] != false {
		t.Errorf("expected ready:false with no sensor data, got %v", got)
	}
}

// The report is a Must-Have deliverable and must render with no LLM attached.
func TestReportRendersWithoutGemini(t *testing.T) {
	srv, st := newServer(t)
	for i := 3; i >= 0; i-- {
		day := time.Now().AddDate(0, 0, -i).Format("2006-01-02")
		if err := st.UpsertNight(day, 300, true); err != nil {
			t.Fatal(err)
		}
	}
	w := do(t, srv, http.MethodGet, "/report")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "not a medical device") {
		t.Error("report is missing the not-a-medical-device disclaimer")
	}
	if !strings.Contains(body, "never reports that the baby is fine") {
		t.Error("report is missing the no-reassurance statement")
	}
}

// A slow browser must drop frames rather than stall the detector.
func TestHubBroadcastNeverBlocks(t *testing.T) {
	h := NewHub()
	ch := h.subscribe()
	defer h.unsubscribe(ch)
	for i := 0; i < 1000; i++ {
		h.Broadcast(Event{Kind: "trace", Data: i})
	}
}
