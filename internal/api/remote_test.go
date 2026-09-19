package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kbhatnagar1506/lull/internal/voice"
)

func TestRemoteServedAtOwnerPath(t *testing.T) {
	s := &Server{Hub: NewHub(), Owner: "krishnabhatnagar"}
	mux := s.Routes()
	for _, path := range []string{"/krishnabhatnagar", "/krishnabhatnagar/"} {
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusOK {
			t.Errorf("%s -> %d", path, rr.Code)
		}
		if !strings.Contains(rr.Body.String(), "Lull") {
			t.Errorf("%s did not serve the remote", path)
		}
	}
}

func TestRemoteHasEveryControl(t *testing.T) {
	for _, want := range []string{
		`data-kick="weak"`, `data-kick="medium"`, `data-kick="strong"`,
		`id="shake"`, `id="felt-btn"`, `id="beat"`, `id="call"`,
		"/api/kick?strength=", "/api/fool", "/api/press?source=remote", "/api/call",
		"/api/stream",
	} {
		if !strings.Contains(remotePage, want) {
			t.Errorf("remote page is missing %q", want)
		}
	}
}

// The fetal range is 110-160 bpm. An adult rate would not read as a baby to
// anyone who has heard a scan, which is the entire point of the sound.
func TestHeartbeatIsInTheFetalRange(t *testing.T) {
	if !strings.Contains(remotePage, "var BPM = 142") {
		t.Error("heartbeat rate is not set to a fetal rate")
	}
	if !strings.Contains(remotePage, "S2") || !strings.Contains(remotePage, "0.32") {
		t.Error("the beat should be a two-sound lub-dub, not a single evenly spaced thump")
	}
}

// A real phone call must not fire on a stray tap.
func TestCallNeedsTwoTaps(t *testing.T) {
	if !strings.Contains(remotePage, "Tap again to call") {
		t.Error("the call button does not arm before firing")
	}
}

// The count on the remote must come from detections, never from commands, or a
// judge could inflate it by pressing buttons.
func TestRemoteCountsDetectionsOnly(t *testing.T) {
	i := strings.Index(remotePage, `e.kind === "detection"`)
	if i < 0 {
		t.Fatal("no detection handler")
	}
	seg := remotePage[i : i+120]
	if !strings.Contains(seg, "detected++") {
		t.Error("the headline number is not driven by detections")
	}
	j := strings.Index(remotePage, `e.kind === "command"`)
	if j < 0 || !strings.Contains(remotePage[j:j+120], "fired++") {
		t.Error("commands should feed the 'fired' tally, not the headline count")
	}
}

func TestCallRejectsGET(t *testing.T) {
	s := &Server{Hub: NewHub(), Voice: voice.New("k", "pn", ""), CallTo: "+15551234567"}
	rr := httptest.NewRecorder()
	s.Routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/call", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /api/call -> %d; a crawler or link preview must not be able to dial", rr.Code)
	}
}

func TestCallReportsWhenUnconfigured(t *testing.T) {
	s := &Server{Hub: NewHub()}
	rr := httptest.NewRecorder()
	s.Routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/call", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("unconfigured -> %d, want 503", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "VAPI_API_KEY") {
		t.Error("the error should say what is missing")
	}
}

func TestCallNeedsANumber(t *testing.T) {
	s := &Server{Hub: NewHub(), Voice: voice.New("k", "pn", "")}
	rr := httptest.NewRecorder()
	s.Routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/call", nil))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("no number -> %d, want 400", rr.Code)
	}
}

func TestNoRemoteWhenOwnerUnset(t *testing.T) {
	s := &Server{Hub: NewHub()}
	rr := httptest.NewRecorder()
	s.Routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/krishnabhatnagar", nil))
	if rr.Code == http.StatusOK && strings.Contains(rr.Body.String(), "Lull &middot; remote") {
		t.Error("the remote should not be served when no owner is configured")
	}
}
