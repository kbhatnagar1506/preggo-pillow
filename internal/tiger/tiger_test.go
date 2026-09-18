package tiger

import (
	"context"
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
