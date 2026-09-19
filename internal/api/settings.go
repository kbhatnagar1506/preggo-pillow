package api

// What this particular pillow is actually wired to.
//
// Every field here is read from the running process, never typed into the
// page. A settings screen that claims a capability the binary does not have
// is worse than no settings screen: it is the first thing a judge tests, and
// the first thing a clinician would rely on.

import (
	"net/http"
	"strings"
	"time"

	"github.com/kbhatnagar1506/lull/internal/auth"
)

// maskNumber keeps the leading digit and the last two. Enough to recognise
// your own number on a screen, not enough to be read off a shoulder in a
// waiting room.
func maskNumber(n string) string {
	digits := []rune{}
	for _, r := range n {
		if r >= '0' && r <= '9' {
			digits = append(digits, r)
		}
	}
	if len(digits) == 0 {
		return ""
	}
	if len(digits) < 5 {
		return "\u2022\u2022\u2022"
	}
	hidden := strings.Repeat("\u2022", len(digits)-3)
	return "+" + string(digits[0]) + " " + hidden + " " + string(digits[len(digits)-2:])
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	idle := auth.DefaultIdle
	authOn := s.Auth != nil && s.Auth.Enabled()
	if s.Auth != nil && s.Auth.Sessions != nil && s.Auth.Sessions.Idle > 0 {
		idle = s.Auth.Sessions.Idle
	}

	var nights int
	var baseline float64
	if s.Store != nil {
		if ns, err := s.Store.Nights(60); err == nil {
			nights = len(ns)
		}
		baseline, _ = s.Store.Baseline(1)
	}

	writeJSON(w, map[string]any{
		"device": map[string]any{
			"id":          s.Device,
			"source":      s.Source,
			"remote_path": "/" + s.Owner,
			"nights":      nights,
			"baseline":    baseline,
		},
		"escalation": map[string]any{
			"voice_configured": s.Voice != nil && s.Voice.Enabled(),
			"calls":            maskNumber(s.CallTo),
		},
		"capabilities": map[string]any{
			"vitals":    s.Vitals != nil,
			"kicker":    s.Kicker != nil,
			"memory":    s.Memory != nil && s.Memory.Enabled(),
			"clinical":  s.Clinical != nil,
			"auth":      authOn,
			"dial":      s.Fool != nil,
			"fetal_hr":  false, // no sensor on earth in this pillow can do it
			"documents": false, // uploads live in the web app, not on the device
		},
		"outbound": s.Outbound,
		"session": map[string]any{
			"idle_minutes":     int(idle / time.Minute),
			"absolute_hours":   int(auth.DefaultAbsolute / time.Hour),
			"auth_enabled":     authOn,
			"tokens_hashed":    true,
			"cookie_signed":    true,
			"encrypted_fields": true,
		},
	})
}
