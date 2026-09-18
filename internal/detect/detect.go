// Package detect turns raw accelerometer and acoustic samples into kick
// detections.
//
// The algorithm is deliberately simple and explainable, because at judging you
// have to be able to say what it does in one sentence:
//
//	High-pass each node to strip gravity and drift, subtract the reference
//	node from the abdominal pair so maternal movement cancels, threshold what
//	is left, and require the acoustic channel to agree.
//
// Reference subtraction is the part that matters. Published work puts a single
// abdominal accelerometer at roughly 50% detection, corrupted by maternal
// breathing and coughing, and finds that adding a reference sensor away from
// the abdomen consistently fixes it.
package detect

import (
	"math"
	"time"

	"github.com/kbhatnagar1506/lull/internal/sensor"
)

// Config holds the tuning knobs. Defaults are a starting point for the foam
// phantom; retune them at hour 6 against real servo kicks and write the final
// numbers down, because they are the difference between 6/11 and 10/11.
type Config struct {
	// HighPassWindow is how much history the rolling mean covers. Anything
	// slower than this (gravity, posture, slow drift) is removed.
	HighPassWindow time.Duration

	// Threshold is the residual, in g, above which a kick is declared.
	Threshold float64

	// Refractory is the minimum gap between two detections. Without it a
	// single kick rings and registers five times.
	Refractory time.Duration

	// RequireAcoustic makes a detection need agreement from the contact mic.
	// Fewer false positives, and it is the demonstration that the two-channel
	// architecture is real rather than decorative.
	RequireAcoustic bool

	// AcousticWindow is how far either side of the accelerometer event the
	// acoustic spike may fall.
	AcousticWindow time.Duration

	// AcousticThreshold is the acoustic RMS above its own rolling floor that
	// counts as a spike.
	AcousticThreshold float64
}

// DefaultConfig is tuned for the simulator. Expect to change Threshold once
// you have real foam in the loop.
func DefaultConfig() Config {
	return Config{
		HighPassWindow:    2 * time.Second,
		Threshold:         0.11,
		Refractory:        900 * time.Millisecond,
		RequireAcoustic:   true,
		AcousticWindow:    250 * time.Millisecond,
		AcousticThreshold: 10.0,
	}
}

// Detection is one kick the system believes happened. It is written to the
// detections table and is what increments the count on screen.
//
// Note the distinction that matters in Q&A: this is produced by the SENSORS.
// It is never produced by the servo firing. The servo's own log is ground
// truth and lives in a separate table, which is how accuracy is scored.
type Detection struct {
	T          time.Time `json:"t"`
	Residual   float64   `json:"residual"`
	Confidence float64   `json:"confidence"`
	Acoustic   bool      `json:"acoustic"`
}

// Detector consumes readings and emits detections.
type Detector struct {
	cfg Config

	nodes    map[sensor.Node]*channelState
	acoustic *acousticState

	lastDetection time.Time

	// Out receives every detection.
	Out chan Detection
}

// channelState high-passes each axis independently and then takes the
// magnitude of what is left.
//
// Doing it the other way round (magnitude first, then high-pass) looks
// equivalent and is not. The sensor sits in a ~1g gravity field, and a kick
// perpendicular to gravity barely moves the magnitude at all:
// sqrt(1² + 0.3²) = 1.044, so a 0.3g kick reads as a 0.044g change and falls
// under any sane threshold. Per-axis high-pass recovers the full 0.3g
// regardless of which way she is lying.
type channelState struct {
	winX, winY, winZ []float64
	stamps           []time.Time
	sumX, sumY, sumZ float64
	current          float64 // magnitude of the high-passed (dynamic) vector
	seen             bool    // has this node ever produced a usable sample
}

type acousticState struct {
	window []float64
	stamps []time.Time
	sum    float64
	spikes []time.Time
}

// New returns a detector. Feed it with Feed and FeedAcoustic, read from Out.
func New(cfg Config) *Detector {
	d := &Detector{
		cfg:      cfg,
		nodes:    make(map[sensor.Node]*channelState),
		acoustic: &acousticState{},
		Out:      make(chan Detection, 256),
	}
	for _, n := range []sensor.Node{sensor.NodeAbdoA, sensor.NodeAbdoB, sensor.NodeRef} {
		d.nodes[n] = &channelState{}
	}
	return d
}

// FeedAcoustic records a contact-mic sample.
func (d *Detector) FeedAcoustic(a sensor.Acoustic) {
	st := d.acoustic
	st.window = append(st.window, a.RMS)
	st.stamps = append(st.stamps, a.T)
	st.sum += a.RMS

	cutoff := a.T.Add(-d.cfg.HighPassWindow)
	for len(st.stamps) > 0 && st.stamps[0].Before(cutoff) {
		st.sum -= st.window[0]
		st.window = st.window[1:]
		st.stamps = st.stamps[1:]
	}
	if len(st.window) == 0 {
		return
	}
	mean := st.sum / float64(len(st.window))
	if a.RMS-mean > d.cfg.AcousticThreshold {
		st.spikes = append(st.spikes, a.T)
		// keep the spike list short
		keep := a.T.Add(-5 * time.Second)
		i := 0
		for i < len(st.spikes) && st.spikes[i].Before(keep) {
			i++
		}
		st.spikes = st.spikes[i:]
	}
}

// Feed records an accelerometer sample and may emit a detection.
func (d *Detector) Feed(r sensor.Reading) {
	st, ok := d.nodes[r.Node]
	if !ok {
		return
	}

	st.winX = append(st.winX, r.AX)
	st.winY = append(st.winY, r.AY)
	st.winZ = append(st.winZ, r.AZ)
	st.stamps = append(st.stamps, r.T)
	st.sumX += r.AX
	st.sumY += r.AY
	st.sumZ += r.AZ

	cutoff := r.T.Add(-d.cfg.HighPassWindow)
	for len(st.stamps) > 0 && st.stamps[0].Before(cutoff) {
		st.sumX -= st.winX[0]
		st.sumY -= st.winY[0]
		st.sumZ -= st.winZ[0]
		st.winX = st.winX[1:]
		st.winY = st.winY[1:]
		st.winZ = st.winZ[1:]
		st.stamps = st.stamps[1:]
	}
	n := float64(len(st.stamps))
	if n < 8 {
		return // not enough history for a meaningful mean yet
	}

	// The rolling mean per axis IS the gravity vector plus slow posture drift.
	// Subtracting it leaves only dynamic acceleration.
	dx := r.AX - st.sumX/n
	dy := r.AY - st.sumY/n
	dz := r.AZ - st.sumZ/n
	st.current = math.Sqrt(dx*dx + dy*dy + dz*dz)
	st.seen = true

	// Only evaluate on the reference node's arrival, so all three have a
	// fresh value for the same instant.
	if r.Node != sensor.NodeRef {
		return
	}
	d.evaluate(r.T)
}

func (d *Detector) evaluate(t time.Time) {
	if t.Sub(d.lastDetection) < d.cfg.Refractory {
		return
	}

	ref := d.nodes[sensor.NodeRef]
	if !ref.seen {
		return // no reference means no subtraction, and no subtraction means
		// we would be counting maternal movement as fetal movement.
	}

	// Average only the abdominal nodes that are actually reporting. Two is
	// better, but one abdominal plus the reference still gives the
	// subtraction, which is the part that matters. Hard-coding a division by
	// two would silently halve the residual when a cable comes loose.
	var sum float64
	var n int
	for _, node := range sensor.AbdominalNodes {
		if st := d.nodes[node]; st.seen {
			sum += st.current
			n++
		}
	}
	if n == 0 {
		return
	}

	// Mean of the abdominal nodes minus the reference. When SHE moves, every
	// node rises together and this cancels toward zero. When the fetus moves,
	// only the abdominal nodes rise and the residual survives.
	residual := sum/float64(n) - ref.current
	if residual < d.cfg.Threshold {
		return
	}

	acoustic := d.acousticAgrees(t)
	if d.cfg.RequireAcoustic && !acoustic {
		return
	}

	d.lastDetection = t
	conf := residual / (d.cfg.Threshold * 2)
	if conf > 1 {
		conf = 1
	}
	if acoustic {
		conf = conf*0.8 + 0.2
	}

	det := Detection{T: t, Residual: residual, Confidence: conf, Acoustic: acoustic}
	select {
	case d.Out <- det:
	default:
	}
}

func (d *Detector) acousticAgrees(t time.Time) bool {
	for _, s := range d.acoustic.spikes {
		if diff := s.Sub(t); diff < d.cfg.AcousticWindow && diff > -d.cfg.AcousticWindow {
			return true
		}
	}
	return false
}
