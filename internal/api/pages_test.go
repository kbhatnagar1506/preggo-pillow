package api

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/kbhatnagar1506/lull/internal/auth"
	_ "modernc.org/sqlite"
)

// navPages are the five entries in the sidebar. Every one of them is a real
// route with a real file behind it.
var navPages = map[string]string{
	"/dashboard":   "dashboard.html",
	"/history":     "history.html",
	"/healthcare":  "healthcare.html",
	"/medications": "medications.html",
	"/settings":    "settings.html",
}

func pageServer() *Server {
	return &Server{Hub: NewHub(), Web: http.Dir("../../web/static"), Owner: "krishnabhatnagar"}
}

func TestEveryNavPageServes(t *testing.T) {
	mux := pageServer().Routes()
	for path := range navPages {
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusOK {
			t.Errorf("%s -> %d; a menu item that 404s is the first thing a judge clicks", path, rr.Code)
			continue
		}
		if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("%s served %s", path, ct)
		}
	}
}

// The bug this guards: the sidebar was copied into five files and three of
// its links pointed at routes that did not exist. Read the hrefs out of the
// shipped HTML and make the router answer every one of them.
func TestEveryLinkInTheSidebarResolves(t *testing.T) {
	mux := pageServer().Routes()
	href := regexp.MustCompile(`(?s)<nav class="nav">(.*?)</nav>`)
	link := regexp.MustCompile(`href="([^"]+)"`)

	for path, file := range navPages {
		body, err := os.ReadFile(filepath.Join("../../web/static", file))
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		nav := href.FindSubmatch(body)
		if nav == nil {
			t.Errorf("%s has no sidebar", file)
			continue
		}
		found := link.FindAllSubmatch(nav[1], -1)
		if len(found) != len(navPages) {
			t.Errorf("%s sidebar has %d links, want %d", file, len(found), len(navPages))
		}
		for _, m := range found {
			target := string(m[1])
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, target, nil))
			if rr.Code == http.StatusNotFound {
				t.Errorf("%s links to %s, which 404s", path, target)
			}
		}
	}
}

func TestEveryPageUsesTheSharedShell(t *testing.T) {
	for _, file := range navPages {
		body, err := os.ReadFile(filepath.Join("../../web/static", file))
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		s := string(body)
		// One stylesheet and one shell script, so the navigation cannot drift
		// between pages the way it did when each carried its own copy.
		for _, want := range []string{
			`href="/app.css"`, `src="/app.js"`,
			`id="side-nav"`, `id="menu-open"`, `href="/auth/logout"`,
		} {
			if !strings.Contains(s, want) {
				t.Errorf("%s is missing %s", file, want)
			}
		}
		if strings.Contains(s, "<style>") {
			t.Errorf("%s carries its own <style> block; it should use /app.css", file)
		}
	}
}

// Signed out, an app page must send you to the front door rather than render.
func TestNavPagesAreGatedWhenAuthIsConfigured(t *testing.T) {
	s := pageServer()
	s.Auth = enabledAuthForTest(t)
	mux := s.Routes()
	for path := range navPages {
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code == http.StatusOK {
			t.Errorf("%s rendered without a session", path)
		}
	}
}

func TestMaskNumberShowsEnoughToRecogniseAndNoMore(t *testing.T) {
	// 555-01xx is the block reserved for documentation, so no real number
	// ever lands in this repository.
	got := maskNumber("+1 (555) 555-0123")
	if !strings.HasSuffix(got, "23") || !strings.HasPrefix(got, "+1") {
		t.Errorf("maskNumber = %q; want the country code and the last two digits", got)
	}
	if strings.Contains(got, "5550") {
		t.Errorf("maskNumber = %q; it is still readable over a shoulder", got)
	}
	if maskNumber("") != "" {
		t.Errorf("an unset number must render as empty, not as a mask")
	}
}

// The iPhone bug: without touch-action, Safari holds each tap to test for
// double-tap-to-zoom and never delivers the second click, so the two-tap call
// button armed and then disarmed itself every time.
func TestRemoteButtonsOptOutOfDoubleTapZoom(t *testing.T) {
	if !strings.Contains(remotePage, "touch-action:manipulation") {
		t.Error("the remote's buttons do not set touch-action; the two-tap call button will not fire on iOS")
	}
}

// enabledAuthForTest builds an auth service that reports Enabled() without
// reaching Auth0: the gate is what is under test, not the provider.
func enabledAuthForTest(t *testing.T) *auth.Service {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sessions, err := auth.NewStore(db)
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	return &auth.Service{
		Provider: auth.NewProvider(auth.Config{
			Domain: "example.auth0.com", ClientID: "cid", ClientSecret: "sec",
			AppBaseURL: "http://localhost:3000",
		}),
		Sessions:      sessions,
		SessionSecret: "a-very-long-session-secret-value",
		DataKey:       make([]byte, 32),
	}
}

// Gating the pages but leaving the data behind them open means the pages were
// never gated: /report is the whole clinical summary and /api/memories is the
// written narrative, both of which were readable with no session at all.
func TestTheRecordNeedsASessionAndTheRemoteDoesNot(t *testing.T) {
	s := pageServer()
	s.Auth = enabledAuthForTest(t)
	mux := s.Routes()

	gated := []string{
		"/report", "/api/nights", "/api/maternal", "/api/memories",
		"/api/meds", "/api/settings", "/api/note", "/api/profile",
		"/api/appointment", "/api/ask",
	}
	for _, path := range gated {
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code == http.StatusOK {
			t.Errorf("%s served health data to an anonymous request", path)
		}
	}

	// The phone remote runs without a session by design, so the controls it
	// uses must stay open or the button the demo depends on stops working.
	//
	// /api/stream is included and needs a cancellable context: it is an SSE
	// handler that loops until the request is cancelled, so serving it into a
	// ResponseRecorder with httptest.NewRequest's background context blocks
	// until the whole package times out.
	for _, path := range []string{"/api/stream", "/api/kick", "/api/press", "/api/fool", "/api/call"} {
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		rr := newSyncRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, path, nil).WithContext(ctx))
		cancel()
		if status(rr) == http.StatusUnauthorized {
			t.Errorf("%s is gated; the phone remote has no session and cannot get one", path)
		}
	}
}

// A fetch() that gets a 302 to the login page sees HTML, not JSON, and fails
// inside JSON.parse — an expired session then looks like a syntax error.
func TestGatedAPIsAnswerWith401NotARedirect(t *testing.T) {
	s := pageServer()
	s.Auth = enabledAuthForTest(t)
	rr := httptest.NewRecorder()
	s.Routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/nights", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("/api/nights -> %d, want 401", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("/api/nights refused with %s, want JSON", ct)
	}
}

// "Lull" is the codebase. "Preggo Pillow" is the product. The two drifted, and
// the places they drifted were the three a stranger actually meets: the record
// a midwife reads, the page on the phone, and the voice on the call.
func TestNothingUserFacingSaysLull(t *testing.T) {
	if strings.Contains(remotePage, "Lull") {
		t.Error("the phone remote still says Lull")
	}
	if strings.Contains(reportTmplSrc, "Lull") {
		t.Error("the clinician's report still says Lull")
	}
	for _, file := range navPages {
		body, err := os.ReadFile(filepath.Join("../../web/static", file))
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		if strings.Contains(string(body), "Lull") {
			t.Errorf("%s still says Lull", file)
		}
	}
}

// status reads the recorder's code under its own lock. The SSE handler is
// still writing from the goroutine serving it when the test looks.
func status(r *syncRecorder) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.code
}

// A Server with nothing wired must refuse, not crash.
//
// Found by the sweep: handlePress dereferenced a nil Store and handleKick a
// nil Kicker. Both sit on routes the phone remote needs open, so they are
// reachable with no session at all — a build running -source phone with no
// local database answered an anonymous request with a panic.
func TestNoHandlerPanicsOnABareServer(t *testing.T) {
	bare := &Server{} // no Hub, no Store, no Kicker, no Web, no Auth
	mux := bare.Routes()

	paths := []struct{ method, path string }{
		{http.MethodGet, "/dashboard"}, {http.MethodGet, "/history"},
		{http.MethodGet, "/healthcare"}, {http.MethodGet, "/medications"},
		{http.MethodGet, "/settings"}, {http.MethodGet, "/report"},
		{http.MethodPost, "/api/press?source=x"}, {http.MethodPost, "/api/kick?strength=weak"},
		{http.MethodPost, "/api/fool"}, {http.MethodPost, "/api/call"},
		{http.MethodGet, "/api/nights"}, {http.MethodGet, "/api/maternal"},
		{http.MethodGet, "/api/memories"}, {http.MethodGet, "/api/meds"},
		{http.MethodGet, "/api/settings"}, {http.MethodGet, "/api/vitals"},
		{http.MethodPost, "/api/blind/start"}, {http.MethodPost, "/api/blind/stop"},
		{http.MethodGet, "/api/blind/score"}, {http.MethodPost, "/api/dose"},
		{http.MethodPost, "/api/meds/remove?id=1"}, {http.MethodPost, "/api/note"},
		{http.MethodPost, "/api/profile"}, {http.MethodPost, "/api/appointment"},
	}
	for _, p := range paths {
		func() {
			defer func() {
				if v := recover(); v != nil {
					t.Errorf("%s %s panicked: %v", p.method, p.path, v)
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			rr := newSyncRecorder()
			req := httptest.NewRequest(p.method, p.path, strings.NewReader("{}")).WithContext(ctx)
			req.Header.Set("Content-Type", "application/json")
			mux.ServeHTTP(rr, req)
			if c := status(rr); c >= 500 && c != http.StatusServiceUnavailable {
				t.Errorf("%s %s -> %d; want a refusal, not a server error", p.method, p.path, c)
			}
		}()
	}
}
