package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kbhatnagar1506/lull/internal/store"
)

func medsServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "lull.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &Server{Hub: NewHub(), Store: st, Web: http.Dir("../../web/static")}
}

func doJSON(t *testing.T, mux *http.ServeMux, method, path, body string) (int, map[string]any) {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, r)
	out := map[string]any{}
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	return rr.Code, out
}

func TestMedicationRoundTrip(t *testing.T) {
	mux := medsServer(t).Routes()

	code, _ := doJSON(t, mux, http.MethodPost, "/api/meds",
		`{"name":"Prenatal vitamin","dose":"1 tablet","times":"08:00, 8:00PM"}`)
	if code != http.StatusOK {
		t.Fatalf("add -> %d", code)
	}

	code, got := doJSON(t, mux, http.MethodGet, "/api/meds?tz=America/New_York", "")
	if code != http.StatusOK {
		t.Fatalf("list -> %d", code)
	}
	meds, _ := got["medications"].([]any)
	if len(meds) != 1 {
		t.Fatalf("got %d medications, want 1", len(meds))
	}
	doses, _ := got["doses"].([]any)
	if len(doses) != 2 {
		t.Errorf("got %d doses, want 2 (08:00 and 20:00)", len(doses))
	}
}

func TestAddingAMedicationWithoutATimeIsRefused(t *testing.T) {
	mux := medsServer(t).Routes()
	code, got := doJSON(t, mux, http.MethodPost, "/api/meds", `{"name":"Iron","times":"whenever"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("-> %d; an unparseable time must not become a silently missing reminder", code)
	}
	if got["error"] == nil {
		t.Error("no reason given for the refusal")
	}
}

// A crawler, a link preview or a prefetch must not be able to change what the
// record says she took.
func TestDoseAndRemovalRefuseGET(t *testing.T) {
	mux := medsServer(t).Routes()
	for _, path := range []string{"/api/dose", "/api/meds/remove?id=1"} {
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusMethodNotAllowed {
			t.Errorf("GET %s -> %d, want 405", path, rr.Code)
		}
	}
}

func TestSettingsReportsWhatIsActuallyWired(t *testing.T) {
	s := medsServer(t)
	s.Owner = "krishnabhatnagar"
	s.Device = "lull-01"
	s.Source = "i2c"
	s.CallTo = "+15555550123"

	code, got := doJSON(t, s.Routes(), http.MethodGet, "/api/settings", "")
	if code != http.StatusOK {
		t.Fatalf("-> %d", code)
	}
	dev, _ := got["device"].(map[string]any)
	if dev["id"] != "lull-01" || dev["source"] != "i2c" {
		t.Errorf("device reported as %v; it must read the running process, not the HTML", dev)
	}
	caps, _ := got["capabilities"].(map[string]any)
	// The one claim this product must never make. No accelerometer or camera
	// reads a fetal heart rate through the abdominal wall.
	if caps["fetal_hr"] != false {
		t.Error("settings claims a fetal heart rate capability")
	}
	// Voice is not configured on this bare server, and the page must say so
	// rather than showing a call button that does nothing.
	esc, _ := got["escalation"].(map[string]any)
	if esc["voice_configured"] != false {
		t.Errorf("voice reported as configured with no Vapi client: %v", esc)
	}
	if to, _ := esc["calls"].(string); strings.Contains(to, "4042") {
		t.Errorf("settings printed the number in the clear: %q", to)
	}
}
