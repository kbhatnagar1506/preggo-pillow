package sensor

import (
	"math"
	"math/rand"
	"sync"
	"time"
)

// Sim is a synthetic sensor source. It exists so the entire software stack can
// be built, demoed and debugged before a single Grove cable is plugged in, and
// so the demo still runs if a cable dies at hour 30.
//
// It models three things that matter to the detector:
//   - a resting baseline with sensor noise on all three nodes
//   - maternal movement, which appears on ALL nodes including the reference
//   - fetal kicks, which appear only on the abdominal nodes, attenuated by the
//     foam (or in the real device, by tissue)
//   - respiration, a slow torso oscillation on every node
type Sim struct {
	SampleHz int

	readings  chan Reading
	acoustics chan Acoustic
	stop      chan struct{}
	once      sync.Once

	mu sync.Mutex
	// pending impulses, applied as a decaying envelope
	kicks    []impulse
	maternal []impulse

	// Respiration. Real, because the maternal panel measures it and a panel
	// measuring noise is worse than no panel.
	breathHz  float64 // ~0.25 Hz is 15 breaths a minute
	breathAmp float64
	t0        time.Time
}

type impulse struct {
	start     time.Time
	amplitude float64 // g at peak
	duration  time.Duration
}

func (i impulse) valueAt(t time.Time) float64 {
	d := t.Sub(i.start)
	if d < 0 || d > i.duration {
		return 0
	}
	// half-sine envelope: quick rise, quick fall, roughly the shape of a
	// kick transmitted through tissue.
	phase := float64(d) / float64(i.duration)
	return i.amplitude * math.Sin(phase*math.Pi)
}

// NewSim returns a simulated source running at sampleHz.
func NewSim(sampleHz int) *Sim {
	if sampleHz <= 0 {
		sampleHz = 100
	}
	s := &Sim{
		SampleHz:  sampleHz,
		readings:  make(chan Reading, 1024),
		acoustics: make(chan Acoustic, 256),
		stop:      make(chan struct{}),
		breathHz:  0.25, // 15 breaths per minute
		breathAmp: 0.035,
		t0:        time.Now(),
	}
	go s.run()
	go s.wander()
	return s
}

func (s *Sim) Readings() <-chan Reading   { return s.readings }
func (s *Sim) Acoustics() <-chan Acoustic { return s.acoustics }

func (s *Sim) Close() error {
	s.once.Do(func() { close(s.stop) })
	return nil
}

// InjectKick simulates a fetal movement. Amplitude is in g as seen at the
// abdominal sensors after attenuation. Called by the kicker when it fires the
// servo, so that simulated mode behaves like the real rig.
func (s *Sim) InjectKick(amplitude float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kicks = append(s.kicks, impulse{
		start:     time.Now(),
		amplitude: amplitude,
		duration:  180 * time.Millisecond,
	})
}

// InjectMaternal simulates the mother moving, coughing, or the bed being
// bumped. This lands on every node, so a correct detector must reject it.
func (s *Sim) InjectMaternal(amplitude float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maternal = append(s.maternal, impulse{
		start:     time.Now(),
		amplitude: amplitude,
		duration:  600 * time.Millisecond,
	})
}

// wander injects occasional spontaneous maternal movement so the resting
// signal is not unrealistically clean.
func (s *Sim) wander() {
	for {
		d := time.Duration(8000+rand.Intn(20000)) * time.Millisecond
		select {
		case <-s.stop:
			return
		case <-time.After(d):
			s.InjectMaternal(0.08 + rand.Float64()*0.25)
		}
	}
}

func (s *Sim) run() {
	defer close(s.readings)
	defer close(s.acoustics)

	tick := time.NewTicker(time.Second / time.Duration(s.SampleHz))
	defer tick.Stop()

	acousticEvery := s.SampleHz / 50 // ~50 Hz acoustic envelope
	if acousticEvery < 1 {
		acousticEvery = 1
	}
	n := 0

	for {
		select {
		case <-s.stop:
			return
		case now := <-tick.C:
			kick, mat := s.envelopes(now)

			// Breathing moves the whole torso, so it lands on every node and
			// is correctly NOT counted as fetal movement by the subtraction.
			elapsed := now.Sub(s.t0).Seconds()
			breath := s.breathAmp * math.Sin(2*math.Pi*s.breathHz*elapsed)

			for _, node := range []Node{NodeAbdoA, NodeAbdoB, NodeRef} {
				v := mat // maternal movement reaches every node
				if node != NodeRef {
					// The second abdominal node sees a slightly weaker,
					// slightly later version of the same kick.
					scale := 1.0
					if node == NodeAbdoB {
						scale = 0.72
					}
					v += kick * scale
				}
				noise := (rand.Float64() - 0.5) * 0.012
				r := Reading{
					T:    now,
					Node: node,
					// Mounting convention (see package maternal): X across the
					// body, Y head-ward, Z out of the back. Gravity on X means
					// she is on her side, which is where we want her.
					AX: -1.0 + noise,
					AY: v*0.6 + noise,
					AZ: v*0.8 + breath + noise,
				}
				select {
				case s.readings <- r:
				default: // drop rather than block the ticker
				}
			}

			n++
			if n%acousticEvery == 0 {
				// The contact mic hears the kick strongly and maternal
				// movement only weakly, which is exactly why it is worth
				// having as a second channel.
				rms := 2.0 + rand.Float64()*1.5 + kick*90 + mat*8
				select {
				case s.acoustics <- Acoustic{T: now, RMS: rms}:
				default:
				}
			}
		}
	}
}

func (s *Sim) envelopes(now time.Time) (kick, maternal float64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	live := s.kicks[:0]
	for _, im := range s.kicks {
		if v := im.valueAt(now); v > 0 {
			kick += v
			live = append(live, im)
		} else if now.Sub(im.start) <= im.duration {
			live = append(live, im)
		}
	}
	s.kicks = live

	liveM := s.maternal[:0]
	for _, im := range s.maternal {
		if v := im.valueAt(now); v > 0 {
			maternal += v
			liveM = append(liveM, im)
		} else if now.Sub(im.start) <= im.duration {
			liveM = append(liveM, im)
		}
	}
	s.maternal = liveM

	return kick, maternal
}
