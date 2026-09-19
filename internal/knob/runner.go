package knob

import (
	"context"
	"log"
	"time"
)

// Reader is the one thing the runner needs: the dial's current reading.
type Reader interface {
	MilliVolts(channel int) (int, error)
}

// Run polls the dial and calls onTurn once per completed gesture.
//
// Poll rate matters in both directions: too slow and a quick flick is missed
// entirely; too fast and the I2C bus is saturated competing with the
// accelerometers. 50 Hz is comfortably above a human gesture and leaves the bus
// mostly idle.
func Run(ctx context.Context, r Reader, channel int, onTurn func(time.Time, int)) {
	d := New()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()

	var consecutiveErrs int
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			mv, err := r.MilliVolts(channel)
			if err != nil {
				// A read error is usually a nudged Grove cable. Skip the
				// sample rather than tearing down; but say something if it
				// persists, because a silently dead dial reads as "she felt
				// nothing", which is a clinically meaningful lie.
				consecutiveErrs++
				if consecutiveErrs == 50 {
					log.Printf("knob: 50 consecutive read errors on A%d (%v) — "+
						"the dial is not being recorded", channel, err)
				}
				continue
			}
			if consecutiveErrs >= 50 {
				log.Printf("knob: reads recovered on A%d", channel)
			}
			consecutiveErrs = 0
			if d.Feed(now, mv) && onTurn != nil {
				onTurn(now, d.Settled())
			}
		}
	}
}
