// Package voice places the escalation call.
//
// Everything else in Lull ends at a screen. This is the part that reaches a
// person who is not looking at one — the partner, at 3am, when the numbers say
// the baby has been quiet for two nights and nobody has noticed.
//
// The call is deliberately narrow in what it may say. The same rule that
// governs the written note governs this: it never reassures, never diagnoses,
// and always ends in "be seen today". A phone call is more persuasive than a
// dashboard, which makes a wrong one more dangerous.
package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kbhatnagar1506/lull/internal/brand"
)

// DefaultBaseURL is Vapi's API.
const DefaultBaseURL = "https://api.vapi.ai"

// Client places outbound calls through Vapi.
type Client struct {
	APIKey        string // the PRIVATE key; the public one is for the browser SDK and 401s here
	PhoneNumberID string // which of your numbers to call FROM
	VoiceID       string // 11Labs voice
	BaseURL       string
	HTTP          *http.Client
}

// New builds a client from configuration. It returns a client even when
// unconfigured, so callers can check Enabled() rather than branch on nil.
func New(apiKey, phoneNumberID, voiceID string) *Client {
	return &Client{
		APIKey:        strings.TrimSpace(apiKey),
		PhoneNumberID: strings.TrimSpace(phoneNumberID),
		VoiceID:       strings.TrimSpace(voiceID),
		BaseURL:       DefaultBaseURL,
		HTTP:          &http.Client{Timeout: 20 * time.Second},
	}
}

func (c *Client) Enabled() bool {
	return c != nil && c.APIKey != "" && c.PhoneNumberID != ""
}

// Alert describes what the call should say.
type Alert struct {
	To            string  // E.164, e.g. +15555550123
	Nights        int     // consecutive nights below baseline
	DeviationPct  float64 // how far below, as a positive percentage
	CustomMessage string  // overrides the generated text entirely
}

// Script builds what the assistant says first.
//
// Generated from the measurements rather than hard-coded, so the call cannot
// claim something the data does not show. If a caller supplies CustomMessage it
// wins, because the demo needs a fixed script — but the default is the honest
// one and the default is what ships.
func (a Alert) Script() string {
	if s := strings.TrimSpace(a.CustomMessage); s != "" {
		return s
	}
	var b strings.Builder
	b.WriteString("Hello. This is an automated alert from " + brand.Product + ", " + brand.Tagline + ". ")
	switch {
	case a.Nights >= 2:
		fmt.Fprintf(&b, "For %d nights running, the baby's movement has been below its own established baseline. ", a.Nights)
	default:
		b.WriteString("Last night, the baby's movement was below its own established baseline. ")
	}
	if a.DeviationPct > 0 {
		fmt.Fprintf(&b, "Last night was %.0f percent down. ", a.DeviationPct)
	}
	// Never an all-clear, never a diagnosis, always the same action. This
	// mirrors the written note, and for the same reason: false reassurance is
	// the documented failure of home fetal monitoring.
	b.WriteString("This is not a diagnosis, and it does not mean anything is wrong. ")
	b.WriteString("It means today is different from this baby's normal, and that needs to be checked by a midwife. ")
	b.WriteString("Please take her to the maternity unit today. Do not wait for it to improve on its own. ")
	b.WriteString("Thank you.")
	return b.String()
}

type callRequest struct {
	PhoneNumberID string    `json:"phoneNumberId"`
	Customer      customer  `json:"customer"`
	Assistant     assistant `json:"assistant"`
}

type customer struct {
	Number string `json:"number"`
}

type assistant struct {
	FirstMessage       string    `json:"firstMessage"`
	FirstMessageMode   string    `json:"firstMessageMode"`
	MaxDurationSeconds int       `json:"maxDurationSeconds"`
	Model              modelCfg  `json:"model"`
	Voice              *voiceCfg `json:"voice,omitempty"`
}

type modelCfg struct {
	Provider string     `json:"provider"`
	Model    string     `json:"model"`
	Messages []modelMsg `json:"messages"`
}

type modelMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type voiceCfg struct {
	Provider string `json:"provider"`
	VoiceID  string `json:"voiceId"`
}

// BuildRequest is separated from Place so the exact payload can be asserted in
// tests without a network or an account.
func (c *Client) BuildRequest(a Alert) callRequest {
	script := a.Script()
	req := callRequest{
		PhoneNumberID: c.PhoneNumberID,
		Customer:      customer{Number: a.To},
		Assistant: assistant{
			FirstMessage:     script,
			FirstMessageMode: "assistant-speaks-first",
			// A stuck call is a real cost and a real nuisance at 3am.
			MaxDurationSeconds: 120,
			Model: modelCfg{
				Provider: "openai",
				Model:    "gpt-4o-mini",
				Messages: []modelMsg{{
					Role: "system",
					Content: "You are delivering a single fetal-movement alert and nothing else. " +
						"Speak the first message, then stop. If the person asks a question, " +
						"repeat that movement is below this baby's baseline and that she should " +
						"be seen at the maternity unit today. " +
						"NEVER say the baby is fine, well, healthy, or that there is nothing to " +
						"worry about — false reassurance is what delays care and costs lives. " +
						"NEVER diagnose. NEVER suggest waiting or checking again later. " +
						"Keep every reply under two sentences, then end the call.",
				}},
			},
		},
	}
	if c.VoiceID != "" {
		req.Assistant.Voice = &voiceCfg{Provider: "11labs", VoiceID: c.VoiceID}
	}
	return req
}

// Result is what a placed call returns.
type Result struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// Place makes the call.
func (c *Client) Place(ctx context.Context, a Alert) (*Result, error) {
	if !c.Enabled() {
		return nil, fmt.Errorf("voice calling is not configured (need VAPI_API_KEY and VAPI_PHONE_NUMBER_ID)")
	}
	if strings.TrimSpace(a.To) == "" {
		return nil, fmt.Errorf("no number to call")
	}
	if !strings.HasPrefix(a.To, "+") {
		return nil, fmt.Errorf("number %q must be E.164, starting with +", a.To)
	}

	body, err := json.Marshal(c.BuildRequest(a))
	if err != nil {
		return nil, err
	}
	base := c.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/call/phone", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")

	cl := c.HTTP
	if cl == nil {
		cl = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := cl.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("vapi: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("vapi: unauthorized — this endpoint needs the PRIVATE key, "+
			"not the public one used by the browser SDK (%s)", strings.TrimSpace(string(raw)))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("vapi: %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	var out Result
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("vapi: decode response: %w", err)
	}
	return &out, nil
}
