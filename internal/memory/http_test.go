package memory

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func client(t *testing.T, base string) *Client {
	t.Helper()
	c, err := New(context.Background(), Options{
		APIKey: "k", AssistantID: "assistant-1", BaseURL: base,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	if !c.Enabled() {
		t.Fatal("client is not enabled")
	}
	return c
}

// A memory is a fact about a pregnancy. Losing one to a transient 500 loses it
// from the only place it exists, so writes retry.
func TestRememberRetriesOnServerError(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"memory_id":"m1"}`))
	}))
	defer srv.Close()

	c := client(t, srv.URL)
	c.Remember("she reported he is quieter", map[string]any{"kind": "note"})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&calls) >= 2 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("only %d attempts; a transient 500 dropped the memory", atomic.LoadInt32(&calls))
}

func TestRememberSendsSanitizedMetadata(t *testing.T) {
	got := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		md, _ := req["metadata"].(map[string]any)
		select {
		case got <- md:
		default:
		}
		_, _ = w.Write([]byte(`{"memory_id":"m1"}`))
	}))
	defer srv.Close()

	c := client(t, srv.URL)
	c.Remember("night summary", map[string]any{"kind": "night", "baseline": 341.0})

	select {
	case md := <-got:
		if _, ok := md["lull_kind"]; !ok {
			t.Errorf("metadata not namespaced: %v", md)
		}
		for k, v := range md {
			if _, ok := v.(string); !ok {
				t.Errorf("key %q sent as %T; a non-string can type-lock the key", k, v)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no write reached the server")
	}
}

func TestSearchParsesResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"memories":[{"id":"a","content":"quieter since Tuesday","score":0.91}]}`))
	}))
	defer srv.Close()

	c := client(t, srv.URL)
	got, err := c.Search(context.Background(), "quieter", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Content != "quieter since Tuesday" || got[0].Ident() != "a" {
		t.Errorf("parsed %+v", got)
	}
}

func TestListParsesTotalCount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"memories":[{"id":"a","content":"x"}],"total_count":11}`))
	}))
	defer srv.Close()

	c := client(t, srv.URL)
	mems, total, err := c.List(context.Background(), 25)
	if err != nil {
		t.Fatal(err)
	}
	if total != 11 || len(mems) != 1 {
		t.Errorf("got %d memories, total %d", len(mems), total)
	}
}

// A question is not a fact. Letting the model write its own answers back into
// memory fills the store with its own speculation.
func TestAskUsesReadonlyMemoryAndKeepsTheThread(t *testing.T) {
	var mode string
	var sawThread string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		mode, _ = req["memory"].(string)
		sawThread, _ = req["thread_id"].(string)
		_, _ = w.Write([]byte(`{"content":"answer","thread_id":"t-1"}`))
	}))
	defer srv.Close()

	c := client(t, srv.URL)
	if _, err := c.Ask(context.Background(), "has he been quieter", `{"movements":198}`); err != nil {
		t.Fatal(err)
	}
	if mode != "Readonly" {
		t.Errorf("memory mode %q, want Readonly: a question must not write itself into memory", mode)
	}
	if sawThread != "" {
		t.Errorf("first call sent thread_id %q", sawThread)
	}

	if _, err := c.Ask(context.Background(), "and last week?", ""); err != nil {
		t.Fatal(err)
	}
	if sawThread != "t-1" {
		t.Errorf("second call sent thread_id %q, want the thread from the first reply", sawThread)
	}
}

func TestAskIncludesTonightsReadings(t *testing.T) {
	var content string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		content, _ = req["content"].(string)
		_, _ = w.Write([]byte(`{"content":"ok"}`))
	}))
	defer srv.Close()

	c := client(t, srv.URL)
	if _, err := c.Ask(context.Background(), "is he ok", `{"movements_tonight":198}`); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "198") {
		t.Error("the question reached the model without tonight's readings attached")
	}
	if !strings.Contains(content, "is he ok") {
		t.Error("the question itself was lost")
	}
}

// If Backboard cannot be reached at startup there is nowhere to write, so the
// client must degrade to disabled rather than queue into the void.
func TestUnreachableBackboardDegradesToDisabled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	srv.Close() // refuse connections

	c, err := New(context.Background(), Options{APIKey: "k", BaseURL: srv.URL, Name: "Lull"})
	if err != nil {
		t.Fatalf("New returned an error instead of degrading: %v", err)
	}
	if c.Enabled() {
		t.Error("client reports Enabled although it never found an assistant")
	}
	c.Remember("must not panic", nil)
}

func TestFindsExistingAssistantByNameBeforeCreating(t *testing.T) {
	var created bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`[{"name":"Lull","assistant_id":"existing-1"}]`))
			return
		}
		created = true
		_, _ = w.Write([]byte(`{"assistant_id":"new-1"}`))
	}))
	defer srv.Close()

	c, err := New(context.Background(), Options{APIKey: "k", BaseURL: srv.URL, Name: "Lull"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if created {
		t.Error("created a duplicate assistant instead of reusing the existing one")
	}
	if c.AssistantID() != "existing-1" {
		t.Errorf("assistant %q, want existing-1", c.AssistantID())
	}
}
