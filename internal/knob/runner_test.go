package knob

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type scriptedReader struct {
	mu   sync.Mutex
	vals []int
	i    int
	err  error
}

func (s *scriptedReader) MilliVolts(int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return 0, s.err
	}
	if s.i >= len(s.vals) {
		return s.vals[len(s.vals)-1], nil
	}
	v := s.vals[s.i]
	s.i++
	return v, nil
}

func TestRunEmitsOneTurnPerGesture(t *testing.T) {
	var vals []int
	for i := 0; i < 20; i++ {
		vals = append(vals, 1000)
	}
	for i := 0; i < 30; i++ {
		vals = append(vals, 1000+i*30) // ramp to 1870
	}
	for i := 0; i < 60; i++ {
		vals = append(vals, 1870)
	}
	r := &scriptedReader{vals: vals}

	var mu sync.Mutex
	var turns []int
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go Run(ctx, r, 3, func(_ time.Time, settled int) {
		mu.Lock()
		turns = append(turns, settled)
		mu.Unlock()
	})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(turns)
		mu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(turns) != 1 {
		t.Fatalf("want exactly 1 turn, got %d (%v)", len(turns), turns)
	}
	if turns[0] < 1800 || turns[0] > 1950 {
		t.Errorf("settled at %d, want about 1870", turns[0])
	}
}

// A dial that cannot be read must not look like a person who felt nothing.
func TestRunSurvivesReadErrors(t *testing.T) {
	r := &scriptedReader{vals: []int{1000}, err: errors.New("bus gone")}
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() { Run(ctx, r, 3, func(time.Time, int) {}); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return when the context was cancelled")
	}
}

func TestRunStopsOnContextCancel(t *testing.T) {
	r := &scriptedReader{vals: []int{1500}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { Run(ctx, r, 3, nil); close(done) }()
	time.Sleep(60 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run ignored cancellation")
	}
}
