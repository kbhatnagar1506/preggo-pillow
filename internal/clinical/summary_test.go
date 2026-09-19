package clinical

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func sample() Input {
	return Input{
		Nights:       []NightPoint{{"2026-09-17", 241}, {"2026-09-18", 198}},
		Baseline:     338,
		Tonight:      198,
		DeviationPct: -41.4,
		Posture:      "lateral",
		Alert:        true,
	}
}

func TestDisabledWithoutKey(t *testing.T) {
	s := New(Options{APIKey: ""})
	if s.Enabled() {
		t.Fatal("summariser reports Enabled with no API key")
	}
	if _, err := s.Summarize(context.Background(), sample()); err == nil {
		t.Error("Summarize on a disabled summariser should return an error, not an empty string")
	}
}

func TestNilSummarizerIsSafe(t *testing.T) {
	var s *Summarizer
	if s.Enabled() {
		t.Fatal("nil summariser reports Enabled")
	}
}

func TestSummarizeReturnsTheCompletion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("auth header %q", got)
		}
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path %q", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)

		// The safety rules must reach the model, not just live in our docs.
		// Checked over the wire rather than against the constant, because the
		// failure that matters is the prompt not arriving.
		msgs, _ := json.Marshal(req["messages"])
		for _, rule := range []string{
			"NEVER REASSURE",
			"NEVER DIAGNOSE",
			"Green-top Guideline No. 57",
			"THIS BABY'S OWN BASELINE",
		} {
			if !strings.Contains(string(msgs), rule) {
				t.Errorf("system prompt did not carry %q to the model", rule)
			}
		}
		// vertex-chat is a thinking model: reasoning tokens count against the
		// cap, so a tight max_tokens silently returns an empty completion.
		if mt, ok := req["max_tokens"].(float64); !ok || mt < 500 {
			t.Errorf("max_tokens %v is too low for a thinking model", req["max_tokens"])
		}

		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"  Movement is below baseline.  "},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	s := New(Options{BaseURL: srv.URL, APIKey: "test-key", Model: "vertex-chat"})
	got, err := s.Summarize(context.Background(), sample())
	if err != nil {
		t.Fatal(err)
	}
	if got != "Movement is below baseline." {
		t.Errorf("got %q, want the trimmed completion", got)
	}
}

// An empty completion with finish_reason "stop" looks like a bug and is not
// one: the reasoning tokens ate the budget. The error must say so, or someone
// loses an hour at 3am.
func TestEmptyCompletionExplainsItself(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":""},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	s := New(Options{BaseURL: srv.URL, APIKey: "k"})
	_, err := s.Summarize(context.Background(), sample())
	if err == nil {
		t.Fatal("expected an error for an empty completion")
	}
	if !strings.Contains(err.Error(), "max_tokens") {
		t.Errorf("error %q does not point at the real cause", err)
	}
}

func TestHTTPErrorIsReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer srv.Close()

	s := New(Options{BaseURL: srv.URL, APIKey: "k"})
	if _, err := s.Summarize(context.Background(), sample()); err == nil {
		t.Fatal("expected an error on HTTP 429")
	}
}

func TestBaseURLTrailingSlashIsTolerated(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	s := New(Options{BaseURL: srv.URL + "/", APIKey: "k"})
	if _, err := s.Summarize(context.Background(), sample()); err != nil {
		t.Fatal(err)
	}
	if path != "/chat/completions" {
		t.Errorf("path %q, want /chat/completions (no double slash)", path)
	}
}

// The prompt is the clinical contract. It is the only place the guideline's
// rules are encoded, it is a string with no compiler to check it, and a
// well-meaning edit that drops a line changes what the system tells a pregnant
// woman. These tests are that line's only guard.
func TestPromptImplementsGuidelineRules(t *testing.T) {
	rules := []struct{ name, needle string }{
		{"names the guideline", "Green-top Guideline No. 57"},
		{"forbids inventing a rule", "do not\ninvent a rule"},
		{"quotes the no-threshold finding", "no uniform\n  threshold"},
		{"requires the baby's own baseline", "THIS BABY'S OWN BASELINE"},
		{"rejects the population rule", "Ten kicks in two hours"},
		{"leads with repeat episodes", "two or more occasions"},
		{"names consecutive nights", "second consecutive night"},
		{"forbids reassurance", "NEVER REASSURE"},
		{"forbids diagnosis", "NEVER DIAGNOSE"},
		{"directs to the maternity unit", "contact her maternity unit today"},
		{"forbids advising delay", "Do not suggest waiting"},
		{"calibrates a single episode", "70 percent"},
		{"states device limits", "twenty percent"},
	}
	for _, r := range rules {
		t.Run(r.name, func(t *testing.T) {
			if !strings.Contains(systemPrompt, r.needle) {
				t.Errorf("the prompt no longer contains %q — a guideline rule has been lost", r.needle)
			}
		})
	}
}

// Reassurance is the documented failure mode of home fetal monitoring: a woman
// hears "looks fine", waits, and presents late. The prompt must never license
// it, so no phrasing that could read as an all-clear may creep in.
func TestPromptNeverLicensesReassurance(t *testing.T) {
	banned := []string{
		"reassure the", "all clear", "baby is fine", "looks healthy",
		"no cause for concern", "nothing to worry",
	}
	lower := strings.ToLower(systemPrompt)
	for _, b := range banned {
		idx := strings.Index(lower, strings.ToLower(b))
		if idx < 0 {
			continue
		}
		// It is fine to mention these as things to avoid; not fine to permit them.
		window := lower[max0(idx-60):idx]
		if !strings.Contains(window, "never") && !strings.Contains(window, "do not") &&
			!strings.Contains(window, "no ") {
			t.Errorf("the prompt appears to permit reassurance near %q", b)
		}
	}
}

func TestGuidelineReferenceIsExported(t *testing.T) {
	if !strings.Contains(GuidelineRef, "Green-top Guideline No. 57") {
		t.Errorf("GuidelineRef = %q", GuidelineRef)
	}
	if !strings.HasPrefix(GuidelineURL, "https://www.rcog.org.uk/") {
		t.Errorf("GuidelineURL should point at RCOG, got %q", GuidelineURL)
	}
}

func max0(v int) int {
	if v < 0 {
		return 0
	}
	return v
}
