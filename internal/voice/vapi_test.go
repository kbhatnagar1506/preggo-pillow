package voice

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A phone call is more persuasive than a dashboard, which makes a wrong one
// more dangerous. The same rule that governs the written note governs this:
// never reassure, never diagnose, always end in "be seen today".
func TestScriptNeverReassuresOrDiagnoses(t *testing.T) {
	for _, a := range []Alert{
		{Nights: 1, DeviationPct: 18},
		{Nights: 3, DeviationPct: 41},
		{Nights: 0},
	} {
		s := strings.ToLower(a.Script())
		for _, banned := range []string{
			"baby is fine", "baby is well", "baby is healthy",
			"nothing to worry", "no cause for concern", "all clear", "reassur",
		} {
			if strings.Contains(s, banned) {
				t.Errorf("script contains reassurance %q: %s", banned, s)
			}
		}
		if !strings.Contains(s, "not a diagnosis") {
			t.Errorf("script must disclaim diagnosis: %s", s)
		}
		if !strings.Contains(s, "today") {
			t.Errorf("script must ask her to be seen TODAY, not eventually: %s", s)
		}
		if strings.Contains(s, "wait") && !strings.Contains(s, "do not wait") {
			t.Errorf("script must not suggest waiting: %s", s)
		}
	}
}

// The script is generated from the measurements so it cannot claim something
// the data does not show.
func TestScriptReportsTheActualNumbers(t *testing.T) {
	s := Alert{Nights: 3, DeviationPct: 41}.Script()
	if !strings.Contains(s, "3 nights") {
		t.Errorf("should name the run of nights: %s", s)
	}
	if !strings.Contains(s, "41 percent") {
		t.Errorf("should name the deviation: %s", s)
	}
	one := Alert{Nights: 1, DeviationPct: 12}.Script()
	if strings.Contains(one, "nights running") {
		t.Errorf("a single night must not be described as consecutive: %s", one)
	}
}

func TestCustomMessageOverrides(t *testing.T) {
	a := Alert{Nights: 3, DeviationPct: 41, CustomMessage: "  bespoke text  "}
	if got := a.Script(); got != "bespoke text" {
		t.Errorf("custom message should win and be trimmed, got %q", got)
	}
}

func TestBuildRequestMatchesVapiShape(t *testing.T) {
	c := New("k", "pn-1", "voice-1")
	req := c.BuildRequest(Alert{To: "+15551234567", Nights: 2, DeviationPct: 30})
	b, _ := json.Marshal(req)
	var m map[string]any
	_ = json.Unmarshal(b, &m)

	if m["phoneNumberId"] != "pn-1" {
		t.Errorf("phoneNumberId = %v", m["phoneNumberId"])
	}
	cust := m["customer"].(map[string]any)
	if cust["number"] != "+15551234567" {
		t.Errorf("customer.number = %v", cust["number"])
	}
	as := m["assistant"].(map[string]any)
	if as["firstMessageMode"] != "assistant-speaks-first" {
		t.Errorf("the assistant must speak first, got %v", as["firstMessageMode"])
	}
	if as["maxDurationSeconds"].(float64) <= 0 {
		t.Error("a call with no duration cap can hang open at 3am")
	}
	v := as["voice"].(map[string]any)
	if v["provider"] != "11labs" || v["voiceId"] != "voice-1" {
		t.Errorf("voice = %v", v)
	}
	sys := as["model"].(map[string]any)["messages"].([]any)[0].(map[string]any)["content"].(string)
	if !strings.Contains(sys, "NEVER say the baby is fine") {
		t.Error("the system prompt must forbid reassurance on the live leg of the call too")
	}
}

func TestVoiceOmittedWhenUnset(t *testing.T) {
	c := New("k", "pn", "")
	b, _ := json.Marshal(c.BuildRequest(Alert{To: "+1555"}))
	if strings.Contains(string(b), `"voice"`) {
		t.Error("an empty voice id should be omitted, not sent as empty")
	}
}

func TestEnabled(t *testing.T) {
	if New("", "pn", "").Enabled() {
		t.Error("no key should be disabled")
	}
	if New("k", "", "").Enabled() {
		t.Error("no phone number id should be disabled")
	}
	if !New("k", "pn", "").Enabled() {
		t.Error("configured should be enabled")
	}
}

func TestPlaceRejectsBadNumbers(t *testing.T) {
	c := New("k", "pn", "")
	if _, err := c.Place(context.Background(), Alert{To: ""}); err == nil {
		t.Error("empty number should error")
	}
	if _, err := c.Place(context.Background(), Alert{To: "5555550123"}); err == nil {
		t.Error("a non-E.164 number should error before we dial something wrong")
	}
}

func TestPlacePostsToTheRightEndpoint(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"call-1","status":"queued"}`))
	}))
	defer srv.Close()

	c := New("secret", "pn", "v")
	c.BaseURL = srv.URL
	res, err := c.Place(context.Background(), Alert{To: "+15551234567", Nights: 2, DeviationPct: 25})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/call/phone" {
		t.Errorf("path = %q, want /call/phone", gotPath)
	}
	if gotAuth != "Bearer secret" {
		t.Errorf("auth = %q", gotAuth)
	}
	if !strings.Contains(string(gotBody), "maternity unit today") {
		t.Error("the script did not reach Vapi")
	}
	if res.ID != "call-1" || res.Status != "queued" {
		t.Errorf("result = %+v", res)
	}
}

// Vapi's own 401 says "you may be using the private key instead of the public
// key, or vice versa" — worth surfacing, because it is exactly what happened.
func TestUnauthorizedExplainsTheKeyMixUp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Invalid Key."}`))
	}))
	defer srv.Close()
	c := New("wrong", "pn", "")
	c.BaseURL = srv.URL
	_, err := c.Place(context.Background(), Alert{To: "+15551234567"})
	if err == nil || !strings.Contains(err.Error(), "PRIVATE key") {
		t.Errorf("401 should explain the key mix-up, got %v", err)
	}
}

func TestServerErrorsSurface(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"bad phoneNumberId"}`))
	}))
	defer srv.Close()
	c := New("k", "pn", "")
	c.BaseURL = srv.URL
	_, err := c.Place(context.Background(), Alert{To: "+15551234567"})
	if err == nil || !strings.Contains(err.Error(), "bad phoneNumberId") {
		t.Errorf("want the API's own message, got %v", err)
	}
}

// Whoever answers this call has never heard of the codebase. They have heard
// of the product, if they have heard of anything.
func TestTheCallIntroducesTheProductNotTheCodebase(t *testing.T) {
	script := Alert{To: "+15551234567", DeviationPct: 40, Nights: 2}.Script()
	if strings.Contains(script, "Lull") {
		t.Errorf("the call still introduces itself as Lull:\n%s", script)
	}
	if !strings.Contains(script, "Preggo Pillow") {
		t.Errorf("the call does not name the product:\n%s", script)
	}
}
