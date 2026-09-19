package store

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// base is a fixed instant so tests never depend on wall-clock timing.
var base = time.Date(2026, 9, 18, 22, 0, 0, 0, time.UTC)

func at(ms int) time.Time { return base.Add(time.Duration(ms) * time.Millisecond) }

// ---------------------------------------------------------------- scoring
//
// ScoreWindow is the query behind the blind test, which is the number shown to
// judges. A bug here would fabricate an accuracy claim, so it gets the most
// tests in this package.

func TestScoreWindowPerfectMachine(t *testing.T) {
	s := open(t)
	for i := 0; i < 5; i++ {
		ms := i * 2000
		mustCommand(t, s, at(ms), "medium")
		mustDetection(t, s, at(ms+120)) // well inside tolerance
	}

	sc, err := s.ScoreWindow(at(-1000), at(20000), 900*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if sc.Commands != 5 || sc.MachineHits != 5 || sc.MachineMissed != 0 {
		t.Errorf("got %d/%d hits (missed %d), want 5/5", sc.MachineHits, sc.Commands, sc.MachineMissed)
	}
	if sc.MachineFalsePos != 0 {
		t.Errorf("got %d false positives, want 0", sc.MachineFalsePos)
	}
	if sc.MachineRate != 1 {
		t.Errorf("rate %v, want 1", sc.MachineRate)
	}
}

// A detection outside the tolerance window is NOT a hit, and it is also a false
// positive. Getting this wrong would let a late detection count twice in our
// favour.
func TestScoreWindowLateDetectionIsMissAndFalsePositive(t *testing.T) {
	s := open(t)
	mustCommand(t, s, at(0), "weak")
	mustDetection(t, s, at(5000)) // 5s later: far outside 900ms

	sc, err := s.ScoreWindow(at(-1000), at(20000), 900*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if sc.MachineHits != 0 {
		t.Errorf("hits %d, want 0: a detection 5s late is not a hit", sc.MachineHits)
	}
	if sc.MachineFalsePos != 1 {
		t.Errorf("false positives %d, want 1", sc.MachineFalsePos)
	}
}

func TestScoreWindowToleranceBoundary(t *testing.T) {
	cases := []struct {
		name     string
		offsetMS int
		wantHit  bool
	}{
		{"just inside, after", 850, true},
		{"just inside, before", -850, true},
		{"outside, after", 1200, false},
		{"outside, before", -1200, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := open(t)
			mustCommand(t, s, at(10000), "medium")
			mustDetection(t, s, at(10000+c.offsetMS))

			sc, err := s.ScoreWindow(at(0), at(30000), 900*time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			got := sc.MachineHits == 1
			if got != c.wantHit {
				t.Errorf("offset %dms: hit=%v, want %v", c.offsetMS, got, c.wantHit)
			}
		})
	}
}

// The human gets a wider, ASYMMETRIC window: people react after a kick, never
// before it. A press 200ms BEFORE a kick must not be credited, or the judge's
// score would be flattered by random pressing.
func TestScoreWindowHumanToleranceIsAsymmetric(t *testing.T) {
	s := open(t)
	mustCommand(t, s, at(10000), "medium")
	mustPress(t, s, at(10000+1500)) // 1.5s after: a plausible human reaction

	sc, err := s.ScoreWindow(at(0), at(30000), 900*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if sc.HumanHits != 1 {
		t.Errorf("press 1.5s after the kick: hits %d, want 1", sc.HumanHits)
	}

	s2 := open(t)
	mustCommand(t, s2, at(10000), "medium")
	mustPress(t, s2, at(10000-900)) // pressed BEFORE the kick happened
	sc2, err := s2.ScoreWindow(at(0), at(30000), 900*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if sc2.HumanHits != 0 {
		t.Errorf("press 900ms BEFORE the kick counted as a hit; humans do not anticipate")
	}
}

// Events outside the blind-test window must not leak into its score, or a
// warm-up kick fired before the test would inflate the result.
func TestScoreWindowExcludesOutsideEvents(t *testing.T) {
	s := open(t)
	mustCommand(t, s, at(-50000), "medium") // warm-up, long before
	mustDetection(t, s, at(-50000+100))
	mustCommand(t, s, at(10000), "medium") // inside
	mustDetection(t, s, at(10100))

	sc, err := s.ScoreWindow(at(0), at(30000), 900*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if sc.Commands != 1 {
		t.Errorf("commands %d, want 1: the warm-up kick leaked into the window", sc.Commands)
	}
}

func TestScoreWindowEmptyIsNotADivideByZero(t *testing.T) {
	s := open(t)
	sc, err := s.ScoreWindow(at(0), at(1000), 900*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if sc.Commands != 0 || sc.MachineRate != 0 || sc.HumanRate != 0 {
		t.Errorf("empty window: %+v", sc)
	}
}

// ---------------------------------------------------------------- baseline

// Median, not mean: one restless night must not move the bar an alert is
// measured against.
func TestBaselineUsesMedianNotMean(t *testing.T) {
	s := open(t)
	// One wild outlier that would drag a mean upward.
	counts := []int{300, 310, 320, 330, 5000}
	for i, c := range counts {
		day := base.AddDate(0, 0, -(len(counts) - i)).Format("2006-01-02")
		if err := s.UpsertNight(day, c, true); err != nil {
			t.Fatal(err)
		}
	}
	// Plus tonight, which Baseline(1) must exclude.
	if err := s.UpsertNight(base.Format("2006-01-02"), 198, true); err != nil {
		t.Fatal(err)
	}

	got, err := s.Baseline(1)
	if err != nil {
		t.Fatal(err)
	}
	if got != 320 {
		t.Errorf("baseline %v, want 320 (median of the prior five); a mean would be ~1252", got)
	}
}

func TestBaselineExcludesTonight(t *testing.T) {
	s := open(t)
	for i, c := range []int{400, 400, 400} {
		day := base.AddDate(0, 0, -(3 - i)).Format("2006-01-02")
		if err := s.UpsertNight(day, c, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.UpsertNight(base.Format("2006-01-02"), 10, true); err != nil {
		t.Fatal(err)
	}
	got, err := s.Baseline(1)
	if err != nil {
		t.Fatal(err)
	}
	if got != 400 {
		t.Errorf("baseline %v, want 400: tonight's low count must not lower the bar it is judged against", got)
	}
}

func TestBaselineTooFewNights(t *testing.T) {
	s := open(t)
	if err := s.UpsertNight(base.Format("2006-01-02"), 300, true); err != nil {
		t.Fatal(err)
	}
	got, err := s.Baseline(1)
	if err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Errorf("baseline %v with only tonight recorded, want 0 (unknown)", got)
	}
}

func TestNightsAreOldestFirst(t *testing.T) {
	s := open(t)
	for i := 0; i < 4; i++ {
		day := base.AddDate(0, 0, -i).Format("2006-01-02")
		if err := s.UpsertNight(day, 100+i, true); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Nights(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d nights, want 4", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].Date < got[i-1].Date {
			t.Fatalf("nights are not oldest-first: %v then %v", got[i-1].Date, got[i].Date)
		}
	}
	// The chart and the alert both assume the last element is tonight.
	if got[len(got)-1].Date != base.Format("2006-01-02") {
		t.Errorf("last night is %v, want today", got[len(got)-1].Date)
	}
}

func TestUpsertNightReplacesNotDuplicates(t *testing.T) {
	s := open(t)
	day := base.Format("2006-01-02")
	for _, c := range []int{10, 20, 30} {
		if err := s.UpsertNight(day, c, false); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Nights(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rows for one date, want 1", len(got))
	}
	if got[0].KickCount != 30 {
		t.Errorf("count %d, want the latest value 30", got[0].KickCount)
	}
}

// ---------------------------------------------------------------- sync

// The watermark must only advance past rows that were actually shipped, or a
// failed upload silently loses detections forever.
func TestUnsyncedDetectionsWatermark(t *testing.T) {
	s := open(t)
	for i := 0; i < 5; i++ {
		mustDetection(t, s, at(i*1000))
	}

	first, err := s.UnsyncedDetections(100)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 5 {
		t.Fatalf("got %d unsynced, want 5", len(first))
	}

	// Ship only the first three.
	if err := s.MarkSynced(first[2].ID); err != nil {
		t.Fatal(err)
	}
	rest, err := s.UnsyncedDetections(100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 2 {
		t.Fatalf("after marking 3 synced, got %d unsynced, want 2", len(rest))
	}
	if rest[0].ID != first[3].ID {
		t.Errorf("resumed at id %d, want %d", rest[0].ID, first[3].ID)
	}
}

func TestUnsyncedDetectionsRespectsLimit(t *testing.T) {
	s := open(t)
	for i := 0; i < 50; i++ {
		mustDetection(t, s, at(i*10))
	}
	got, err := s.UnsyncedDetections(20)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 20 {
		t.Errorf("got %d rows, want the limit of 20", len(got))
	}
}

func TestUnsyncedDetectionsRoundTripsFields(t *testing.T) {
	s := open(t)
	if err := s.InsertDetection(at(0), 0.375, 0.82, true); err != nil {
		t.Fatal(err)
	}
	got, err := s.UnsyncedDetections(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1", len(got))
	}
	d := got[0]
	if d.Residual != 0.375 || d.Confidence != 0.82 || !d.Acoustic {
		t.Errorf("round trip lost data: %+v", d)
	}
	if !d.T.Equal(at(0)) {
		t.Errorf("timestamp %v, want %v", d.T, at(0))
	}
}

func TestCountDetectionsOnDay(t *testing.T) {
	s := open(t)
	today := time.Now()
	for i := 0; i < 7; i++ {
		if err := s.InsertDetection(today.Add(time.Duration(i)*time.Second), 0.2, 0.5, true); err != nil {
			t.Fatal(err)
		}
	}
	// One yesterday, which must not be counted.
	if err := s.InsertDetection(today.AddDate(0, 0, -1), 0.2, 0.5, true); err != nil {
		t.Fatal(err)
	}

	n, err := s.CountDetectionsOn(today.Format("2006-01-02"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 7 {
		t.Errorf("counted %d for today, want 7", n)
	}
}

func TestCountDetectionsRejectsBadDate(t *testing.T) {
	s := open(t)
	if _, err := s.CountDetectionsOn("not-a-date"); err == nil {
		t.Error("expected an error for a malformed date")
	}
}

// ---------------------------------------------------------------- events

func TestEventsWindowReturnsAllThreeKinds(t *testing.T) {
	s := open(t)
	mustCommand(t, s, at(1000), "weak")
	mustDetection(t, s, at(1100))
	mustPress(t, s, at(1800))

	ev, err := s.EventsWindow(at(0), at(5000))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, e := range ev {
		seen[e.Kind]++
	}
	for _, k := range []string{"command", "detection", "press"} {
		if seen[k] != 1 {
			t.Errorf("kind %q appeared %d times, want 1 (overlay would be wrong)", k, seen[k])
		}
	}
	for i := 1; i < len(ev); i++ {
		if ev[i].TMS < ev[i-1].TMS {
			t.Fatal("events are not in time order; the overlay draws them left to right")
		}
	}
}

// ---------------------------------------------------------------- helpers

func mustCommand(t *testing.T, s *Store, at time.Time, strength string) {
	t.Helper()
	if err := s.InsertCommand(at, strength); err != nil {
		t.Fatal(err)
	}
}

func mustDetection(t *testing.T, s *Store, at time.Time) {
	t.Helper()
	if err := s.InsertDetection(at, 0.3, 0.7, true); err != nil {
		t.Fatal(err)
	}
}

func mustPress(t *testing.T, s *Store, at time.Time) {
	t.Helper()
	if err := s.InsertPress(at, "judge"); err != nil {
		t.Fatal(err)
	}
}

// The bug this pins: on a cold database the first night's seeding and the auth
// store's migration raced, SQLite's default busy timeout is zero, and whoever
// lost got SQLITE_BUSY. On Cloud Run the loser was authentication, which then
// disabled itself and served every page open with one log line as the warning.
func TestConcurrentWritersDoNotGetSqliteBusy(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "race.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 8; j++ {
				if err := s.InsertPress(time.Now(), "race"); err != nil {
					errs <- err
					return
				}
				if err := s.UpsertNight(fmt.Sprintf("2026-01-%02d", n+1), j, true); err != nil {
					errs <- err
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent write failed: %v", err)
	}
}
