package api

import (
	"context"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"time"

	"github.com/kbhatnagar1506/lull/internal/brand"
	"github.com/kbhatnagar1506/lull/internal/clinical"
	"github.com/kbhatnagar1506/lull/internal/maternal"
	"github.com/kbhatnagar1506/lull/internal/store"
)

func str(v any) string { s, _ := v.(string); return s }

func f64(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int:
		return float64(t)
	}
	return 0
}

// handleReport renders the one page she brings to her appointment.
//
// This is the actual deliverable of the product. The device does not diagnose
// and never says the baby is fine; it produces a record that did not exist
// before, so that "he's been quieter" stops being something she has to be
// believed about.
//
// Printable straight from the browser. Do not build a PDF library at hour 20.
func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	// No database is unavailability, not a broken request: -source phone runs
	// the whole product without one.
	nights, err := s.Store.Nights(14)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	baseline, err := s.Store.Baseline(1)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	var latest store.Night
	if len(nights) > 0 {
		latest = nights[len(nights)-1]
	}
	var deviation float64
	if baseline > 0 {
		deviation = (float64(latest.KickCount) - baseline) / baseline * 100
	}

	alert := deviation <= -25 && consecutiveLow(nights, baseline, 2)

	var simulated bool
	for _, n := range nights {
		if n.Simulated {
			simulated = true
			break
		}
	}

	// The written note is best-effort. The report renders without it.
	var note string
	if s.Clinical != nil && s.Clinical.Enabled() {
		pts := make([]clinical.NightPoint, 0, len(nights))
		for _, n := range nights {
			pts = append(pts, clinical.NightPoint{Date: n.Date, Count: n.KickCount})
		}
		m := s.maternalStats()
		// Attach measured maternal vitals when we have them. This is what lets
		// the note say "her numbers are normal AND the baby moved less" — the
		// control that separates a change in the baby from a change in her.
		var mv *clinical.MaternalVitals
		if s.Vitals != nil {
			if sum, ok := s.Vitals.Summarise(12 * time.Hour); ok {
				mv = &clinical.MaternalVitals{
					PulseBPM: sum.PulseBPM, BreathingRPM: sum.BreathingRPM,
					Source: string(sum.Source), Cleared: sum.Cleared,
					Accuracy: sum.Accuracy, Normal: sum.Normal,
				}
			}
		}
		cctx, ccancel := context.WithTimeout(r.Context(), 60*time.Second)
		n, err := s.Clinical.Summarize(cctx, clinical.Input{
			Nights: pts, Baseline: baseline, Tonight: latest.KickCount,
			DeviationPct:  deviation,
			Posture:       str(m["posture"]),
			SupineMinutes: f64(m["supine_minutes"]),
			// The quality travels with the rate, and Summarize withholds the
			// rate itself unless the tracker stands behind it.
			RespirationRPM:     f64(m["respiration_rpm"]),
			RespirationQuality: str(m["respiration_quality"]),
			SnorePercent:       f64(m["snore_percent"]),
			WakeEvents:         int(f64(m["wake_events"])),
			Alert:              alert,
			MaternalVitals:     mv,
		})
		ccancel()
		if err != nil {
			log.Printf("clinical summary: %v", err)
		} else {
			note = n
		}
	}

	data := reportData{
		Generated: time.Now().Format("2 January 2006, 15:04"),
		Nights:    nights,
		Baseline:  int(baseline),
		Latest:    latest.KickCount,
		Deviation: deviation,
		Alert:     alert,
		Simulated: simulated,
		Maternal:  s.maternalStats(),
		Note:      note,
		// Shown beside the note so the rule being applied is visible and
		// checkable. An LLM writing medical-sounding prose is worthless; an
		// LLM applying a named published rule to measured numbers is not, and
		// the difference has to be legible to the reader.
		GuidelineRef: clinical.GuidelineRef,
		GuidelineURL: clinical.GuidelineURL,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := reportTmpl.Execute(w, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

type reportData struct {
	Generated    string
	Nights       []store.Night
	Baseline     int
	Latest       int
	Deviation    float64
	Alert        bool
	Simulated    bool
	Maternal     map[string]any
	Note         string
	GuidelineRef string
	GuidelineURL string
}

// Product is read from the brand package rather than typed into the markup,
// so the record a midwife reads carries the same name as the app it came from.
func (d reportData) Product() string { return brand.Product }

func (d reportData) DeviationStr() string {
	return fmt.Sprintf("%+.0f%%", d.Deviation)
}

// reportTmplSrc is named rather than inline so tests can assert on it. The
// clinical guard rails live in this markup as much as in the prompt.
const reportTmplSrc = `<!doctype html>
<meta charset="utf-8">
<title>{{.Product}} &mdash; record for your appointment</title>
<style>
  body{max-width:720px;margin:36px auto;padding:0 24px;
       font:15px/1.6 -apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif;color:#111}
  h1{font-size:20px;letter-spacing:.14em;text-transform:uppercase;margin:0}
  .sub{color:#666;font-size:13px;margin:4px 0 26px}
  .banner{padding:14px 16px;border-radius:8px;margin:20px 0;font-size:15px}
  .banner.alert{background:#fdecea;border:1px solid #f1a9a0;color:#8a1c12}
  .banner.ok{background:#eaf6ec;border:1px solid #a9d8b4;color:#1c5a2a}
  table{width:100%;border-collapse:collapse;margin:18px 0;font-size:14px}
  th,td{text-align:left;padding:7px 0;border-bottom:1px solid #eee}
  td:last-child,th:last-child{text-align:right;font-variant-numeric:tabular-nums}
  .low{color:#b3261e;font-weight:600}
  h2{font-size:11px;letter-spacing:.14em;text-transform:uppercase;color:#666;
     margin:28px 0 8px;font-weight:600}
  .cite{font-size:12px;color:#5b6472;margin-top:-6px;line-height:1.5}
  .cite a{color:#41506b}
  .note{background:#f6f7f9;border-left:3px solid #ccc;padding:12px 14px;
        font-size:13px;color:#444;margin-top:26px}
  .simtag{display:inline-block;padding:2px 7px;border-radius:4px;background:#fff3cd;
          color:#7a5c00;font-size:10px;letter-spacing:.08em;text-transform:uppercase}
  @media print{body{margin:0}.noprint{display:none}}
</style>

<h1>{{.Product}}</h1>
<div class="sub">Record for your appointment &middot; generated {{.Generated}}
{{if .Simulated}}<span class="simtag">contains simulated data</span>{{end}}</div>

{{if .Alert}}
<div class="banner alert">
  <strong>Reduced fetal movement relative to this baby's established baseline,
  two consecutive nights.</strong><br>
  Contact your maternity unit today and ask for assessment. Do not wait for your
  next scheduled appointment.
</div>
{{else}}
<div class="banner ok">
  Movement over the last 14 nights is consistent with this baby's established
  baseline.
</div>
{{end}}

{{if .Note}}
<h2>Summary</h2>
<p>{{.Note}}</p>
<p class="cite">Assessed against <a href="{{.GuidelineURL}}">{{.GuidelineRef}}</a>,
which advises comparison with the baby&rsquo;s own established pattern rather than
any fixed count, and earlier review where reduced movement recurs.</p>
{{end}}

<h2>Fetal movement</h2>
<table>
  <tr><td>Established baseline (median of prior nights)</td><td>{{.Baseline}} / night</td></tr>
  <tr><td>Most recent night</td><td>{{.Latest}}</td></tr>
  <tr><td>Change from baseline</td><td class="{{if .Alert}}low{{end}}">{{.DeviationStr}}</td></tr>
</table>

<h2>Nightly counts</h2>
<table>
  <tr><th>Night</th><th>Movements</th></tr>
  {{range .Nights}}
  <tr><td>{{.Date}}{{if .Simulated}} <span class="simtag">sim</span>{{end}}</td><td>{{.KickCount}}</td></tr>
  {{end}}
</table>

<h2>Maternal, most recent night</h2>
<table>
  <tr><td>Posture</td><td>{{.Maternal.posture}}</td></tr>
  <tr><td>Time supine</td><td>{{printf "%.0f" .Maternal.supine_minutes}} min</td></tr>
  <tr><td>Respiration</td><td>{{.RespirationStr}}</td></tr>
  <tr><td>Wake events</td><td>{{.Maternal.wake_events}}</td></tr>
  <tr><td>Snore burden</td><td>{{printf "%.1f" .Maternal.snore_percent}}%</td></tr>
</table>

<div class="note">
  <strong>What this is, and what it is not.</strong>
  {{.Product}} is not a medical device and does not diagnose. It counts fetal movement
  passively overnight and reports change relative to this baby's own established
  pattern, rather than against a population threshold.
  <br><br>
  It deliberately never reports that the baby is fine. Reassurance is the known
  failure mode of home fetal monitoring: it delays women from seeking care.
  This page only ever reports that the pattern changed.
  <br><br>
  Assessment of reduced fetal movement is a clinical decision and belongs with
  your midwife or obstetrician.
</div>

<p class="noprint" style="margin-top:24px">
  <button onclick="print()">Print this page</button>
</p>
`

var reportTmpl = template.Must(template.New("report").Parse(reportTmplSrc))

// RespirationStr renders the breathing row on the page she hands to a midwife.
//
// The tracker reports 0 to mean "not measured", so printing the raw figure put
// "0 / min" on a clinical page, which reads as a patient who has stopped
// breathing. A rate the tracker would not stand behind is not shown either:
// this page is read as a record of the night, and a marginal estimate printed
// in a table is indistinguishable from a measured one.
func (d reportData) RespirationStr() string {
	switch maternal.RespirationQuality(str(d.Maternal["respiration_quality"])) {
	case maternal.RespMeasured:
		return fmt.Sprintf("%.0f / min", f64(d.Maternal["respiration_rpm"]))
	case maternal.RespProvisional:
		return "not reliably resolved"
	}
	return "not measured"
}
