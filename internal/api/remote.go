package api

// The phone remote. One page, served at the owner's own path, designed to be
// held in a hand and tapped by a judge while the count moves on a laptop
// across the table.
//
// Single user on purpose. Lull monitors one pregnancy; there is no account
// system and there should not be one for a device that sits on a bedside
// table. The path IS the identity.

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/kbhatnagar1506/lull/internal/voice"
)

// handleRemote serves the phone control page.
func (s *Server) handleRemote(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")

	// The page is public by design — a bedside remote has no account. That is
	// fine while it only fires a servo. It stops being fine the moment a live
	// Vapi key is on the box, because then anyone who finds the URL can ring
	// a real person's phone, repeatedly, at someone else's expense.
	//
	// So when CallToken is set, the call button only receives it if the
	// request already knew it. Everything else on the page still works.
	token := ""
	if s.CallToken != "" && subtle.ConstantTimeCompare(
		[]byte(r.URL.Query().Get("k")), []byte(s.CallToken)) == 1 {
		token = s.CallToken
	}
	fmt.Fprint(w, strings.ReplaceAll(remotePage, "{{CALL_TOKEN}}", template.HTMLEscapeString(token)))
}

// callAuthorised reports whether this request may place a call. With no token
// configured nothing changes: the local demo and the Pi keep working.
func (s *Server) callAuthorised(r *http.Request) bool {
	if s.CallToken == "" {
		return true
	}
	given := r.URL.Query().Get("k")
	if given == "" {
		given = r.Header.Get("X-Call-Token")
	}
	return subtle.ConstantTimeCompare([]byte(given), []byte(s.CallToken)) == 1
}

type callResponse struct {
	OK     bool   `json:"ok"`
	CallID string `json:"call_id,omitempty"`
	Status string `json:"status,omitempty"`
	To     string `json:"to,omitempty"`
	Error  string `json:"error,omitempty"`
}

// handleCall places the escalation call.
//
// POST only. A phone call is not something a link preview, a prefetch or a
// crawler should be able to trigger by fetching a URL.
func (s *Server) handleCall(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(callResponse{Error: "POST only"})
		return
	}
	if !s.callAuthorised(r) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(callResponse{Error: "not authorised to place calls from here"})
		return
	}
	if s.Voice == nil || !s.Voice.Enabled() {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(callResponse{
			Error: "voice calling is not configured (VAPI_API_KEY, VAPI_PHONE_NUMBER_ID)"})
		return
	}

	to := strings.TrimSpace(r.URL.Query().Get("to"))
	if to == "" {
		to = s.CallTo
	}
	if to == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(callResponse{Error: "no number to call"})
		return
	}

	// Pull the real numbers so the call describes what was actually measured,
	// using the same baseline the dashboard and the report use. A call that
	// quotes a different figure from the screen destroys trust in both.
	alert := voice.Alert{To: to, CustomMessage: r.URL.Query().Get("message")}
	if s.Store != nil {
		nights, nerr := s.Store.Nights(14)
		baseline, berr := s.Store.Baseline(1)
		if nerr == nil && berr == nil && baseline > 0 && len(nights) > 0 {
			latest := float64(nights[len(nights)-1].KickCount)
			if latest < baseline {
				alert.DeviationPct = (baseline - latest) / baseline * 100
			}
			for n := len(nights); n >= 1; n-- {
				if consecutiveLow(nights, baseline, n) {
					alert.Nights = n
					break
				}
			}
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	res, err := s.Voice.Place(ctx, alert)
	if err != nil {
		log.Printf("call: %v", err)
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(callResponse{Error: err.Error()})
		return
	}
	log.Printf("call placed to %s: id=%s status=%s", to, res.ID, res.Status)
	s.Hub.Broadcast(Event{Kind: "call", Data: map[string]any{
		"t_ms": time.Now().UnixMilli(), "to": to, "status": res.Status,
	}})
	_ = json.NewEncoder(w).Encode(callResponse{OK: true, CallID: res.ID, Status: res.Status, To: to})
}
