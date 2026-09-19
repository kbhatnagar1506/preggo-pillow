package maternal

import (
	"fmt"
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/kbhatnagar1506/lull/internal/sensor"
)

// exploreFeed is feed() but returns the raw best amp/rpm too.
func exploreFeed(tr *Tracker, seconds int, hz int, bpm float64, amp float64, gx, gy, gz float64) {
	start := time.Now()
	bh := bpm / 60
	for i := 0; i < seconds*hz; i++ {
		el := float64(i) / float64(hz)
		breath := amp * math.Sin(2*math.Pi*bh*el)
		n := func() float64 { return (rand.Float64() - 0.5) * 0.012 }
		tr.Feed(sensor.Reading{
			T:    start.Add(time.Duration(el * float64(time.Second))),
			Node: sensor.NodeRef,
			AX:   gx + n(), AY: gy + n(), AZ: gz + breath + n(),
		})
	}
}

func TestZZExploreRates(t *testing.T) {
	for _, bpm := range []float64{0, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 14, 15, 16, 18, 20, 24, 30, 40} {
		var got []float64
		for seed := 0; seed < 5; seed++ {
			rand.Seed(int64(seed) + 1)
			tr := NewTracker()
			exploreFeed(tr, 40, 100, bpm, 0.035, -1.0, 0, 0)
			got = append(got, tr.Stats().RespirationRPM)
		}
		fmt.Printf("true=%5.1f -> %v\n", bpm, got)
	}
}

func TestZZExploreAmplitudes(t *testing.T) {
	for _, amp := range []float64{0, 0.001, 0.002, 0.003, 0.004, 0.005, 0.008, 0.012, 0.02, 0.035, 0.06} {
		var rpms, amps []float64
		for seed := 0; seed < 3; seed++ {
			rand.Seed(int64(seed) + 1)
			tr := NewTracker()
			exploreFeed(tr, 40, 100, 15, amp, -1.0, 0, 0)
			rpms = append(rpms, tr.Stats().RespirationRPM)
			tr.mu.Lock()
			// recompute best amp for reporting
			span := t0span(tr)
			hz := float64(len(tr.respStamps)) / span
			var bestAmp float64
			for _, w := range [][]float64{tr.respX, tr.respY, tr.respZ} {
				a, _ := estimateOscillation(w, hz, span)
				if a > bestAmp {
					bestAmp = a
				}
			}
			tr.mu.Unlock()
			amps = append(amps, bestAmp)
		}
		fmt.Printf("amp=%.4f -> rpm %v  measuredAmp %v\n", amp, rpms, amps)
	}
}

func t0span(tr *Tracker) float64 {
	return tr.respStamps[len(tr.respStamps)-1].Sub(tr.respStamps[0]).Seconds()
}

// What does the live sim source actually produce?
func TestZZExploreSimSource(t *testing.T) {
	s := sensor.NewSim(100)
	defer s.Close()
	tr := NewTracker()
	deadline := time.After(45 * time.Second)
	ch := s.Readings()
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
loop:
	for {
		select {
		case r := <-ch:
			tr.Feed(r)
		case <-tick.C:
			st := tr.Stats()
			fmt.Printf("sim t: ready=%v rpm=%.2f posture=%s\n", st.Ready, st.RespirationRPM, st.Posture)
		case <-deadline:
			break loop
		}
	}
	st := tr.Stats()
	fmt.Printf("SIM FINAL rpm=%.3f ready=%v\n", st.RespirationRPM, st.Ready)
}

// Degenerate signals: slow postural drift, a single roll, a breath-hold.
func TestZZExploreDegenerate(t *testing.T) {
	// slow drift only (she rolls once over 40s)
	rand.Seed(7)
	tr := NewTracker()
	start := time.Now()
	for i := 0; i < 40*100; i++ {
		el := float64(i) / 100
		n := func() float64 { return (rand.Float64() - 0.5) * 0.012 }
		drift := 0.05 * math.Sin(2*math.Pi*(1.0/80.0)*el) // 0.0125 Hz = 0.75 rpm
		tr.Feed(sensor.Reading{T: start.Add(time.Duration(el * float64(time.Second))), Node: sensor.NodeRef,
			AX: -1 + n(), AY: n(), AZ: drift + n()})
	}
	fmt.Printf("slow drift -> rpm=%.2f\n", tr.Stats().RespirationRPM)

	// very slow breathing right at the old gate
	for _, bpm := range []float64{5.5, 6.0, 6.5, 7.0, 7.5, 8.0, 8.5, 9.0, 9.5, 10.0} {
		var got []float64
		for seed := 0; seed < 5; seed++ {
			rand.Seed(int64(seed) + 100)
			tr := NewTracker()
			exploreFeed(tr, 40, 100, bpm, 0.035, -1.0, 0, 0)
			got = append(got, tr.Stats().RespirationRPM)
		}
		fmt.Printf("near-gate true=%.1f -> %v\n", bpm, got)
	}
}
