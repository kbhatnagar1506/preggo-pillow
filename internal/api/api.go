// Package api serves the dashboard and streams live events to it.
//
// Streaming uses Server-Sent Events rather than WebSockets: the dashboard only
// ever receives, SSE is one stdlib handler with no dependency, and it
// reconnects on its own. One fewer thing to debug at hour 30.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/kbhatnagar1506/lull/internal/auth"
	"github.com/kbhatnagar1506/lull/internal/clinical"
	"github.com/kbhatnagar1506/lull/internal/kicker"
	"github.com/kbhatnagar1506/lull/internal/maternal"
	"github.com/kbhatnagar1506/lull/internal/memory"
	"github.com/kbhatnagar1506/lull/internal/store"
	"github.com/kbhatnagar1506/lull/internal/vitals"
	"github.com/kbhatnagar1506/lull/internal/voice"
)

// Event is anything pushed to the browser.
type Event struct {
	Kind string `json:"kind"` // detection | command | press | trace | status
	Data any    `json:"data"`
}

// Hub fans events out to every connected dashboard.
type Hub struct {
	mu      sync.RWMutex
	clients map[chan Event]struct{}
}

func NewHub() *Hub {
	return &Hub{clients: make(map[chan Event]struct{})}
}

func (h *Hub) subscribe() chan Event {
	ch := make(chan Event, 128)
	if h == nil {
		return ch
	}
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *Hub) unsubscribe(ch chan Event) {
	if h == nil {
		close(ch)
		return
	}
	h.mu.Lock()
	delete(h.clients, ch)
	h.mu.Unlock()
	close(ch)
}

// Broadcast never blocks. A slow browser drops frames rather than stalling the
// detector, which is the right trade for a live demo.
func (h *Hub) Broadcast(e Event) {
	if h == nil {
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.clients {
		select {
		case ch <- e:
		default:
		}
	}
}

// Server wires the hub, the store and the kicker to HTTP.
type Server struct {
	Hub    *Hub
	Store  *store.Store
	Kicker *kicker.Kicker

	// Auth gates the app behind Auth0 plus our own session. Nil leaves every
	// page open, which is what -source sim with no Auth0 config should do.
	Auth *auth.Service

	// Vitals holds contactless maternal pulse and breathing. Nil simply
	// reports the capability as unavailable.
	Vitals *vitals.Store

	// Voice places the escalation call. Nil or unconfigured simply disables
	// the button; it must never stop the dashboard from serving.
	Voice *voice.Client
	// CallTo is the default number to reach. Single user, single number.
	CallTo string
	// Owner is the path the phone remote is served at, e.g. "krishnabhatnagar".
	Owner string
	// CallToken, when set, is required to place a call. Empty keeps the local
	// behaviour: the button just works.
	CallToken string
	Web       http.FileSystem

	// Device and Source describe this physical unit, so the settings page can
	// report what is actually running rather than what the HTML claims.
	Device string
	Source string

	// Outbound names every service this process sends data to, filled in by
	// main from what is actually configured. The privacy section on the
	// settings page is rendered from this list rather than from a sentence
	// someone typed once and then wired up a database behind.
	Outbound []string

	// Memory and Recorder are the narrative half of the product. Both are safe
	// when nil: Lull works identically without them.
	Memory   *memory.Client
	Recorder *memory.Recorder

	// Clinical writes the provider note from the numeric series. Different job
	// from Memory: numbers, not narrative.
	Clinical *clinical.Summarizer

	// Maternal returns the mother's live metrics. Wired to the tracker so the
	// panel on screen is computed, not typed into the HTML.
	Maternal func() map[string]any

	// Fool injects simulated maternal movement. It lands on every node at
	// once, so a correct detector must reject it. This is the "now watch me
	// try to fool it" beat in the demo.
	Fool func()

	mu           sync.Mutex
	blindStarted time.Time
	blindEnded   time.Time
}

func (s *Server) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	// A nil Web panics inside net/http on the first request, taking the whole
	// server down rather than failing one route. Guard it.
	if s.Web != nil {
		mux.Handle("/", http.FileServer(s.Web))
	} else {
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "dashboard assets not mounted", http.StatusServiceUnavailable)
		})
	}
	// Open: the phone remote runs without a session on purpose, and the
	// Presage bridge posts vitals from a separate process. These carry no
	// stored record — they fire the servo, log a press, or push a reading.
	mux.HandleFunc("/api/stream", s.handleStream)
	mux.HandleFunc("/api/press", s.handlePress)
	mux.HandleFunc("/api/kick", s.handleKick)
	mux.HandleFunc("/api/fool", s.handleFool)
	mux.HandleFunc("/api/call", s.handleCall)
	mux.HandleFunc("/api/vitals", s.handleVitals)
	mux.HandleFunc("/api/blind/start", s.handleBlindStart)
	mux.HandleFunc("/api/blind/stop", s.handleBlindStop)
	mux.HandleFunc("/api/blind/score", s.handleBlindScore)

	// Gated: everything that reads or writes the record. Gating the pages but
	// leaving the data behind them open means the pages were never gated —
	// /report alone is the whole clinical summary, and /api/memories is the
	// written narrative including every alert.
	api := func(h http.HandlerFunc) http.Handler {
		if s.Auth != nil && s.Auth.Enabled() {
			return s.Auth.RequireAPI(h)
		}
		return h
	}
	mux.Handle("/api/nights", api(s.handleNights))
	mux.Handle("/api/maternal", api(s.handleMaternal))
	mux.Handle("/api/memories", api(s.handleMemoryList))
	mux.Handle("/api/note", api(s.handleNote))
	mux.Handle("/api/profile", api(s.handleProfile))
	mux.Handle("/api/appointment", api(s.handleAppointment))
	mux.Handle("/api/ask", api(s.handleAsk))
	mux.Handle("/api/meds", api(s.handleMeds))
	mux.Handle("/api/meds/remove", api(s.handleMedRemove))
	mux.Handle("/api/dose", api(s.handleDose))
	mux.Handle("/api/settings", api(s.handleSettings))
	// The landing page is "/", so the dashboard needs its own path. Serving
	// dashboard.html under a clean URL rather than exposing the file name.
	// Auth routes and the two pages, when configured.
	if s.Auth != nil {
		s.Auth.Routes(mux, func(name string) ([]byte, error) {
			if s.Web == nil {
				return nil, errNoAssets
			}
			f, err := s.Web.Open(name)
			if err != nil {
				return nil, err
			}
			defer f.Close()
			return io.ReadAll(f)
		})
	}

	// The app pages are gated when auth is configured and open when it is
	// not, so a hardware demo with no Auth0 credentials still works.
	//
	// Every link in the sidebar is registered here. A menu item that 404s is
	// worse than a missing menu item: the judge clicks it first.
	gate := func(h http.Handler) http.Handler {
		if s.Auth != nil && s.Auth.Enabled() {
			return s.Auth.Require(h)
		}
		return h
	}
	// The report is the clinical record in full. It is a page, not an API, so
	// an expired session should land on the login screen rather than a blob
	// of JSON.
	mux.Handle("/report", gate(http.HandlerFunc(s.handleReport)))
	for path, file := range map[string]string{
		"/dashboard":   "dashboard.html",
		"/history":     "history.html",
		"/healthcare":  "healthcare.html",
		"/medications": "medications.html",
		"/settings":    "settings.html",
	} {
		mux.Handle(path, gate(s.page(file)))
	}
	// The phone remote lives at the owner's own path. Single user by design:
	// Lull monitors one pregnancy, and a bedside device does not need accounts.
	if s.Owner != "" {
		mux.HandleFunc("/"+s.Owner, s.handleRemote)
		mux.HandleFunc("/"+s.Owner+"/", s.handleRemote)
	}
	return mux
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := s.Hub.subscribe()
	defer s.Hub.unsubscribe(ch)

	// keepalive so proxies and browsers do not time the stream out
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case e := <-ch:
			b, err := json.Marshal(e)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}
	}
}

// handlePress records a human saying "I felt one". In the demo this is the
// blind-test input; in the product it is how the model gets its labels.
func (s *Server) handlePress(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	src := r.URL.Query().Get("source")
	if src == "" {
		src = "ui"
	}
	if err := s.Store.InsertPress(now, src); err != nil {
		log.Printf("press insert: %v", err)
	}
	s.Hub.Broadcast(Event{Kind: "press", Data: map[string]any{
		"t_ms": now.UnixMilli(), "source": src,
	}})
	writeJSON(w, map[string]any{"ok": true})
}

// handleKick is warm-up mode: fire on demand so a judge can trigger one.
func (s *Server) handleKick(w http.ResponseWriter, r *http.Request) {
	strength := r.URL.Query().Get("strength")
	if strength == "" {
		strength = kicker.Medium
	}
	s.Kicker.Fire(strength)
	writeJSON(w, map[string]any{"ok": true, "strength": strength})
}

func (s *Server) handleBlindStart(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.blindStarted = time.Now()
	s.blindEnded = time.Time{}
	s.mu.Unlock()
	s.Kicker.Start()
	s.Hub.Broadcast(Event{Kind: "status", Data: map[string]any{"blind": true}})
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleBlindStop(w http.ResponseWriter, r *http.Request) {
	s.Kicker.Stop()
	s.mu.Lock()
	s.blindEnded = time.Now()
	s.mu.Unlock()
	s.Hub.Broadcast(Event{Kind: "status", Data: map[string]any{"blind": false}})
	writeJSON(w, map[string]any{"ok": true})
}

// handleBlindScore is the reveal: the machine's hits against the human's, over
// the same commanded kicks.
func (s *Server) handleBlindScore(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	from, to := s.blindStarted, s.blindEnded
	s.mu.Unlock()
	if from.IsZero() {
		http.Error(w, "no blind test has been run", http.StatusBadRequest)
		return
	}
	if to.IsZero() {
		to = time.Now()
	}
	score, err := s.Store.ScoreWindow(from, to, 900*time.Millisecond)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	events, err := s.Store.EventsWindow(from, to)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, map[string]any{
		"score":  score,
		"events": events,
		"from":   from.UnixMilli(),
		"to":     to.UnixMilli(),
	})
}

func (s *Server) handleFool(w http.ResponseWriter, r *http.Request) {
	if s.Fool != nil {
		s.Fool()
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) maternalStats() map[string]any {
	if s.Maternal == nil {
		return map[string]any{
			"posture": "unknown", "supine_minutes": 0.0, "respiration_rpm": 0.0,
			"respiration_quality": string(maternal.RespUnmeasured),
			"wake_events":         0, "snore_percent": 0.0, "ready": false,
		}
	}
	return s.Maternal()
}

func (s *Server) handleMaternal(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.maternalStats())
}

func (s *Server) handleNights(w http.ResponseWriter, r *http.Request) {
	nights, err := s.Store.Nights(30)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	baseline, err := s.Store.Baseline(1)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	var latest int
	if len(nights) > 0 {
		latest = nights[len(nights)-1].KickCount
	}
	var deviation float64
	if baseline > 0 {
		deviation = (float64(latest) - baseline) / baseline
	}
	writeJSON(w, map[string]any{
		"nights":    nights,
		"baseline":  baseline,
		"latest":    latest,
		"deviation": deviation,
		// Two consecutive nights, never one. Fetal sleep cycles run 20-40
		// minutes and babies have genuinely quiet nights, so a single-night
		// alarm would be noise.
		"alert": deviation <= -0.25 && consecutiveLow(nights, baseline, 2),
	})
}

func consecutiveLow(nights []store.Night, baseline float64, n int) bool {
	if baseline <= 0 || len(nights) < n {
		return false
	}
	for i := len(nights) - n; i < len(nights); i++ {
		if float64(nights[i].KickCount) > baseline*0.75 {
			return false
		}
	}
	return true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// handleDashboard serves the operator dashboard at a clean path, so "/" can be
// the landing page.
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	s.page("dashboard.html").ServeHTTP(w, r)
}

// page serves one embedded HTML file at a clean URL, so "/" stays the landing
// page and no route leaks a ".html" into the address bar.
func (s *Server) page(file string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Web == nil {
			http.Error(w, "dashboard assets not mounted", http.StatusServiceUnavailable)
			return
		}
		f, err := s.Web.Open(file)
		if err != nil {
			http.Error(w, "page not found", http.StatusNotFound)
			return
		}
		defer f.Close()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.Copy(w, f)
	})
}

// errNoAssets is returned when the embedded web assets are not mounted.
var errNoAssets = errors.New("web assets not mounted")
