// Package kicker drives the phantom: the servo that simulates fetal movement.
//
// This is DEMO SCAFFOLDING, not the product. In a real pregnancy a fetus
// replaces the servo. Say that out loud at judging, because teams that blur it
// get asked awkward questions and teams that name it look rigorous.
//
// Its second job is to be ground truth. Every fire is timestamped and logged,
// which is more precise than anything you could extract from video, and it is
// what lets the system score itself live.
package kicker

import (
	"math/rand"
	"sync"
	"time"
)

// Strength buckets. The weak ones are the entire point: they are what a judge
// misses during the blind test, and missing them is what makes the reveal land.
const (
	Weak   = "weak"
	Medium = "medium"
	Strong = "strong"
)

// Amplitudes in g as seen at the abdominal sensors after the foam attenuates
// the push. Retune once you have real foam in the loop.
var amplitudes = map[string]float64{
	Weak:   0.16,
	Medium: 0.30,
	Strong: 0.52,
}

// Servo is whatever actually produces the kick. In simulation it injects an
// impulse into the synthetic signal; on the Pi it drives the real servo through
// the motor/servo driver board.
type Servo interface {
	Kick(strength string, amplitude float64) error
}

// Kicker fires on a randomized schedule.
type Kicker struct {
	servo  Servo
	onFire func(t time.Time, strength string)
	minGap time.Duration
	maxGap time.Duration

	mu      sync.Mutex
	running bool
	stop    chan struct{}
}

func New(servo Servo, onFire func(t time.Time, strength string)) *Kicker {
	return &Kicker{
		servo:  servo,
		onFire: onFire,
		minGap: 2 * time.Second,
		maxGap: 8 * time.Second,
	}
}

// Every method below tolerates a nil Kicker.
//
// The servo is optional hardware — on the phone path there is no phantom to
// drive at all — and a nil servo was already handled. A nil *Kicker* was not:
// handleKick called Fire unconditionally, so a build with no injector wired
// panicked on a request that anyone on the network can make without a
// session, because the phone remote is unauthenticated by design.
func (k *Kicker) Running() bool {
	if k == nil {
		return false
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.running
}

// Start begins the randomized schedule. This is blind-test mode: the judge
// never knows when a kick is coming.
func (k *Kicker) Start() {
	if k == nil {
		return
	}
	k.mu.Lock()
	if k.running {
		k.mu.Unlock()
		return
	}
	k.running = true
	k.stop = make(chan struct{})
	stop := k.stop
	k.mu.Unlock()

	go func() {
		for {
			span := k.maxGap - k.minGap
			wait := k.minGap + time.Duration(rand.Int63n(int64(span)))
			select {
			case <-stop:
				return
			case <-time.After(wait):
				k.FireRandom()
			}
		}
	}()
}

func (k *Kicker) Stop() {
	if k == nil {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.running {
		return
	}
	k.running = false
	close(k.stop)
}

// FireRandom picks a strength and kicks. Weak is weighted most heavily so the
// blind test is genuinely hard.
func (k *Kicker) FireRandom() {
	if k == nil {
		return
	}
	r := rand.Float64()
	s := Medium
	switch {
	case r < 0.45:
		s = Weak
	case r < 0.80:
		s = Medium
	default:
		s = Strong
	}
	k.Fire(s)
}

// Fire kicks at a named strength. Used by the warm-up demo, where a judge
// presses a button and watches the count move.
func (k *Kicker) Fire(strength string) {
	if k == nil {
		return
	}
	amp, ok := amplitudes[strength]
	if !ok {
		amp = amplitudes[Medium]
		strength = Medium
	}
	now := time.Now()
	// A nil servo is a legitimate configuration, not a bug: on the phone path
	// there is no phantom to drive, and a judge taps the pod with a finger
	// instead. The command is still recorded so the blind-test overlay lines
	// up, but there is nothing to actuate.
	if k.servo != nil {
		_ = k.servo.Kick(strength, amp)
	}
	if k.onFire != nil {
		k.onFire(now, strength)
	}
}

// Angles the servo swings to for each strength. REST is 10 degrees; the
// sketch clamps to 120. Retune these at hour 6 against the real foam: weak
// must be genuinely hard for a human to feel, because the weak kicks are what
// make the blind test land.
var angles = map[string]int{
	Weak:   35,
	Medium: 60,
	Strong: 95,
}

// SerialServo drives the real servo on the Arduino that has one.
//
// The servo is on the Arduino rather than the Pi because the Pi's software PWM
// jitters, and jittery kicks poison the ground truth the accuracy number rests
// on. Arduino has hardware PWM.
type SerialServo struct {
	Send func(angle int) error
}

func (s SerialServo) Kick(strength string, _ float64) error {
	a, ok := angles[strength]
	if !ok {
		a = angles[Medium]
	}
	if s.Send == nil {
		return nil
	}
	return s.Send(a)
}

// SimServo injects impulses into a simulated sensor source.
type SimServo struct {
	Inject func(amplitude float64)
}

func (s SimServo) Kick(_ string, amplitude float64) error {
	if s.Inject != nil {
		s.Inject(amplitude)
	}
	return nil
}
