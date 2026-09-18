package api

import (
	"fmt"
	"html/template"
	"net/http"
	"time"

	"github.com/kbhatnagar1506/lull/internal/store"
)

// handleReport renders the one page she brings to her appointment.
//
// This is the actual deliverable of the product. The device does not diagnose
// and never says the baby is fine; it produces a record that did not exist
// before, so that "he's been quieter" stops being something she has to be
// believed about.
//
// Printable straight from the browser. Do not build a PDF library at hour 20.
func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	nights, err := s.Store.Nights(14)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	baseline, err := s.Store.Baseline(1)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
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

	data := reportData{
		Generated: time.Now().Format("2 January 2006, 15:04"),
		Nights:    nights,
		Baseline:  int(baseline),
		Latest:    latest.KickCount,
		Deviation: deviation,
		Alert:     alert,
		Simulated: simulated,
		Maternal:  s.maternalStats(),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := reportTmpl.Execute(w, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

type reportData struct {
	Generated string
	Nights    []store.Night
	Baseline  int
	Latest    int
	Deviation float64
	Alert     bool
	Simulated bool
	Maternal  map[string]any
}

func (d reportData) DeviationStr() string {
	return fmt.Sprintf("%+.0f%%", d.Deviation)
}

var reportTmpl = template.Must(template.New("report").Parse(`<!doctype html>
<meta charset="utf-8">
<title>Lull — record for your appointment</title>
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
  .note{background:#f6f7f9;border-left:3px solid #ccc;padding:12px 14px;
        font-size:13px;color:#444;margin-top:26px}
  .simtag{display:inline-block;padding:2px 7px;border-radius:4px;background:#fff3cd;
          color:#7a5c00;font-size:10px;letter-spacing:.08em;text-transform:uppercase}
  @media print{body{margin:0}.noprint{display:none}}
</style>

<h1>Lull</h1>
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
  <tr><td>Respiration</td><td>{{printf "%.0f" .Maternal.respiration_rpm}} / min</td></tr>
  <tr><td>Wake events</td><td>{{.Maternal.wake_events}}</td></tr>
  <tr><td>Snore burden</td><td>{{printf "%.1f" .Maternal.snore_percent}}%</td></tr>
</table>

<div class="note">
  <strong>What this is, and what it is not.</strong>
  Lull is not a medical device and does not diagnose. It counts fetal movement
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
`))
