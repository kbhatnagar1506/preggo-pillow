package memory

import (
	"context"
	"testing"
)

// sanitizeMetadata exists for one reason: Backboard type-locks a metadata key
// to whatever type it first receives, and any later write with a different type
// for that key returns HTTP 500 permanently for that assistant. These tests pin
// the two properties that make that impossible to trip.

func TestSanitizeAlwaysProducesStrings(t *testing.T) {
	in := map[string]any{
		"kind":      "night",
		"count":     198,
		"baseline":  341.0,
		"deviation": -41.93,
		"verbatim":  true,
		"big":       int64(9000),
	}
	out := sanitizeMetadata(in)
	for k, v := range out {
		if _, ok := v.(string); !ok {
			t.Errorf("key %q produced %T, want string: a non-string can type-lock the key", k, v)
		}
	}
}

func TestSanitizeNamespacesEveryKey(t *testing.T) {
	out := sanitizeMetadata(map[string]any{"kind": "note", "date": "2026-09-18"})
	for k := range out {
		if len(k) < 5 || k[:5] != "lull_" {
			t.Errorf("key %q is not namespaced; a bare name may already be locked to another type by a different integration", k)
		}
	}
	if _, ok := out["lull_kind"]; !ok {
		t.Error("expected lull_kind")
	}
}

func TestSanitizeNilForEmpty(t *testing.T) {
	if got := sanitizeMetadata(nil); got != nil {
		t.Errorf("nil metadata produced %v, want nil", got)
	}
	if got := sanitizeMetadata(map[string]any{}); got != nil {
		t.Errorf("empty metadata produced %v, want nil", got)
	}
}

func TestStringifyValue(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{"already", "already"},
		{true, "true"},
		{false, "false"},
		{341.0, "341"}, // whole floats must not become "341.00"
		{-41.93, "-41.93"},
		{198, "198"},
		{int64(9000), "9000"},
	}
	for _, c := range cases {
		if got := stringifyValue(c.in); got != c.want {
			t.Errorf("stringifyValue(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// A float that happens to be whole should read as an integer, because "341.00"
// in a clinical memory looks like spurious precision.
func TestSanitizeWholeFloatsReadAsIntegers(t *testing.T) {
	out := sanitizeMetadata(map[string]any{"baseline": 341.0})
	if got := out["lull_baseline"]; got != "341" {
		t.Errorf("got %q, want \"341\"", got)
	}
}

// ---- a disabled client must be completely safe -----------------------------
//
// Lull must run with no API key, no network, and behind a captive portal. Every
// one of these would be called on the sensor path.

func TestDisabledClientIsSafe(t *testing.T) {
	c, err := New(context.Background(), Options{APIKey: ""})
	if err != nil {
		t.Fatalf("New with no key returned an error: %v", err)
	}
	if c.Enabled() {
		t.Fatal("client with no key reports Enabled")
	}

	c.Remember("should not panic or block", map[string]any{"kind": "test"})
	c.Close()
	if id := c.AssistantID(); id != "" {
		t.Errorf("assistant id %q on a disabled client", id)
	}

	if mems, err := c.Search(context.Background(), "anything", 5); err != nil || mems != nil {
		t.Errorf("Search on disabled client: %v, %v; want nil, nil", mems, err)
	}
	if _, _, err := c.List(context.Background(), 10); err != nil {
		t.Errorf("List on disabled client returned %v, want nil", err)
	}
	if err := c.Forget(context.Background(), "abc"); err != nil {
		t.Errorf("Forget on disabled client returned %v, want nil", err)
	}
	if _, err := c.Ask(context.Background(), "q", ""); err == nil {
		t.Error("Ask on a disabled client should report that memory is not configured")
	}
}

func TestNilClientIsSafe(t *testing.T) {
	var c *Client
	if c.Enabled() {
		t.Fatal("nil client reports Enabled")
	}
	if id := c.AssistantID(); id != "" {
		t.Errorf("nil client returned id %q", id)
	}
	c.Remember("no panic please", nil) // must not panic
}

// The API returns the id under "memory_id" on create and "id" on list/search.
func TestMemoryIdentAcceptsBothFieldNames(t *testing.T) {
	if got := (Memory{ID: "from-list"}).Ident(); got != "from-list" {
		t.Errorf("got %q", got)
	}
	if got := (Memory{MemoryID: "from-create"}).Ident(); got != "from-create" {
		t.Errorf("got %q", got)
	}
	if got := (Memory{ID: "a", MemoryID: "b"}).Ident(); got != "a" {
		t.Errorf("got %q, want the list field to win when both are set", got)
	}
	if got := (Memory{}).Ident(); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

// ---- the recorder ----------------------------------------------------------

func TestRecorderOnDisabledClientIsSafe(t *testing.T) {
	c, _ := New(context.Background(), Options{APIKey: ""})
	r := NewRecorder(c)
	r.Profile("Maya", 34, "2 November 2026")
	r.Night("2026-09-18", 198, 341, -41.9)
	r.Alert("2026-09-18", 198, 341, -41.9, 2)
	r.Maternal("2026-09-18", "lateral", 0, 15, 0, 3)
	r.Note("he has been quieter")
	r.Appointment("14 September 2026", "CTG normal")
}

func TestRecorderIgnoresEmptyNote(t *testing.T) {
	r := NewRecorder(&Client{})
	r.Note("")
	r.Note("   ")
}
