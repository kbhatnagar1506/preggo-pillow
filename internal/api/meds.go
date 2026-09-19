package api

// Medication reminders.
//
// Real rows in the same SQLite file as the detections, not browser storage: a
// reminder that lives in one phone's localStorage is not a reminder, it is a
// note that disappears when she opens the page on the laptop instead.

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kbhatnagar1506/lull/internal/store"
)

// medsToday resolves the local date the page is asking about. The browser
// sends its own zone, because a pillow shipped to another timezone must not
// decide that 11pm Tuesday is already Wednesday.
func medsToday(r *http.Request) string {
	loc := time.Local
	if tz := r.URL.Query().Get("tz"); tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}
	return time.Now().In(loc).Format("2006-01-02")
}

func (s *Server) handleMeds(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeJSON(w, map[string]any{"available": false})
		return
	}
	today := medsToday(r)

	if r.Method == http.MethodGet {
		date := strings.TrimSpace(r.URL.Query().Get("date"))
		if date == "" {
			date = today
		}
		meds, err := s.Store.Medications()
		if err != nil {
			httpError(w, err, http.StatusInternalServerError)
			return
		}
		doses, err := s.Store.DosesOn(date, today)
		if err != nil {
			httpError(w, err, http.StatusInternalServerError)
			return
		}
		taken, total, err := s.Store.Adherence(7, today)
		if err != nil {
			httpError(w, err, http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{
			"available":   true,
			"today":       today,
			"date":        date,
			"medications": meds,
			"doses":       doses,
			"adherence":   map[string]int{"taken": taken, "total": total, "days": 7},
		})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
		return
	}

	var body struct {
		Name  string `json:"name"`
		Dose  string `json:"dose"`
		Times string `json:"times"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	times, err := store.NormaliseTimes(body.Times)
	if err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	id, err := s.Store.AddMedication(body.Name, body.Dose, times)
	if err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "id": id})
}

// handleMedRemove drops a schedule. POST only: a crawler following a link
// must not be able to delete a medication.
func (s *Server) handleMedRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if s.Store == nil {
		writeJSON(w, map[string]any{"available": false})
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("id")), 10, 64)
	if err != nil {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	if err := s.Store.RemoveMedication(id); err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleDose records taken / skipped / due for one scheduled dose.
func (s *Server) handleDose(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if s.Store == nil {
		writeJSON(w, map[string]any{"available": false})
		return
	}
	var body struct {
		MedID int64  `json:"med_id"`
		Date  string `json:"date"`
		Time  string `json:"time"`
		State string `json:"state"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if body.Date == "" {
		body.Date = medsToday(r)
	}
	if body.MedID == 0 || body.Time == "" {
		http.Error(w, "med_id and time required", http.StatusBadRequest)
		return
	}
	if err := s.Store.MarkDose(body.MedID, body.Date, body.Time, body.State); err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func httpError(w http.ResponseWriter, err error, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
}
