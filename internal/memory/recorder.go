package memory

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/kbhatnagar1506/lull/internal/maternal"

	"github.com/kbhatnagar1506/lull/internal/brand"
)

// Recorder decides what is worth remembering about this pregnancy.
//
// The discipline that makes this useful instead of noise: memories are SALIENT
// EVENTS, not raw data. One memory per detected kick would be thousands of
// identical rows and recall would return garbage. The numbers already live in
// SQLite. Backboard holds what a midwife would actually want to know:
//
//	who she is and how far along
//	what each night came to, against her own baseline
//	when the pattern changed, and what happened next
//	what she noticed herself, in her own words
//	when she was seen, and what she was told
//
// Everything here is de-duplicated and rate-limited, because a memory store
// that repeats itself is a memory store nobody can search.
type Recorder struct {
	c *Client

	mu        sync.Mutex
	lastNight string          // date of the last nightly summary written
	lastAlert string          // date of the last alert written
	seen      map[string]bool // crude de-dupe for profile facts
}

func NewRecorder(c *Client) *Recorder {
	return &Recorder{c: c, seen: map[string]bool{}}
}

func (r *Recorder) enabled() bool { return r != nil && r.c.Enabled() }

// Profile records who she is. Written once per distinct fact, because a
// due date does not change and repeating it pollutes recall.
func (r *Recorder) Profile(name string, gestationalWeeks int, dueDate string) {
	if !r.enabled() {
		return
	}
	who := name
	if who == "" {
		who = "She"
	}

	if gestationalWeeks > 0 {
		r.once(fmt.Sprintf("gest-%d", gestationalWeeks), fmt.Sprintf(
			"%s is %d weeks pregnant as of %s. Monitoring with "+brand.Product+" began at %d weeks.",
			who, gestationalWeeks, time.Now().Format("2 January 2006"), gestationalWeeks),
			map[string]any{"kind": "profile", "gestational_weeks": gestationalWeeks})
	}
	if dueDate != "" {
		r.once("due-"+dueDate, fmt.Sprintf("%s's estimated due date is %s.", who, dueDate),
			map[string]any{"kind": "profile", "due_date": dueDate})
	}
	if name != "" {
		r.once("name-"+name, fmt.Sprintf("The person using this device is called %s.", name),
			map[string]any{"kind": "profile"})
	}
}

// Night records one night's result against her own baseline. Called once per
// night, not once per reading.
func (r *Recorder) Night(date string, count int, baseline float64, deviationPct float64) {
	if !r.enabled() {
		return
	}
	r.mu.Lock()
	if r.lastNight == date {
		r.mu.Unlock()
		return
	}
	r.lastNight = date
	r.mu.Unlock()

	var shape string
	switch {
	case deviationPct <= -25:
		shape = "well below her established baseline"
	case deviationPct <= -10:
		shape = "somewhat below her baseline"
	case deviationPct >= 15:
		shape = "above her baseline"
	default:
		shape = "in line with her baseline"
	}

	r.c.Remember(fmt.Sprintf(
		"On the night of %s the baby moved %d times, which is %s of %.0f (%.0f%%).",
		date, count, shape, baseline, deviationPct),
		map[string]any{
			"kind": "night", "date": date, "count": count,
			"baseline": baseline, "deviation_pct": deviationPct,
		})
}

// Alert records the pattern changing. This is the memory that matters most: it
// is what she will want to point at in a week's time, and what she currently
// has no way to prove.
func (r *Recorder) Alert(date string, count int, baseline float64, deviationPct float64, consecutive int) {
	if !r.enabled() {
		return
	}
	r.mu.Lock()
	if r.lastAlert == date {
		r.mu.Unlock()
		return
	}
	r.lastAlert = date
	r.mu.Unlock()

	r.c.Remember(fmt.Sprintf(
		"ALERT on %s: reduced fetal movement for %d consecutive nights. %d movements against a "+
			"baseline of %.0f, down %.0f%%. She was advised to contact her maternity unit that day "+
			"rather than wait for her next appointment.",
		date, consecutive, count, baseline, -deviationPct),
		map[string]any{
			"kind": "alert", "date": date, "count": count,
			"baseline": baseline, "deviation_pct": deviationPct,
			"consecutive_nights": consecutive,
		})
}

// Maternal records her own state for the night. Posture matters on its own:
// supine going-to-sleep position in late pregnancy carries roughly 2.6x the
// odds of late stillbirth.
//
// It takes the whole Stats rather than six loose float64s so that the rate and
// the confidence in the rate cannot be separated on the way in. What this
// writes is prose that a midwife reads and that semantic recall quotes back
// months later, so a breathing rate stated here becomes a measured fact about
// a patient. The tracker is only willing to stand behind some of the rates it
// produces, and this asks it which.
func (r *Recorder) Maternal(date string, ms maternal.Stats) {
	if !r.enabled() {
		return
	}
	var parts []string
	parts = append(parts, fmt.Sprintf("she slept mostly %s", ms.Posture))
	if ms.SupineMinutes >= 20 {
		parts = append(parts, fmt.Sprintf("with %.0f minutes on her back", ms.SupineMinutes))
	}
	if ms.WakeEvents > 0 {
		parts = append(parts, fmt.Sprintf("waking %d times", ms.WakeEvents))
	}
	switch rpm, ok := ms.RespirationForRecord(); {
	case ok:
		parts = append(parts, fmt.Sprintf("breathing around %.0f a minute", rpm))
	case ms.RespirationQuality == maternal.RespProvisional:
		// Something oscillated, but not well enough to name a rate. Saying so
		// is worth a clause: silence here would read as "breathing was not
		// worth mentioning" when what happened is that the device could not
		// resolve it. The figure itself stays in the metadata below, where it
		// is a diagnostic and not a sentence about her.
		parts = append(parts, "with no reliable breathing rate from the device")
	}
	if ms.SnorePercent >= 5 {
		parts = append(parts, fmt.Sprintf("snoring for %.0f%% of the night", ms.SnorePercent))
	}

	r.c.Remember(fmt.Sprintf("On %s %s.", date, strings.Join(parts, ", ")),
		map[string]any{
			"kind": "maternal", "date": date, "posture": ms.Posture,
			"supine_minutes": ms.SupineMinutes, "wake_events": ms.WakeEvents,
			"respiration_rpm": ms.RespirationRPM, "snore_percent": ms.SnorePercent,
			// Carried so a clinician can see why the sentence above declined
			// to quote a rate, rather than wondering whether one was taken.
			"respiration_quality": string(ms.RespirationQuality),
		})
}

// Note records something she said, in her own words.
//
// This is the half of the problem the sensor cannot reach. "He's been quieter
// since Tuesday" is the thing she currently has to remember, re-explain, and be
// believed about. Verbatim, because paraphrasing a patient is how detail gets
// lost.
func (r *Recorder) Note(text string) {
	if !r.enabled() || strings.TrimSpace(text) == "" {
		return
	}
	r.c.Remember(fmt.Sprintf("On %s she reported, in her own words: %q",
		time.Now().Format("2 January 2006"), strings.TrimSpace(text)),
		map[string]any{"kind": "note", "verbatim": true})
}

// Appointment records being seen, and what she was told. Closing this loop is
// what stops the next appointment starting from nothing.
func (r *Recorder) Appointment(when, what string) {
	if !r.enabled() {
		return
	}
	r.c.Remember(fmt.Sprintf("She was seen on %s. Outcome: %s", when, strings.TrimSpace(what)),
		map[string]any{"kind": "appointment", "date": when})
}

// once writes a fact at most one time per process lifetime.
func (r *Recorder) once(key, content string, meta map[string]any) {
	r.mu.Lock()
	if r.seen[key] {
		r.mu.Unlock()
		return
	}
	r.seen[key] = true
	r.mu.Unlock()
	r.c.Remember(content, meta)
}
