// Package clinical turns the nightly series into a short summary for the
// provider report, using Gemini on Vertex AI.
//
// WHY A SECOND MODEL, WHEN BACKBOARD ALREADY HAS ONE
// --------------------------------------------------
// They do different jobs, and stacking two models on the same job would be
// obvious padding.
//
//	Backboard  narrative recall. What she said, when she was seen, what she
//	           was told. Semantic memory across a whole pregnancy.
//	Gemini     numeric reasoning over the time series. Fourteen nights of
//	           counts, a baseline, maternal state, and what the shape of that
//	           means in one paragraph a midwife can read in ten seconds.
//
// The safety rule is the same in both and is not negotiable: never reassure.
// Reassurance is the documented failure mode of home fetal monitoring, because
// it delays women from seeking care when movement is down.
package clinical

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// Summarizer calls an OpenAI-compatible endpoint. On this machine that is the
// LiteLLM proxy on :4000, which fronts Gemini on Vertex AI using the service
// account. Any compatible endpoint works, which keeps the Pi from needing
// Google credentials of its own.
type Summarizer struct {
	base   string
	key    string
	model  string
	http   *http.Client
	logger *log.Logger
}

type Options struct {
	BaseURL string // falls back to LLM_BASE_URL, then the local LiteLLM proxy
	APIKey  string // falls back to LLM_API_KEY
	Model   string // falls back to LLM_MODEL, then "vertex-chat"
	Logger  *log.Logger
}

func (s *Summarizer) Enabled() bool { return s != nil && s.key != "" }

func New(o Options) *Summarizer {
	if o.Logger == nil {
		o.Logger = log.Default()
	}
	if o.BaseURL == "" {
		o.BaseURL = envOr("LLM_BASE_URL", "http://127.0.0.1:4000/v1")
	}
	if o.APIKey == "" {
		o.APIKey = os.Getenv("LLM_API_KEY")
	}
	if o.Model == "" {
		o.Model = envOr("LLM_MODEL", "vertex-chat")
	}
	if o.APIKey == "" {
		o.Logger.Println("clinical: no LLM_API_KEY, written summary is off (the report still renders)")
		return &Summarizer{logger: o.Logger}
	}
	o.Logger.Printf("clinical: %s via %s", o.Model, o.BaseURL)
	return &Summarizer{
		base:   strings.TrimRight(o.BaseURL, "/"),
		key:    o.APIKey,
		model:  o.Model,
		http:   &http.Client{Timeout: 90 * time.Second},
		logger: o.Logger,
	}
}

// Input is everything the model gets. Deliberately just numbers: no name, no
// free text, no notes. The narrative lives in Backboard, and health data should
// not travel further than the job requires.
type Input struct {
	Nights         []NightPoint `json:"nights"`
	Baseline       float64      `json:"baseline"`
	Tonight        int          `json:"tonight"`
	DeviationPct   float64      `json:"deviation_percent"`
	Posture        string       `json:"posture"`
	SupineMinutes  float64      `json:"supine_minutes"`
	RespirationRPM float64      `json:"respiration_rpm"`
	SnorePercent   float64      `json:"snore_percent"`
	WakeEvents     int          `json:"wake_events"`
	Alert          bool         `json:"alert"`
}

type NightPoint struct {
	Date  string `json:"date"`
	Count int    `json:"count"`
}

const systemPrompt = `You write a short clinical note for a midwife or obstetrician, summarising
overnight fetal movement monitoring from a home device.

Hard rules:
- NEVER reassure that the baby is fine. There is no "all clear". Reassurance is the documented
  failure mode of home fetal monitoring because it delays women from seeking care.
- NEVER diagnose. Assessment of reduced fetal movement is a CTG and a scan, and belongs to a
  clinician.
- Compare only to THIS baby's own baseline, never to a population threshold. "Ten kicks in two
  hours" is a population rule applied to a baby with its own pattern.
- State the device's limits where relevant: counts are estimates from an accelerometer and contact
  microphone, and respiration is accurate to roughly plus or minus twenty percent.

Style: three or four sentences, plain clinical English, no bullet points, no headings, no preamble.
Lead with the movement trend. If movement is below baseline across consecutive nights, say so first
and say that assessment is indicated today.`

// Summarize returns the note, or an error. The report renders fine without it.
func (s *Summarizer) Summarize(ctx context.Context, in Input) (string, error) {
	if !s.Enabled() {
		return "", fmt.Errorf("summariser not configured")
	}
	payload, err := json.Marshal(in)
	if err != nil {
		return "", err
	}

	body := map[string]any{
		"model": s.model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": "Monitoring data:\n" + string(payload)},
		},
		// Generous on purpose: vertex-chat is a thinking model and reasoning
		// tokens count against this. A tight cap returns an empty string with
		// finish_reason "stop", which looks like a bug and is not one.
		"max_tokens":  1500,
		"temperature": 0.2,
	}
	b, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.base+"/chat/completions", bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+s.key)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("llm HTTP %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}

	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 || strings.TrimSpace(out.Choices[0].Message.Content) == "" {
		return "", fmt.Errorf("empty completion (finish_reason=%q): raise max_tokens, reasoning tokens count against it",
			finishReason(out.Choices))
	}
	return strings.TrimSpace(out.Choices[0].Message.Content), nil
}

func finishReason(ch []struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
	FinishReason string `json:"finish_reason"`
}) string {
	if len(ch) == 0 {
		return ""
	}
	return ch[0].FinishReason
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
