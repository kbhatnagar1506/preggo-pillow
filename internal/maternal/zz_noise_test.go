package maternal

import (
	"fmt"
	"math/rand"
	"sort"
	"testing"
	"time"

	"github.com/kbhatnagar1506/lull/internal/sensor"
)

// How big does the measured "amplitude" get when there is NO breathing at all?
// That is the floor the amplitude gate has to clear.
func TestZZNoiseFloor(t *testing.T) {
	var amps []float64
	var rpms []float64
	nonzero := 0
	for seed := 0; seed < 200; seed++ {
		rand.Seed(int64(seed) + 1000)
		tr := NewTracker()
		start := time.Now()
		for i := 0; i < 40*100; i++ {
			el := float64(i) / 100
			n := func() float64 { return (rand.Float64() - 0.5) * 0.012 }
			tr.Feed(sensor.Reading{T: start.Add(time.Duration(el * float64(time.Second))),
				Node: sensor.NodeRef, AX: -1 + n(), AY: n(), AZ: n()})
		}
		tr.mu.Lock()
		span := tr.respStamps[len(tr.respStamps)-1].Sub(tr.respStamps[0]).Seconds()
		hz := float64(len(tr.respStamps)) / span
		var bestAmp, bestRPM float64
		for _, w := range [][]float64{tr.respX, tr.respY, tr.respZ} {
			a, r := estimateOscillation(w, hz, span)
			if a > bestAmp {
				bestAmp, bestRPM = a, r
			}
		}
		tr.mu.Unlock()
		amps = append(amps, bestAmp)
		rpms = append(rpms, bestRPM)
		if tr.Stats().RespirationRPM != 0 {
			nonzero++
			fmt.Printf("  LEAK seed=%d amp=%.5f rpm=%.1f reported=%.1f\n", seed, bestAmp, bestRPM, tr.Stats().RespirationRPM)
		}
	}
	sort.Float64s(amps)
	fmt.Printf("noise-only measured amplitude over %d runs: min=%.5f p50=%.5f p90=%.5f p99=%.5f max=%.5f\n",
		len(amps), amps[0], amps[len(amps)/2], amps[int(float64(len(amps))*0.9)], amps[int(float64(len(amps))*0.99)], amps[len(amps)-1])
	above := 0
	for _, a := range amps {
		if a >= 0.004 {
			above++
		}
	}
	fmt.Printf("noise-only runs whose amplitude cleared the 0.004 gate: %d/%d\n", above, len(amps))
	fmt.Printf("noise-only runs that reported a NUMBER: %d/%d\n", nonzero, len(amps))
}
