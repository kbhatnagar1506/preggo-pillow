package api

// Contactless maternal vitals, ingested from whatever measured them.
//
// Presage SmartSpectra is SDK-only — iOS, Android, C++, Node — with no REST
// endpoint that takes video and returns vitals. So something has to run the SDK
// against a camera and post the result here. Keeping the ingest generic means
// the bridge can be replaced (a phone app, a laptop, a manual entry) without
// touching anything downstream.

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/kbhatnagar1506/lull/internal/vitals"
)

type vitalsPost struct {
	PulseBPM     float64 `json:"pulse_bpm"`
	BreathingRPM float64 `json:"breathing_rpm"`
	Source       string  `json:"source"`
	Confidence   float64 `json:"confidence"`
}

// handleVitals ingests a reading (POST) or reports the current summary (GET).
func (s *Server) handleVitals(w http.ResponseWriter, r *http.Request) {
	if s.Vitals == nil {
		writeJSON(w, map[string]any{"available": false})
		return
	}

	if r.Method == http.MethodGet {
		sum, ok := s.Vitals.Summarise(10 * time.Minute)
		if !ok {
			writeJSON(w, map[string]any{"available": false})
			return
		}
		writeJSON(w, sum)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
		return
	}

	var in vitalsPost
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	src := vitals.Source(in.Source)
	switch src {
	case vitals.SourcePresage, vitals.SourceAccel, vitals.SourceManual:
	default:
		src = vitals.SourceManual
	}
	reading := vitals.Reading{
		At: time.Now(), PulseBPM: in.PulseBPM, BreathingRPM: in.BreathingRPM,
		Source: src, Confidence: in.Confidence,
	}
	if err := s.Vitals.Add(reading); err != nil {
		// Rejected rather than stored: a zero pulse beside "the baby moved
		// less" reads as a catastrophe rather than a dropped frame.
		writeJSONStatus(w, http.StatusUnprocessableEntity, map[string]any{"error": err.Error()})
		return
	}
	s.Hub.Broadcast(Event{Kind: "vitals", Data: map[string]any{
		"t_ms":          reading.At.UnixMilli(),
		"pulse_bpm":     reading.PulseBPM,
		"breathing_rpm": reading.BreathingRPM,
		"source":        string(reading.Source),
		"cleared":       reading.Source.Cleared(),
	}})
	writeJSON(w, map[string]any{"ok": true})
}

func writeJSONStatus(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
