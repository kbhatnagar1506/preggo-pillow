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
		if !strings.Contains(rr.Body.String(), "Preggo Pillow") {
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
// One tap calls. The arm-then-confirm gesture was removed: iOS swallowed the
// second tap as a zoom, and a button that needs hitting twice fails in front
// of an audience. What must survive is the re-entrancy guard, so a jittery
// thumb cannot place two calls.
func TestCallFiresOnOneTapButNotTwice(t *testing.T) {
	if strings.Contains(remotePage, "Tap again to call") {
		t.Error("the call button still requires a second tap")
	}
	for _, want := range []string{"var calling = false", "if (calling)", `post("/api/call"`} {
		if !strings.Contains(remotePage, want) {
			t.Errorf("the call button is missing %q", want)
		}
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
	embedded, err := os.ReadFile("../../web/static/index.html")
	if err != nil {
		t.Fatalf("read embedded landing page: %v", err)
	}
	// Byte-identical, deliberately. The standalone copy used to carry
	// {{APP_URL}} placeholders that a documented sed step was supposed to
	// replace before deploying; nothing ever ran it, so the live Get Started
	// buttons pointed at a literal "{{APP_URL}}/dashboard". Where the app
	// lives is deployment configuration and now lives in site/vercel.json as
	// redirects, leaving one file with one set of links.
	if string(b) != string(embedded) {
		t.Error("site/index.html has drifted from web/static/index.html; regenerate it with `make site`")
	}
	if strings.Contains(string(b), "{{") {
		t.Error("the standalone copy still carries an unsubstituted template placeholder")
	}
}

// The nav must match the real app's, or the shell is a different product from
// the one the team built.
func TestDashboardHasTheRealNav(t *testing.T) {
	b, err := os.ReadFile("../../web/static/dashboard.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(b)
	for _, want := range []string{
		`href="/dashboard"`, `href="/history"`, `href="/healthcare"`,
		`href="/medications"`, `href="/settings"`,
		"Live from the pillow", `id="live-badge"`, `href="/auth/logout"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("dashboard is missing %q", want)
		}
	}
}

// Lull has no fetal heart-rate sensor and no accelerometer or camera can read
// one through the abdominal wall. The tile must say so rather than show a
// number, because a fabricated vital sign on a pregnancy monitor is the worst
// possible thing to ship.
func TestDashboardDoesNotClaimFetalHeartRate(t *testing.T) {
	b, err := os.ReadFile("../../web/static/dashboard.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(b)
	i := strings.Index(page, `id="v-fhr"`)
	if i < 0 {
		t.Fatal("the baby heart rate tile is gone")
	}
	tile := page[i:min(i+700, len(page))]
	if !strings.Contains(tile, "Not measured by this device") {
		t.Error("the fetal heart rate tile does not disclaim that it has no sensor")
	}
	if !strings.Contains(tile, "no sensor") {
		t.Error("the tile has no badge marking it unavailable")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// With a live Vapi key on a public box, an open /api/call means anyone who
// finds the URL can ring a real person's phone at someone else's expense.
func TestCallTokenGatesTheCallWithoutBreakingTheLocalDemo(t *testing.T) {
	withToken := &Server{Hub: NewHub(), Owner: "demo", CallToken: "s3cret",
		Voice: voice.New("k", "pn", ""), CallTo: "+15555550123"}
	mux := withToken.Routes()

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/call", nil))
	if rr.Code != http.StatusForbidden {
		t.Errorf("POST /api/call with no token -> %d, want 403", rr.Code)
	}

	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/call?k=wrong", nil))
	if rr.Code != http.StatusForbidden {
		t.Errorf("POST /api/call with a wrong token -> %d, want 403", rr.Code)
	}

	// The page hands the token to the button only when the visitor already
	// had it, so a scanner that loads the page cannot read it off the HTML.
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/demo", nil))
	if strings.Contains(rr.Body.String(), "s3cret") {
		t.Error("the remote page leaked the call token to an unauthenticated visitor")
	}
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/demo?k=s3cret", nil))
	if !strings.Contains(rr.Body.String(), "s3cret") {
		t.Error("the remote page withheld the token from a visitor who had it")
	}

	// No token configured: nothing changes for the Pi or the local demo.
	open := &Server{Hub: NewHub(), Owner: "demo", Voice: voice.New("k", "pn", ""), CallTo: "+15555550123"}
	rr = httptest.NewRecorder()
	open.Routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/call", nil))
	if rr.Code == http.StatusForbidden {
		t.Error("an unconfigured token still blocked the call; the local demo would break")
	}
	// And the placeholder must never survive into the served page.
	rr = httptest.NewRecorder()
	open.Routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/demo", nil))
	if strings.Contains(rr.Body.String(), "{{CALL_TOKEN}}") {
		t.Error("the remote page shipped an unsubstituted placeholder")
	}
}
