package store

import (
	"path/filepath"
	"testing"
	"time"
)

func medStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "meds.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestNormaliseTimesAcceptsWhatAFormActuallyProduces(t *testing.T) {
	got, err := NormaliseTimes(" 8:00PM,08:00 , 20:00,")
	if err != nil {
		t.Fatalf("normalise: %v", err)
	}
	// 8:00PM and 20:00 are the same reminder; two alarms at one minute is one
	// alarm, and showing it twice would make the adherence denominator wrong.
	want := []string{"08:00", "20:00"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestNormaliseTimesRefusesNonsenseRatherThanDroppingIt(t *testing.T) {
	for _, in := range []string{"", "   ", "breakfast", "25:00"} {
		if _, err := NormaliseTimes(in); err == nil {
			t.Errorf("NormaliseTimes(%q) accepted; a silently dropped reminder is a missed dose", in)
		}
	}
}

func TestDosesMaterialiseForTodayButNotForTomorrow(t *testing.T) {
	s := medStore(t)
	if _, err := s.AddMedication("Iron", "65 mg", []string{"09:00", "21:00"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	today := "2026-09-19"
	doses, err := s.DosesOn(today, today)
	if err != nil {
		t.Fatalf("doses: %v", err)
	}
	if len(doses) != 2 {
		t.Fatalf("got %d doses today, want 2", len(doses))
	}

	// Tomorrow is listed so she can see what is coming, but nothing is owed
	// yet, so no row is written and the adherence denominator does not move.
	if _, err := s.DosesOn("2026-09-20", today); err != nil {
		t.Fatalf("tomorrow: %v", err)
	}
	_, total, err := s.Adherence(7, today)
	if err != nil {
		t.Fatalf("adherence: %v", err)
	}
	if total != 2 {
		t.Errorf("adherence denominator %d; tomorrow's doses must not count as owed today", total)
	}
}

func TestMarkDoseIsReversible(t *testing.T) {
	s := medStore(t)
	id, err := s.AddMedication("Prenatal", "", []string{"08:00"})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	today := time.Now().Format("2006-01-02")
	if err := s.MarkDose(id, today, "08:00", "taken"); err != nil {
		t.Fatalf("mark: %v", err)
	}
	doses, _ := s.DosesOn(today, today)
	if doses[0].TakenMs == 0 {
		t.Fatal("dose not recorded as taken")
	}
	// A mistap has to be undoable, or people stop using the button honestly
	// and the number stops meaning anything.
	if err := s.MarkDose(id, today, "08:00", "due"); err != nil {
		t.Fatalf("undo: %v", err)
	}
	doses, _ = s.DosesOn(today, today)
	if doses[0].TakenMs != 0 || doses[0].Skipped {
		t.Errorf("undo left state %+v", doses[0])
	}
}

// The property that matters: deleting a schedule must not improve the past.
func TestRemovingAMedicationKeepsWhatWasAlreadyRecorded(t *testing.T) {
	s := medStore(t)
	id, _ := s.AddMedication("Iron", "", []string{"09:00", "21:00"})
	today := time.Now().Format("2006-01-02")
	if _, err := s.DosesOn(today, today); err != nil {
		t.Fatalf("doses: %v", err)
	}
	if err := s.MarkDose(id, today, "09:00", "taken"); err != nil {
		t.Fatalf("mark: %v", err)
	}
	if err := s.RemoveMedication(id); err != nil {
		t.Fatalf("remove: %v", err)
	}
	taken, total, err := s.Adherence(7, today)
	if err != nil {
		t.Fatalf("adherence: %v", err)
	}
	if taken != 1 || total != 2 {
		t.Errorf("after removal adherence is %d/%d; want 1/2 — deleting the plan must not rewrite the history", taken, total)
	}
}

func TestAdherenceCountsOnlyDaysThatHaveArrived(t *testing.T) {
	s := medStore(t)
	if _, err := s.AddMedication("Iron", "", []string{"09:00"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	today := "2026-09-19"
	if _, err := s.DosesOn(today, today); err != nil {
		t.Fatalf("doses: %v", err)
	}
	_, total, err := s.Adherence(7, today)
	if err != nil {
		t.Fatalf("adherence: %v", err)
	}
	// Six earlier days exist in the window but were never looked at, so no
	// rows were written for them. A pillow unplugged for a week must not wake
	// up and claim she missed doses it never asked her about.
	if total != 1 {
		t.Errorf("total %d; want 1", total)
	}
}
