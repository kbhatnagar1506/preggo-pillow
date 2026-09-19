package tiger

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Tiger is optional. Lull runs on a Raspberry Pi behind venue WiFi, so every
// path here must be safe with no connection at all.

func TestOpenWithoutURLDegradesNotFails(t *testing.T) {
	t.Setenv("TIGER_DATABASE_URL", "")
	s, err := Open(context.Background(), Options{})
	if err != nil {
		t.Fatalf("Open with no URL returned an error: %v", err)
	}
	if s.Enabled() {
		t.Fatal("store reports Enabled with no URL")
	}
}

func TestDisabledStoreIsSafe(t *testing.T) {
	t.Setenv("TIGER_DATABASE_URL", "")
	s, _ := Open(context.Background(), Options{})
	ctx := context.Background()

	if err := s.SyncDetections(ctx, []Detection{{T: time.Now()}}); err != nil {
		t.Errorf("SyncDetections: %v", err)
	}
	if err := s.RecordMaternal(ctx, time.Now(), "lateral", 0, 15, 0, 2); err != nil {
		t.Errorf("RecordMaternal: %v", err)
	}
	if n, err := s.Nights(ctx, 14); err != nil || n != nil {
		t.Errorf("Nights: %v, %v", n, err)
	}
	if b, err := s.Baseline(ctx); err != nil || b != 0 {
		t.Errorf("Baseline: %v, %v", b, err)
	}
	if err := s.Refresh(ctx); err != nil {
		t.Errorf("Refresh: %v", err)
	}
	s.Close()
}

func TestNilStoreIsSafe(t *testing.T) {
	var s *Store
	if s.Enabled() {
		t.Fatal("nil store reports Enabled")
	}
	if err := s.SyncDetections(context.Background(), nil); err != nil {
		t.Errorf("SyncDetections on nil store: %v", err)
	}
	s.Close()
}

func TestBadURLDoesNotPanic(t *testing.T) {
	s, err := Open(context.Background(), Options{URL: "not-a-postgres-url"})
	if err == nil {
		t.Log("parse accepted the string; the ping will fail instead")
	}
	if s.Enabled() {
		t.Error("store reports Enabled on a malformed URL")
	}
}

func TestSyncEmptyBatchIsANoop(t *testing.T) {
	t.Setenv("TIGER_DATABASE_URL", "")
	s, _ := Open(context.Background(), Options{})
	if err := s.SyncDetections(context.Background(), nil); err != nil {
		t.Errorf("empty batch: %v", err)
	}
}

func TestIsAlreadyExists(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{`relation "lull_detections" already exists`, true},
		{`table is already a hypertable`, true},
		{`duplicate key value`, true},
		{`connection refused`, false},
		{`syntax error at or near "SELECT"`, false},
	}
	for _, c := range cases {
		if got := isAlreadyExists(errString(c.msg)); got != c.want {
			t.Errorf("isAlreadyExists(%q) = %v, want %v", c.msg, got, c.want)
		}
	}
	if isAlreadyExists(nil) {
		t.Error("nil error reported as already-exists")
	}
}

type errString string

func (e errString) Error() string { return string(e) }

// The schema encodes decisions that are invisible at a glance and trivial to
// erase with a well-meaning edit. These assert them without needing a database.
func TestMigrationBucketsNightsFromNoon(t *testing.T) {
	sql := strings.Join(MigrationStatements(), "\n")

	// THE decision. Sleep crosses midnight, so a midnight-aligned bucket cuts
	// every night in half and the baseline becomes meaningless. Noon-to-noon
	// keeps one night in one bucket.
	if !strings.Contains(sql, "origin => TIMESTAMPTZ '2000-01-01 12:00:00+00'") {
		t.Error("the nightly aggregate is no longer bucketed from noon; a midnight " +
			"boundary splits every night across two buckets")
	}
	if !strings.Contains(sql, "time_bucket(INTERVAL '1 day'") {
		t.Error("nightly bucket is not one day")
	}
}

func TestMigrationCreatesTheExpectedShape(t *testing.T) {
	sql := strings.Join(MigrationStatements(), "\n")
	for _, tbl := range []string{"lull_detections", "lull_commands", "lull_presses", "lull_maternal"} {
		if !strings.Contains(sql, "CREATE TABLE IF NOT EXISTS "+tbl) {
			t.Errorf("missing table %s", tbl)
		}
		if !strings.Contains(sql, "create_hypertable('"+tbl+"'") {
			t.Errorf("%s is not a hypertable, so it gets none of Timescale's benefit", tbl)
		}
	}
	if !strings.Contains(sql, "timescaledb.continuous") {
		t.Error("the nightly rollup is not a continuous aggregate")
	}
}

// commands, detections and presses must stay separate all the way into
// Timescale. Merging them there would undo the guarantee the whole demo rests
// on: that the count cannot be inflated by pressing buttons.
func TestMigrationKeepsTheThreeRecordsSeparate(t *testing.T) {
	sql := strings.Join(MigrationStatements(), "\n")
	if !strings.Contains(sql, "FROM lull_detections") {
		t.Error("the nightly count must be built from detections")
	}
	for _, wrong := range []string{"FROM lull_commands", "FROM lull_presses"} {
		if strings.Contains(sql, "lull_nightly") && strings.Contains(sql, wrong) {
			t.Errorf("the nightly aggregate reads %s; the count must come from detections only", wrong)
		}
	}
}

func TestMigrationIsRerunnable(t *testing.T) {
	for _, q := range MigrationStatements() {
		if strings.HasPrefix(strings.TrimSpace(q), "CREATE TABLE") &&
			!strings.Contains(q, "IF NOT EXISTS") {
			t.Errorf("not re-runnable, will fail on a second start: %.50s", q)
		}
		if strings.Contains(q, "create_hypertable") && !strings.Contains(q, "if_not_exists=>TRUE") {
			t.Errorf("hypertable creation is not idempotent: %.60s", q)
		}
	}
}
