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
	Posture       string  `json:"posture"`
	SupineMinutes float64 `json:"supine_minutes"`

	// RespirationRPM is omitted unless RespirationQuality says the tracker
	// stands behind it. See withoutUnreliableRespiration.
	RespirationRPM float64 `json:"respiration_rpm,omitempty"`
	// RespirationQuality is maternal.RespirationQuality as a string. Kept as
	// a string so this package stays a wire format with no opinion about
	// accelerometers, the same way Source is on MaternalVitals.
	RespirationQuality string `json:"respiration_quality,omitempty"`

	SnorePercent float64 `json:"snore_percent"`
	WakeEvents   int     `json:"wake_events"`
	Alert        bool    `json:"alert"`

	// Contactless maternal vitals, where an instrument measured them. This is
	// the control arm of the whole argument: "her numbers are normal AND the
	// baby moved less" is a far stronger statement than either half alone.
	MaternalVitals *MaternalVitals `json:"maternal_vitals,omitempty"`
}

// MaternalVitals carries the measurement AND how it was measured, because the
// note must qualify a +/-20% accelerometer estimate and must not qualify an
// FDA-cleared one.
type MaternalVitals struct {
	PulseBPM     float64 `json:"pulse_bpm,omitempty"`
	BreathingRPM float64 `json:"breathing_rpm,omitempty"`
	Source       string  `json:"source"`
	Cleared      bool    `json:"fda_cleared"`
	Accuracy     string  `json:"accuracy"`
	Normal       bool    `json:"within_normal_range"`
}

type NightPoint struct {
	Date  string `json:"date"`
	Count int    `json:"count"`
}

// RespirationMeasured is the one RespirationQuality under which a breathing
// rate may be sent to the model at all. It mirrors maternal.RespMeasured;
// TestRespirationQualityAgreesWithTheTracker in internal/api pins the two
// together so they cannot drift apart silently.
const RespirationMeasured = "measured"

// withoutUnreliableRespiration drops a breathing rate the tracker would not
// stand behind, before the model ever sees it.
//
// The prompt already forbids quoting an unresolved rate, but a prompt is a
// request and this note goes into a clinical record. A model cannot narrate a
// number it was never given, so the number is withheld rather than caveated.
// The quality string still travels, so the note can say the rate was not
// resolved — which is a fact, and a useful one.
func (in Input) withoutUnreliableRespiration() Input {
	if in.RespirationQuality != RespirationMeasured {
		in.RespirationRPM = 0
	}
	return in
}

// GuidelineRef is the clinical rule this note implements, shown beside the
// analysis on the dashboard.
//
// It matters that this is visible. An LLM writing plausible medical-sounding
// prose is worthless; an LLM applying a named, published rule to measured
// numbers is a different thing, and the reader can check it.
const GuidelineRef = "RCOG Green-top Guideline No. 57: Reduced Fetal Movements"

// GuidelineURL points at the guideline itself.
const GuidelineURL = "https://www.rcog.org.uk/guidance/browse-all-guidance/green-top-guidelines/reduced-fetal-movements-green-top-guideline-no-57/"

// systemPrompt implements GTG-57 rather than inventing a rule.
//
// Each hard rule below traces to something the guideline actually says, and the
// quotes are there so a future reader can check the implementation against the
// source instead of trusting this comment.
const systemPrompt = `You write a short clinical note for a midwife or obstetrician, summarising
overnight fetal movement monitoring from a home device.

You are applying RCOG Green-top Guideline No. 57 (Reduced Fetal Movements). Follow it; do not
invent a rule of your own.

Hard rules, each from the guideline:

- COMPARE ONLY TO THIS BABY'S OWN BASELINE. The guideline is explicit that "there is no uniform
  threshold of fetal movements above which perinatal morbidity increases", and advises that women
  "be aware of their baby's individual pattern of movements". So never cite a population figure.
  "Ten kicks in two hours" is a rule the guideline does not endorse.

- LEAD WITH REPEAT EPISODES. Reduced movement reported "on two or more occasions" carries an
  increased risk of stillbirth, fetal growth restriction and preterm birth. If this is the second
  or later consecutive night below baseline, that is the most important thing in the note and it
  goes in the first sentence, with the words "second consecutive night" or similar.

- NEVER REASSURE. There is no "all clear" here, and false reassurance is the documented failure of
  home fetal monitoring: it delays women from seeking care. Do not say the baby is fine, well,
  healthy, normal, or reassuring. Absence of a flag is not evidence of wellbeing.

- NEVER DIAGNOSE. Assessment of reduced fetal movement is a CTG and a scan, and belongs to a
  clinician. Where movement is below baseline, say that assessment is indicated and that she should
  contact her maternity unit today. Do not suggest waiting, monitoring at home, or trying again
  later.

- DO NOT OVERSTATE A SINGLE QUIET NIGHT. Around 70 percent of pregnancies with a single episode of
  reduced fetal movements are uncomplicated. State the observation and the recommendation without
  alarm; the point is to prompt contact, not fear.

- STATE THE DEVICE'S LIMITS where relevant: movement counts are estimates from accelerometers.
  Respiration derived from the accelerometer is accurate to roughly plus or minus twenty percent
  and must be qualified as such.

- NEVER INVENT A BREATHING RATE. respiration_rpm is present only when the device resolved one it
  stands behind. When respiration_quality is "provisional" or "unmeasured", or respiration_rpm is
  absent, there is no maternal breathing rate for this night: say it was not resolved, or say
  nothing about it. Do not estimate one, do not infer one from the other figures, and never read
  an absent rate as a low rate — a missing number is a missing measurement, not bradypnoea.

- USE THE MATERNAL VITALS AS A CONTROL, when maternal_vitals is present. If her pulse and breathing
  are within normal range while movement is below baseline, say so explicitly in one clause: it
  distinguishes a change in the baby from a change in the mother, and it is the single most useful
  thing in the note. When fda_cleared is true the figures are from an FDA-cleared instrument and
  must NOT be hedged with the plus-or-minus-twenty-percent caveat; when it is false they must be.
  Never present maternal vitals as evidence the baby is well — they say nothing about the baby.

Style: three or four sentences, plain clinical English, no bullet points, no headings, no preamble.
Lead with the movement trend.`

// Summarize returns the note, or an error. The report renders fine without it.
func (s *Summarizer) Summarize(ctx context.Context, in Input) (string, error) {
	if !s.Enabled() {
		return "", fmt.Errorf("summariser not configured")
	}
	payload, err := json.Marshal(in.withoutUnreliableRespiration())
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
