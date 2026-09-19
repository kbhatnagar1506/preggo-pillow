package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

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
	got := maskNumber("+1 (404) 429-2188")
	if !strings.HasSuffix(got, "88") || !strings.HasPrefix(got, "+1") {
		t.Errorf("maskNumber = %q; want the country code and the last two digits", got)
	}
	if strings.Contains(got, "4042") {
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
