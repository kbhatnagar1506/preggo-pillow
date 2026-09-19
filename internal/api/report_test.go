package api

import (
	"strings"
	"testing"

	"github.com/kbhatnagar1506/lull/internal/clinical"
	"github.com/kbhatnagar1506/lull/internal/maternal"
)

// The guideline citation has to actually reach the page. Without it the note
// reads as an LLM's opinion; with it, the reader can check the rule against the
// source. That distinction is the whole justification for putting generated
// text in front of a clinician.
func TestReportCitesTheGuidelineBesideTheNote(t *testing.T) {
	if !strings.Contains(reportTmplSrc, "{{.GuidelineRef}}") ||
		!strings.Contains(reportTmplSrc, "{{.GuidelineURL}}") {
		t.Fatal("the report template does not render the guideline citation")
	}
	// It must sit inside the note block: a citation shown when there is no
	// note is citing nothing.
	noteIdx := strings.Index(reportTmplSrc, "{{if .Note}}")
	citeIdx := strings.Index(reportTmplSrc, "{{.GuidelineRef}}")
	endIdx := strings.Index(reportTmplSrc[noteIdx:], "{{end}}") + noteIdx
	if !(noteIdx < citeIdx && citeIdx < endIdx) {
		t.Error("the citation is outside the note block")
	}
}

func TestReportGuidelineValuesComeFromClinical(t *testing.T) {
	if clinical.GuidelineRef == "" || clinical.GuidelineURL == "" {
		t.Fatal("clinical must export the guideline reference")
	}
	if !strings.Contains(clinical.GuidelineRef, "No. 57") {
		t.Errorf("GuidelineRef = %q", clinical.GuidelineRef)
	}
}

// ---- respiration on the page she hands to a midwife ------------------------

// clinical takes the quality as a plain string so it stays a wire format with
// no opinion about accelerometers. That is only safe while the two spellings
// agree, and a silent drift would disable the filter in Summarize without
// failing anything.
func TestRespirationQualityAgreesWithTheTracker(t *testing.T) {
	if clinical.RespirationMeasured != string(maternal.RespMeasured) {
		t.Fatalf("clinical.RespirationMeasured is %q but maternal.RespMeasured is %q: Summarize would strip every rate, or none",
			clinical.RespirationMeasured, maternal.RespMeasured)
	}
}

// "0 / min" on a clinical page reads as a patient who has stopped breathing.
// The tracker means "not measured" by it.
func TestReportNeverPrintsAZeroBreathingRate(t *testing.T) {
	cases := []struct {
		quality maternal.RespirationQuality
		rpm     float64
		want    string
		mustNot string
	}{
		{maternal.RespUnmeasured, 0, "not measured", "0 / min"},
		{maternal.RespProvisional, 8, "not reliably resolved", "8 / min"},
		{maternal.RespMeasured, 14, "14 / min", "not measured"},
		{"", 6, "not measured", "6 / min"},
	}
	for _, c := range cases {
		d := reportData{Maternal: map[string]any{
			"respiration_rpm":     c.rpm,
			"respiration_quality": string(c.quality),
		}}
		got := d.RespirationStr()
		if got != c.want {
			t.Errorf("quality %q at %.0f rpm rendered %q, want %q", c.quality, c.rpm, got, c.want)
		}
		if strings.Contains(got, c.mustNot) {
			t.Errorf("quality %q rendered %q, which must not contain %q", c.quality, got, c.mustNot)
		}
	}
}

// The tracker is the source of these strings; the report must not have invented
// its own spellings for them.
func TestReportRendersEveryTrackerQuality(t *testing.T) {
	for _, q := range []maternal.RespirationQuality{maternal.RespUnmeasured, maternal.RespProvisional, maternal.RespMeasured} {
		d := reportData{Maternal: map[string]any{"respiration_rpm": 14.0, "respiration_quality": string(q)}}
		if got := d.RespirationStr(); got == "" {
			t.Errorf("quality %q rendered nothing", q)
		}
	}
}
