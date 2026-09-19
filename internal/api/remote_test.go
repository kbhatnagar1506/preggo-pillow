package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
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

// The landing page is "/" and the dashboard moved to "/dashboard". Getting this
// wrong means a judge opening the URL lands on an operator console instead of
// the product.
func TestLandingAndDashboardRoutes(t *testing.T) {
	s := &Server{Hub: NewHub(), Web: http.Dir("../../web/static"), Owner: "krishnabhatnagar"}
	mux := s.Routes()

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("/ -> %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "Preggo Pillow") {
		t.Error("/ should serve the landing page")
	}
	if strings.Contains(rr.Body.String(), "api/stream") {
		t.Error("/ is serving the dashboard, not the landing page")
	}

	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/dashboard", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("/dashboard -> %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "api/stream") {
		t.Error("/dashboard should serve the operator dashboard")
	}
}

func TestDashboardWithoutAssetsFailsCleanly(t *testing.T) {
	s := &Server{Hub: NewHub()}
	rr := httptest.NewRecorder()
	s.Routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/dashboard", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("no assets -> %d, want 503 rather than a panic", rr.Code)
	}
}

// Every link the landing page offers must resolve, or Get Started is a dead end
// in front of a judge.
func TestLandingLinksResolve(t *testing.T) {
	b, err := os.ReadFile("../../web/static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(b)
	s := &Server{Hub: NewHub(), Web: http.Dir("../../web/static"), Owner: "krishnabhatnagar"}
	mux := s.Routes()

	re := regexp.MustCompile(`href="(/[^"#]*)"`)
	seen := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(page, -1) {
		href := m[1]
		if seen[href] {
			continue
		}
		seen[href] = true
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, href, nil))
		if rr.Code != http.StatusOK {
			t.Errorf("landing page links to %s which returns %d", href, rr.Code)
		}
	}
	if len(seen) == 0 {
		t.Error("no internal links found; the CTAs are probably broken")
	}
}

// The standalone Vercel copy must not ship unreplaced placeholders, and must
// stay in step with the embedded one.
func TestStandaloneSiteCopy(t *testing.T) {
	b, err := os.ReadFile("../../site/index.html")
	if err != nil {
		t.Skip("no standalone site directory")
	}
	site := string(b)
	if !strings.Contains(site, "{{APP_URL}}") {
		t.Error("the standalone copy should template its app links, not hard-code localhost")
	}
	if !strings.Contains(site, "Your Comfort,") {
		t.Error("the standalone copy has drifted from the embedded landing page")
	}
}
