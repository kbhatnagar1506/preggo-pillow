package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kbhatnagar1506/lull/internal/vitals"
)

func post(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/vitals", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.Routes().ServeHTTP(rr, req)
	return rr
}

func TestVitalsIngestAndSummarise(t *testing.T) {
	s := &Server{Hub: NewHub(), Vitals: vitals.NewStore(64)}
	if rr := post(t, s, `{"pulse_bpm":78,"breathing_rpm":15,"source":"presage"}`); rr.Code != http.StatusOK {
		t.Fatalf("POST -> %d: %s", rr.Code, rr.Body.String())
	}
	rr := httptest.NewRecorder()
	s.Routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/vitals", nil))
	b := rr.Body.String()
	for _, want := range []string{`"pulse_bpm":78`, `"source":"presage"`, `"cleared":true`, `"normal":true`} {
		if !strings.Contains(b, want) {
			t.Errorf("summary missing %s: %s", want, b)
		}
	}
}

// A camera that loses the face emits zeros. Storing a zero pulse beside "the
// baby moved less" would read as a catastrophe rather than a dropped frame.
func TestVitalsRejectsImplausible(t *testing.T) {
	s := &Server{Hub: NewHub(), Vitals: vitals.NewStore(64)}
	for _, body := range []string{
		`{"pulse_bpm":0,"breathing_rpm":0,"source":"presage"}`,
		`{"pulse_bpm":400,"source":"presage"}`,
		`{"breathing_rpm":0.2,"source":"presage"}`,
	} {
		if rr := post(t, s, body); rr.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s -> %d, want 422", body, rr.Code)
		}
	}
	rr := httptest.NewRecorder()
	s.Routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/vitals", nil))
	if !strings.Contains(rr.Body.String(), `"available":false`) {
		t.Error("nothing plausible was posted, so there should be no summary")
	}
}

// An unknown source must not be able to claim FDA clearance by asserting it.
func TestUnknownSourceDowngradesToManual(t *testing.T) {
	s := &Server{Hub: NewHub(), Vitals: vitals.NewStore(64)}
	post(t, s, `{"pulse_bpm":70,"breathing_rpm":14,"source":"totally-cleared-honest"}`)
	rr := httptest.NewRecorder()
	s.Routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/vitals", nil))
	b := rr.Body.String()
	if strings.Contains(b, `"cleared":true`) {
		t.Errorf("an arbitrary source claimed clearance: %s", b)
	}
	if !strings.Contains(b, `"source":"manual"`) {
		t.Errorf("unknown source should fall back to manual: %s", b)
	}
}

func TestVitalsUnavailableWithoutStore(t *testing.T) {
	s := &Server{Hub: NewHub()}
	rr := httptest.NewRecorder()
	s.Routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/vitals", nil))
	if !strings.Contains(rr.Body.String(), `"available":false`) {
		t.Error("no store should report unavailable, not crash")
	}
}

func TestVitalsRejectsGarbage(t *testing.T) {
	s := &Server{Hub: NewHub(), Vitals: vitals.NewStore(8)}
	if rr := post(t, s, `not json`); rr.Code != http.StatusBadRequest {
		t.Errorf("garbage -> %d, want 400", rr.Code)
	}
}
