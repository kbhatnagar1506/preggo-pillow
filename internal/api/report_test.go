package api

import (
	"strings"
	"testing"

	"github.com/kbhatnagar1506/lull/internal/clinical"
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
