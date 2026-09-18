package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// Memory endpoints. All of them degrade to a clear message rather than an error
// page when Backboard is not configured, because Lull must work without it.

func (s *Server) handleMemoryList(w http.ResponseWriter, r *http.Request) {
	if s.Memory == nil || !s.Memory.Enabled() {
		writeJSON(w, map[string]any{"enabled": false, "memories": []any{}, "total": 0})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	mems, total, err := s.Memory.List(ctx, 50)
	if err != nil {
		writeJSON(w, map[string]any{"enabled": true, "error": err.Error(), "memories": []any{}})
		return
	}
	writeJSON(w, map[string]any{
		"enabled":      true,
		"assistant_id": s.Memory.AssistantID(),
		"memories":     mems,
		"total":        total,
	})
}

// handleNote stores what she said, in her own words. This is the half of the
// problem the sensor cannot reach.
func (s *Server) handleNote(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text string `json:"text"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	if body.Text == "" {
		body.Text = r.URL.Query().Get("text")
	}
	if body.Text == "" {
		http.Error(w, "no text", http.StatusBadRequest)
		return
	}
	if s.Recorder != nil {
		s.Recorder.Note(body.Text)
	}
	s.Hub.Broadcast(Event{Kind: "note", Data: map[string]any{
		"t_ms": time.Now().UnixMilli(), "text": body.Text,
	}})
	writeJSON(w, map[string]any{"ok": true})
}

// handleProfile records who she is and how far along.
func (s *Server) handleProfile(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name    string `json:"name"`
		Weeks   int    `json:"weeks"`
		DueDate string `json:"due_date"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	if s.Recorder != nil {
		s.Recorder.Profile(body.Name, body.Weeks, body.DueDate)
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleAppointment closes the loop: she was seen, and this is what she was
// told. Without it, the next appointment starts from nothing.
func (s *Server) handleAppointment(w http.ResponseWriter, r *http.Request) {
	var body struct {
		When string `json:"when"`
		What string `json:"what"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	if body.When == "" {
		body.When = time.Now().Format("2 January 2006")
	}
	if body.What == "" {
		http.Error(w, "no outcome given", http.StatusBadRequest)
		return
	}
	if s.Recorder != nil {
		s.Recorder.Appointment(body.When, body.What)
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleAsk answers a question using stored memory plus tonight's readings.
//
// The device knows the numbers. The memory knows the pregnancy. This endpoint
// is where those meet, and it is the thing she cannot do today: ask "has he
// been quieter?" and get an answer grounded in her own history rather than her
// own recollection.
func (s *Server) handleAsk(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Question string `json:"question"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	if body.Question == "" {
		body.Question = r.URL.Query().Get("q")
	}
	if body.Question == "" {
		http.Error(w, "no question", http.StatusBadRequest)
		return
	}
	if s.Memory == nil || !s.Memory.Enabled() {
		writeJSON(w, map[string]any{
			"answer": "Narrative memory is not configured, so I can only show you the numbers on screen.",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()

	answer, err := s.Memory.Ask(ctx, body.Question, s.tonightContext())
	if err != nil {
		writeJSON(w, map[string]any{"answer": "", "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"answer": answer})
}

// tonightContext is the live sensor state, handed to the assistant so its
// answer is grounded in tonight rather than only in what it remembers.
func (s *Server) tonightContext() string {
	nights, err := s.Store.Nights(14)
	if err != nil || len(nights) == 0 {
		return ""
	}
	baseline, _ := s.Store.Baseline(1)
	latest := nights[len(nights)-1]

	var dev float64
	if baseline > 0 {
		dev = (float64(latest.KickCount) - baseline) / baseline * 100
	}

	m := s.maternalStats()
	b, _ := json.Marshal(map[string]any{
		"date":              latest.Date,
		"movements_tonight": latest.KickCount,
		"her_baseline":      int(baseline),
		"deviation_percent": int(dev),
		"maternal":          m,
	})
	return string(b)
}
