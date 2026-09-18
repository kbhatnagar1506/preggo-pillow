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

		// The safety rule must reach the model, not just live in our docs.
		msgs, _ := json.Marshal(req["messages"])
		if !strings.Contains(string(msgs), "NEVER reassure") {
			t.Error("system prompt did not carry the never-reassure rule")
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
