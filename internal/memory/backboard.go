// Package memory gives Lull a narrative memory on Backboard.
//
// WHY THIS IS HERE, AND NOT BOLTED ON
// -----------------------------------
// Lull's whole argument is that a woman in her third trimester is observed for
// about one tenth of one percent of the time, and everything that matters
// happens in the gap. The device closes the NUMERIC gap: it counts movement
// every night.
//
// There is a second gap nobody closes. What she noticed. What she was worried
// about on the 12th. What the midwife said at the last appointment. None of
// that persists anywhere either, so every appointment restarts from nothing and
// she has to re-explain herself.
//
//	The device remembers the numbers. Backboard remembers the pregnancy.
//
// DEGRADATION IS THE CONTRACT
// ---------------------------
// Nothing in the live demo may depend on the network. Every call here is
// asynchronous and best-effort: no API key, no internet, a venue captive
// portal, or Backboard being down all degrade Lull to exactly what it was
// before, with a log line and nothing else. The sensor loop never blocks on a
// HTTP request.
package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kbhatnagar1506/lull/internal/brand"
)

const defaultBase = "https://app.backboard.io/api"

// Client talks to Backboard. A nil or disabled Client is safe to call.
type Client struct {
	base   string
	key    string
	http   *http.Client
	logger *log.Logger

	mu          sync.RWMutex
	assistantID string
	threadID    string

	queue chan writeReq
	once  sync.Once
	stop  chan struct{}
}

type writeReq struct {
	content  string
	metadata map[string]any
}

// Options configure the client.
type Options struct {
	APIKey      string
	AssistantID string // reused if set; otherwise found or created by name
	Name        string // assistant name to find or create
	BaseURL     string
	Logger      *log.Logger
}

// Enabled reports whether memory is wired up. Callers should not branch on this
// for correctness, only for reporting: every method is safe on a nil client.
func (c *Client) Enabled() bool { return c != nil && c.key != "" }

// AssistantID exposes the assistant this client is bound to, for the UI.
func (c *Client) AssistantID() string {
	if c == nil {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.assistantID
}

// New builds a client. It returns a disabled client and a nil error when no API
// key is present, because "no key" is a normal way to run Lull, not a failure.
func New(ctx context.Context, o Options) (*Client, error) {
	if o.Logger == nil {
		o.Logger = log.Default()
	}
	if o.APIKey == "" {
		o.APIKey = os.Getenv("BACKBOARD_API_KEY")
	}
	if o.APIKey == "" {
		o.Logger.Println("memory: no BACKBOARD_API_KEY, narrative memory is off (Lull works normally)")
		return &Client{logger: o.Logger}, nil
	}
	if o.BaseURL == "" {
		o.BaseURL = defaultBase
	}
	if o.Name == "" {
		o.Name = brand.Product
	}

	c := &Client{
		base:        strings.TrimRight(o.BaseURL, "/"),
		key:         o.APIKey,
		http:        &http.Client{Timeout: 30 * time.Second},
		logger:      o.Logger,
		assistantID: o.AssistantID,
		queue:       make(chan writeReq, 256),
		stop:        make(chan struct{}),
	}

	if c.assistantID == "" {
		id, err := c.findOrCreateAssistant(ctx, o.Name)
		if err != nil {
			// Still return an enabled-looking client? No: without an assistant
			// there is nowhere to write. Degrade to off and say why.
			o.Logger.Printf("memory: could not reach Backboard (%v), narrative memory is off", err)
			return &Client{logger: o.Logger}, nil
		}
		c.assistantID = id
	}
	o.Logger.Printf("memory: Backboard assistant %s", c.assistantID)

	go c.drain()
	return c, nil
}

// Close flushes politely and stops the writer.
func (c *Client) Close() {
	if !c.Enabled() {
		return
	}
	c.once.Do(func() { close(c.stop) })
}

// ---------------------------------------------------------------- writing

// Remember queues a memory. It never blocks and never returns an error,
// because a failed memory write must not be able to affect a sensor reading.
func (c *Client) Remember(content string, metadata map[string]any) {
	if !c.Enabled() || content == "" {
		return
	}
	select {
	case c.queue <- writeReq{content: content, metadata: metadata}:
	default:
		c.logger.Println("memory: write queue full, dropping a memory")
	}
}

func (c *Client) drain() {
	for {
		select {
		case <-c.stop:
			return
		case w := <-c.queue:
			if err := c.addMemory(context.Background(), w); err != nil {
				c.logger.Printf("memory: write failed: %v", err)
			}
		}
	}
}

func (c *Client) addMemory(ctx context.Context, w writeReq) error {
	body := map[string]any{"content": w.content}
	if len(w.metadata) > 0 {
		body["metadata"] = sanitizeMetadata(w.metadata)
	}
	var out struct {
		MemoryID string `json:"memory_id"`
	}

	// Two attempts. Backboard returns transient 500s, and a dropped memory is
	// a fact about a pregnancy that no longer exists anywhere.
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		err = c.do(ctx, http.MethodPost,
			fmt.Sprintf("/assistants/%s/memories", c.AssistantID()), body, &out)
		if err == nil {
			return nil
		}
		time.Sleep(time.Duration(attempt+1) * 750 * time.Millisecond)
	}
	return err
}

// sanitizeMetadata works around one undocumented Backboard behaviour, found by
// bisecting payloads: METADATA KEYS ARE TYPE-LOCKED AFTER FIRST USE.
//
// A brand-new key accepts whatever type arrives first. Every later write that
// sends a different type for that key returns HTTP 500 with a generic
// "Something went wrong", forever, for that assistant. Verified both ways:
//
//	{"brandnew1":"hello"} -> 201, then {"brandnew1":42}      -> 500
//	{"brandnew2":42}      -> 201, then {"brandnew2":"hello"} -> 500
//
// Two consequences we design around:
//
//  1. Every value is sent as a string, always. One type per key, forever, so a
//     key can never be poisoned by an int today and a float tomorrow.
//  2. Keys are namespaced with a "lull_" prefix, because a bare name like
//     "count" or "date" may already be locked to another type on a shared
//     assistant by some other integration.
//
// Worth reporting upstream: a type conflict is a 400, not a 500, and this
// belongs in the docs.
func sanitizeMetadata(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out["lull_"+k] = stringifyValue(v)
	}
	return out
}

func stringifyValue(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		if t == math.Trunc(t) && math.Abs(t) < 1e15 {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', 2, 64)
	case float32:
		return strconv.FormatFloat(float64(t), 'f', 2, 32)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	default:
		return fmt.Sprint(t)
	}
}

// ---------------------------------------------------------------- reading

// Memory is one recalled fact.
//
// The id arrives under two different names depending on the endpoint: create
// returns "memory_id", list and search return "id". Both are accepted here so
// callers do not have to care which call produced the value.
type Memory struct {
	ID       string  `json:"id"`
	MemoryID string  `json:"memory_id"`
	Content  string  `json:"content"`
	Score    float64 `json:"score"`
}

// Ident returns whichever id field the API populated.
func (m Memory) Ident() string {
	if m.ID != "" {
		return m.ID
	}
	return m.MemoryID
}

// Forget deletes one memory.
func (c *Client) Forget(ctx context.Context, id string) error {
	if !c.Enabled() || id == "" {
		return nil
	}
	return c.do(ctx, http.MethodDelete,
		fmt.Sprintf("/assistants/%s/memories/%s", c.AssistantID(), id), nil, nil)
}

// Search does semantic recall over everything Lull has written about this
// pregnancy.
func (c *Client) Search(ctx context.Context, query string, limit int) ([]Memory, error) {
	if !c.Enabled() {
		return nil, nil
	}
	if limit <= 0 {
		limit = 5
	}
	var out struct {
		Memories []Memory `json:"memories"`
	}
	err := c.do(ctx, http.MethodPost,
		fmt.Sprintf("/assistants/%s/memories/search", c.AssistantID()),
		map[string]any{"query": query, "limit": limit}, &out)
	return out.Memories, err
}

// List returns stored memories, newest page first.
func (c *Client) List(ctx context.Context, pageSize int) ([]Memory, int, error) {
	if !c.Enabled() {
		return nil, 0, nil
	}
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 25
	}
	var out struct {
		Memories   []Memory `json:"memories"`
		TotalCount int      `json:"total_count"`
	}
	err := c.do(ctx, http.MethodGet,
		fmt.Sprintf("/assistants/%s/memories?page=1&page_size=%d", c.AssistantID(), pageSize),
		nil, &out)
	return out.Memories, out.TotalCount, err
}

// Ask sends a question and gets an answer grounded in stored memory.
//
// memory="Readonly" is deliberate for questions: a question is not a fact, and
// letting the model write its own answers back into memory is how a memory
// store fills up with its own speculation.
func (c *Client) Ask(ctx context.Context, question, context_ string) (string, error) {
	if !c.Enabled() {
		return "", fmt.Errorf("narrative memory is not configured")
	}

	content := question
	if context_ != "" {
		content = "Tonight's readings from the device:\n" + context_ + "\n\nHer question: " + question
	}

	body := map[string]any{
		"assistant_id": c.AssistantID(),
		"content":      content,
		"stream":       false,
		"memory":       "Readonly",
	}
	c.mu.RLock()
	tid := c.threadID
	c.mu.RUnlock()
	if tid != "" {
		body["thread_id"] = tid
	}

	var out struct {
		Content  string `json:"content"`
		ThreadID string `json:"thread_id"`
	}
	if err := c.do(ctx, http.MethodPost, "/threads/messages", body, &out); err != nil {
		return "", err
	}
	if out.ThreadID != "" {
		c.mu.Lock()
		c.threadID = out.ThreadID
		c.mu.Unlock()
	}
	return out.Content, nil
}

// ---------------------------------------------------------------- plumbing

func (c *Client) findOrCreateAssistant(ctx context.Context, name string) (string, error) {
	var existing []struct {
		Name        string `json:"name"`
		AssistantID string `json:"assistant_id"`
	}
	if err := c.do(ctx, http.MethodGet, "/assistants", nil, &existing); err == nil {
		for _, a := range existing {
			if strings.EqualFold(a.Name, name) {
				return a.AssistantID, nil
			}
		}
	}

	var created struct {
		AssistantID string `json:"assistant_id"`
	}
	err := c.do(ctx, http.MethodPost, "/assistants", map[string]any{
		"name":          name,
		"description":   "Pregnancy movement companion",
		"system_prompt": systemPrompt,
	}, &created)
	if err != nil {
		return "", err
	}
	if created.AssistantID == "" {
		return "", fmt.Errorf("assistant created but no id returned")
	}
	return created.AssistantID, nil
}

// systemPrompt encodes the one product rule that must never be broken: Lull
// does not reassure. Reassurance is the known failure mode of home fetal
// monitoring, because it delays women from seeking care when movement is down.
const systemPrompt = `You hold the narrative memory of one pregnancy for ` + brand.Product + `, a device that ` +
	`passively counts fetal movement overnight and compares it to this baby's own baseline.

Rules you must never break:
- Never reassure that the baby is fine. There is no "all clear". Reassurance is the known failure
  mode of home fetal monitoring because it delays women from seeking care.
- Never diagnose. Assessment of reduced fetal movement is a CTG and a scan, and belongs to a
  clinician.
- If movement is below her baseline, say so plainly and tell her to contact her maternity unit
  today rather than waiting for her next appointment.

What you are for: continuity. Tell her what changed, what she told you before, and when she was
last seen. She is exhausted and has explained herself many times already. Be brief, warm, and
concrete.`

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", c.key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Never include the request body in an error: it can carry health
		// information, and errors end up in logs.
		return fmt.Errorf("backboard %s %s: HTTP %d: %s",
			method, path, resp.StatusCode, truncate(string(raw), 200))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
