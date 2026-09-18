// Package sensor defines the data model every input source produces, plus the
// sources themselves: a simulator for developing without hardware, and serial
// ingest for the Arduino accelerometer nodes.
package sensor

import (
	"math"
	"time"
)

// Node identifies where a sensor sits on the body. The reference node is the
// one that makes the whole thing work: maternal movement hits all three nodes
// at once, so subtracting the reference leaves only what happened at the
// abdomen. A single abdominal sensor on its own detects roughly half of fetal
// movements; adding the reference is what takes it to usable.
type Node string

const (
	NodeAbdoA Node = "abdo_a" // abdominal, upper
	NodeAbdoB Node = "abdo_b" // abdominal, lower
	NodeRef   Node = "ref"    // reference, worn on the back
)

// AbdominalNodes are the nodes that can see fetal movement.
var AbdominalNodes = []Node{NodeAbdoA, NodeAbdoB}

// Reading is one accelerometer sample. Timestamps are always stamped by the Pi
// on ingest, never by the Arduino: millis() on two boards and the Pi clock will
// drift apart, and a drifting clock makes the blind-test overlay look broken
// when the detector is fine.
type Reading struct {
	T    time.Time `json:"t"`
	Node Node      `json:"node"`
	AX   float64   `json:"ax"` // g
	AY   float64   `json:"ay"`
	AZ   float64   `json:"az"`
}

// Magnitude is the vector length of the acceleration, in g. Gravity is included
// here; the detector high-passes it out rather than trying to subtract a fixed
// 1g, because orientation changes as she moves.
func (r Reading) Magnitude() float64 {
	return math.Sqrt(r.AX*r.AX + r.AY*r.AY + r.AZ*r.AZ)
}

// Acoustic is one sample from the contact microphone taped to the abdomen. This
// is the independent second channel: whichever accelerometer part turns out to
// be in the Grove bag, the acoustic path does not care.
type Acoustic struct {
	T   time.Time `json:"t"`
	RMS float64   `json:"rms"`
}

// Source produces readings until its channel closes.
type Source interface {
	Readings() <-chan Reading
	Acoustics() <-chan Acoustic
	Close() error
}
